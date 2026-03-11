package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
	"webhook-catcher/internal/infra/access"
	"webhook-catcher/internal/infra/config"
	"webhook-catcher/internal/infra/id"
	"webhook-catcher/internal/infra/logging"
	"webhook-catcher/internal/infra/metrics"
	"webhook-catcher/internal/infra/ratelimit"
	"webhook-catcher/internal/service"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

type API struct {
	cfg                config.Config
	services           *service.Services
	logger             *logging.Logger
	metrics            metrics.Recorder
	access             access.Validator
	limiter            *ratelimit.Limiter
	corsAllowAnyOrigin bool
	corsAllowedOrigins map[string]struct{}
}

func New(cfg config.Config, services *service.Services, logger *logging.Logger, recorder metrics.Recorder) *API {
	corsAllowAnyOrigin, corsAllowedOrigins := parseAllowedOrigins(cfg.CORSAllowedOrigins)
	return &API{
		cfg:                cfg,
		services:           services,
		logger:             logger,
		metrics:            recorder,
		access:             access.NewValidator(cfg.RequireAccess, cfg.AccessAudience),
		limiter:            ratelimit.New(cfg.AdminRateLimitRPM),
		corsAllowAnyOrigin: corsAllowAnyOrigin,
		corsAllowedOrigins: corsAllowedOrigins,
	}
}

func (a *API) Handler() http.Handler {
	return http.HandlerFunc(a.serveHTTP)
}

func (a *API) serveHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID, err := id.NewULID(start)
	if err != nil {
		reqID = strconv.FormatInt(start.UnixNano(), 10)
	}
	ctx := context.WithValue(r.Context(), requestIDKey, reqID)
	r = r.WithContext(ctx)
	w.Header().Set("X-Request-ID", reqID)

	if strings.HasPrefix(r.URL.Path, "/v1/") {
		if handled := a.handleAdminCORS(w, r, reqID, start, ctx); handled {
			return
		}

		headers := flattenHeaders(r.Header)
		if !a.access.Validate(headers) {
			writeError(w, http.StatusUnauthorized, reqID, "unauthorized", "Access não autorizado", nil)
			a.logRequest(r, reqID, http.StatusUnauthorized, start)
			a.metrics.RouteLatency(ctx, routeKey(r), http.StatusUnauthorized, time.Since(start).Milliseconds())
			return
		}
		if !a.limiter.Allow(clientIP(r), start) {
			writeError(w, http.StatusTooManyRequests, reqID, "rate_limited", "Limite de taxa excedido", nil)
			a.logRequest(r, reqID, http.StatusTooManyRequests, start)
			a.metrics.RouteLatency(ctx, routeKey(r), http.StatusTooManyRequests, time.Since(start).Milliseconds())
			return
		}
	}

	rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	a.dispatch(rw, r)
	a.logRequest(r, reqID, rw.status, start)
	a.metrics.RouteLatency(ctx, routeKey(r), rw.status, time.Since(start).Milliseconds())
}

func (a *API) dispatch(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if r.URL.Path == "/health" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339Nano)})
		return
	}
	if r.URL.Path == "/ready" {
		if err := a.services.Ready(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, reqID, "not_ready", "Dependências indisponíveis", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
		return
	}
	if r.URL.Path == "/internal/retention/run" {
		a.handleRetention(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/hook/") {
		a.handleCapture(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/inboxes") {
		a.handleInboxes(w, r)
		return
	}
	writeError(w, http.StatusNotFound, reqID, "route_not_found", "Rota não encontrada", nil)
}

func (a *API) handleInboxes(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	segments := splitPath(r.URL.Path)
	if len(segments) == 2 {
		switch r.Method {
		case http.MethodPost:
			a.handleCreateInbox(w, r)
		case http.MethodGet:
			a.handleListInboxes(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Método não suportado", nil)
		}
		return
	}
	if len(segments) == 3 {
		id := segments[2]
		switch r.Method {
		case http.MethodGet:
			a.handleGetInbox(w, r, id)
		case http.MethodPatch:
			a.handlePatchInbox(w, r, id)
		case http.MethodDelete:
			a.handleDeleteInbox(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Método não suportado", nil)
		}
		return
	}
	if len(segments) == 4 && segments[3] == "requests" && r.Method == http.MethodGet {
		a.handleListRequests(w, r, segments[2])
		return
	}
	if len(segments) == 5 && segments[3] == "requests" && r.Method == http.MethodGet {
		a.handleGetRequestDetails(w, r, segments[2], segments[4])
		return
	}
	writeError(w, http.StatusNotFound, reqID, "route_not_found", "Rota não encontrada", nil)
}

type createInboxReq struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	RetentionDays *int    `json:"retention_days"`
}

func (a *API) handleCreateInbox(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	var req createInboxReq
	if err := decodeJSON(r.Body, &req); err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_json", "JSON inválido", nil)
		return
	}
	out, err := a.services.CreateInbox(r.Context(), service.CreateInboxInput{
		Name:          req.Name,
		Description:   req.Description,
		RetentionDays: req.RetentionDays,
		Now:           time.Now().UTC(),
		RequestHost:   r.Host,
		RequestProto:  requestProto(r),
	})
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                 out.Inbox.ID,
		"name":               out.Inbox.Name,
		"token":              out.Inbox.Token,
		"status":             out.Inbox.Status,
		"description":        out.Inbox.Description,
		"retention_days":     out.Inbox.RetentionDays,
		"public_capture_url": out.PublicCaptureURL,
		"created_at":         out.Inbox.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	if active, err := a.services.ActiveInboxes(r.Context()); err == nil {
		a.metrics.SetActiveInboxes(r.Context(), active)
	}
}

func (a *API) handleListInboxes(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	page := readPage(r)
	items, total, err := a.services.ListInboxes(r.Context(), page)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":      items,
		"page":      page.Page,
		"page_size": page.PageSize,
		"total":     total,
	})
}

func (a *API) handleGetInbox(w http.ResponseWriter, r *http.Request, id string) {
	reqID := requestIDFromContext(r.Context())
	item, err := a.services.GetInbox(r.Context(), id)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type patchInboxReq struct {
	Name          *string             `json:"name"`
	Description   *string             `json:"description"`
	Status        *domain.InboxStatus `json:"status"`
	RetentionDays *int                `json:"retention_days"`
}

func (a *API) handlePatchInbox(w http.ResponseWriter, r *http.Request, id string) {
	reqID := requestIDFromContext(r.Context())
	var req patchInboxReq
	if err := decodeJSON(r.Body, &req); err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_json", "JSON inválido", nil)
		return
	}
	inbox, err := a.services.UpdateInbox(r.Context(), id, domain.InboxPatch{
		Name:          req.Name,
		Description:   req.Description,
		Status:        req.Status,
		RetentionDays: req.RetentionDays,
	}, time.Now().UTC())
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, inbox)
	if active, err := a.services.ActiveInboxes(r.Context()); err == nil {
		a.metrics.SetActiveInboxes(r.Context(), active)
	}
}

func (a *API) handleDeleteInbox(w http.ResponseWriter, r *http.Request, id string) {
	reqID := requestIDFromContext(r.Context())
	if err := a.services.DeleteInbox(r.Context(), id, time.Now().UTC()); err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if active, err := a.services.ActiveInboxes(r.Context()); err == nil {
		a.metrics.SetActiveInboxes(r.Context(), active)
	}
}

func (a *API) handleCapture(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	token := strings.TrimPrefix(r.URL.Path, "/hook/")
	if token == "" {
		writeError(w, http.StatusNotFound, reqID, "inbox_not_found", "Inbox não encontrada", nil)
		return
	}

	limited := io.LimitReader(r.Body, a.cfg.MaxPayloadBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "malformed_request", "Não foi possível ler o payload", nil)
		return
	}
	if int64(len(body)) > a.cfg.MaxPayloadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, reqID, "payload_too_large", "Payload acima do limite", map[string]any{"max_bytes": a.cfg.MaxPayloadBytes})
		return
	}

	err = a.services.CaptureRequest(r.Context(), service.CaptureInput{
		Token:       token,
		Method:      r.Method,
		Path:        r.URL.Path,
		QueryParams: service.ExtractQueryParams(r),
		Headers:     service.ExtractHeaders(r),
		BodyRaw:     body,
		ContentType: r.Header.Get("Content-Type"),
		RemoteIP:    clientIP(r),
		UserAgent:   r.UserAgent(),
		ReceivedAt:  time.Now().UTC(),
	})
	if err != nil {
		a.handleServiceError(w, reqID, err)
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInboxDisabled) {
			a.metrics.PersistFailure(r.Context(), routeKey(r))
		}
		return
	}

	a.metrics.CaptureRecorded(r.Context(), token, int64(len(body)))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListRequests(w http.ResponseWriter, r *http.Request, inboxID string) {
	reqID := requestIDFromContext(r.Context())
	page := readPage(r)
	filter, err := readFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro inválido", nil)
		return
	}
	items, total, err := a.services.ListRequests(r.Context(), inboxID, filter, page)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":      items,
		"page":      page.Page,
		"page_size": page.PageSize,
		"total":     total,
	})
}

func (a *API) handleGetRequestDetails(w http.ResponseWriter, r *http.Request, inboxID, requestID string) {
	reqID := requestIDFromContext(r.Context())
	inbox, item, err := a.services.GetRequestDetails(r.Context(), inboxID, requestID)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"inbox": inbox,
		"request": map[string]any{
			"id":              item.ID,
			"inbox_id":        item.InboxID,
			"method":          item.Method,
			"path":            item.Path,
			"query_params":    item.QueryParams,
			"headers":         item.Headers,
			"body_raw_base64": base64.StdEncoding.EncodeToString(item.BodyRaw),
			"body_size_bytes": item.BodySizeBytes,
			"content_type":    item.ContentType,
			"remote_ip":       item.RemoteIP,
			"user_agent":      item.UserAgent,
			"received_at":     item.ReceivedAt.UTC().Format(time.RFC3339Nano),
			"request_hash":    item.RequestHash,
			"truncated_body":  item.TruncatedBody,
		},
	})
}

func (a *API) handleRetention(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Método não suportado", nil)
		return
	}
	if a.cfg.CronSecret != "" {
		if r.Header.Get("X-Cron-Secret") != a.cfg.CronSecret {
			writeError(w, http.StatusUnauthorized, reqID, "unauthorized", "Cron não autorizado", nil)
			return
		}
	}
	deleted, err := a.services.RunRetention(r.Context(), time.Now().UTC())
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (a *API) handleServiceError(w http.ResponseWriter, requestID string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, requestID, "invalid_input", "Entrada inválida", nil)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, requestID, "inbox_not_found", "Inbox não encontrada", nil)
	case errors.Is(err, domain.ErrInboxDisabled):
		writeError(w, http.StatusConflict, requestID, "inbox_disabled", "Inbox desativada", nil)
	case errors.Is(err, domain.ErrPayloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, requestID, "payload_too_large", "Payload acima do limite", nil)
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, requestID, "conflict", "Conflito de dados", nil)
	default:
		writeError(w, http.StatusInternalServerError, requestID, "internal_error", "Erro interno", nil)
	}
}

func (a *API) logRequest(r *http.Request, requestID string, status int, start time.Time) {
	headers := flattenHeaders(r.Header)
	a.logger.Info("request", map[string]any{
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
		"status":     status,
		"latency_ms": time.Since(start).Milliseconds(),
		"remote_ip":  clientIP(r),
		"user_agent": r.UserAgent(),
		"headers":    maskSensitiveHeaders(headers),
	})
}

func decodeJSON(body io.Reader, out any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func requestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func flattenHeaders(h http.Header) map[string]string {
	result := make(map[string]string, len(h))
	for k, vv := range h {
		result[strings.ToLower(k)] = strings.Join(vv, ",")
	}
	return result
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func maskSensitiveHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers))
	for k, v := range headers {
		if k == "authorization" || k == "cookie" || k == "cf-access-client-secret" {
			cloned[k] = "***"
			continue
		}
		cloned[k] = v
	}
	return cloned
}

func readPage(r *http.Request) domain.Pagination {
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	return domain.Pagination{Page: page, PageSize: pageSize}
}

func readFilter(r *http.Request) (domain.ListFilter, error) {
	q := r.URL.Query()
	sortBy := q.Get("sort")
	if sortBy != "" && sortBy != "received_at" {
		return domain.ListFilter{}, errors.New("unsupported sort")
	}
	order := q.Get("order")
	if order == "" {
		if strings.HasPrefix(sortBy, "-") {
			order = "desc"
		} else {
			order = "desc"
		}
	}
	filter := domain.ListFilter{
		Method:      q.Get("method"),
		ContentType: q.Get("content_type"),
		Order:       order,
	}
	if hasBody := q.Get("has_body"); hasBody != "" {
		value, err := strconv.ParseBool(hasBody)
		if err != nil {
			return domain.ListFilter{}, err
		}
		filter.HasBody = &value
	}
	if from := q.Get("from"); from != "" {
		t, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return domain.ListFilter{}, err
		}
		filter.From = &t
	}
	if to := q.Get("to"); to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return domain.ListFilter{}, err
		}
		filter.To = &t
	}
	return filter, nil
}

func parseIntDefault(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func (a *API) handleAdminCORS(w http.ResponseWriter, r *http.Request, reqID string, start time.Time, ctx context.Context) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if !a.isOriginAllowed(origin) {
		writeError(w, http.StatusForbidden, reqID, "cors_origin_not_allowed", "Origem não permitida por CORS", nil)
		a.logRequest(r, reqID, http.StatusForbidden, start)
		a.metrics.RouteLatency(ctx, routeKey(r), http.StatusForbidden, time.Since(start).Milliseconds())
		return true
	}

	if a.corsAllowAnyOrigin {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", a.cfg.CORSAllowedMethods)
	w.Header().Set("Access-Control-Allow-Headers", a.cfg.CORSAllowedHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
	if a.cfg.CORSMaxAgeSeconds > 0 {
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(a.cfg.CORSMaxAgeSeconds))
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		a.logRequest(r, reqID, http.StatusNoContent, start)
		a.metrics.RouteLatency(ctx, routeKey(r), http.StatusNoContent, time.Since(start).Milliseconds())
		return true
	}
	return false
}

func (a *API) isOriginAllowed(origin string) bool {
	if a.corsAllowAnyOrigin {
		return true
	}
	_, ok := a.corsAllowedOrigins[origin]
	return ok
}

func parseAllowedOrigins(raw string) (bool, map[string]struct{}) {
	origins := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		if candidate == "*" {
			return true, map[string]struct{}{}
		}
		origins[candidate] = struct{}{}
	}
	return false, origins
}

func routeKey(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/hook/") {
		return "hook_capture"
	}
	if strings.HasPrefix(r.URL.Path, "/v1/inboxes") {
		return "inboxes"
	}
	if r.URL.Path == "/health" {
		return "health"
	}
	if r.URL.Path == "/ready" {
		return "ready"
	}
	if r.URL.Path == "/internal/retention/run" {
		return "retention"
	}
	return "unknown"
}

func requestProto(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		return v
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
