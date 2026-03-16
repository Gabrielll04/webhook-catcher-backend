package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	idempoMu           sync.Mutex
	idempoResponses    map[string]idempotencyEntry
	sse                *sseBroker
}

type idempotencyEntry struct {
	StatusCode int
	Body       []byte
	ExpiresAt  time.Time
}

const (
	maxAdminPayloadBytes = 1024 * 1024
	idempotencyTTL       = 24 * time.Hour
)

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
		idempoResponses:    make(map[string]idempotencyEntry),
		sse:                newSSEBroker(),
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
			writeError(w, http.StatusServiceUnavailable, reqID, "not_ready", "DependÃƒÂªncias indisponÃƒÂ­veis", nil)
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
	writeError(w, http.StatusNotFound, reqID, "route_not_found", "Rota nÃƒÂ£o encontrada", nil)
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
			writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "MÃƒÂ©todo nÃƒÂ£o suportado", nil)
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
			writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "MÃƒÂ©todo nÃƒÂ£o suportado", nil)
		}
		return
	}
	if len(segments) == 4 && segments[3] == "requests" && r.Method == http.MethodGet {
		a.handleListRequests(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[3] == "stream" && r.Method == http.MethodGet {
		a.handleInboxStream(w, r, segments[2])
		return
	}
	if len(segments) == 5 && segments[3] == "requests" && r.Method == http.MethodGet {
		a.handleGetRequestDetails(w, r, segments[2], segments[4])
		return
	}
	writeError(w, http.StatusNotFound, reqID, "route_not_found", "Rota nÃƒÂ£o encontrada", nil)
}

type createInboxReq struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	RetentionDays *int    `json:"retention_days"`
}

func (a *API) handleCreateInbox(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Metodo nao suportado", nil)
		return
	}

	var req createInboxReq
	if err := decodeJSONPayload(r, &req, true); err != nil {
		a.writeJSONPayloadError(w, reqID, err)
		return
	}

	idempoKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if statusCode, body, ok := a.readIdempotencyResponse(r.Method, r.URL.Path, idempoKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Idempotent-Replay", "true")
		w.WriteHeader(statusCode)
		_, _ = w.Write(body)
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

	response := map[string]any{
		"id":                 out.Inbox.ID,
		"name":               out.Inbox.Name,
		"token":              out.Inbox.Token,
		"status":             out.Inbox.Status,
		"description":        out.Inbox.Description,
		"retention_days":     out.Inbox.RetentionDays,
		"public_capture_url": out.PublicCaptureURL,
		"created_at":         out.Inbox.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	payload, _ := json.Marshal(response)
	payload = append(payload, '\n')
	if idempoKey != "" {
		a.storeIdempotencyResponse(r.Method, r.URL.Path, idempoKey, http.StatusCreated, payload)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(payload)

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
	if err := decodeJSONPayload(r, &req, true); err != nil {
		a.writeJSONPayloadError(w, reqID, err)
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
	method := strings.ToUpper(r.Method)
	if method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !isAllowedCaptureMethod(method) {
		writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "Metodo nao suportado", nil)
		return
	}

	token := strings.TrimPrefix(r.URL.Path, "/hook/")
	if token == "" {
		writeError(w, http.StatusNotFound, reqID, "inbox_not_found", "Inbox nao encontrada", nil)
		return
	}

	if err := a.services.ValidateCaptureTarget(r.Context(), token); err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}

	limited := io.LimitReader(r.Body, a.cfg.MaxPayloadBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "malformed_request", "Nao foi possivel ler o payload", nil)
		return
	}
	if int64(len(body)) > a.cfg.MaxPayloadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, reqID, "payload_too_large", "Payload acima do limite", map[string]any{"max_bytes": a.cfg.MaxPayloadBytes})
		return
	}
	if requiresPayload(method) && len(bytes.TrimSpace(body)) == 0 {
		writeError(w, http.StatusBadRequest, reqID, "invalid_payload", "Payload vazio nao permitido", nil)
		return
	}

	receivedAt := time.Now().UTC()
	captured, err := a.services.CaptureRequest(r.Context(), service.CaptureInput{
		Token:       token,
		Method:      method,
		Path:        r.URL.Path,
		QueryParams: service.ExtractQueryParams(r),
		Headers:     service.ExtractHeaders(r),
		BodyRaw:     body,
		ContentType: r.Header.Get("Content-Type"),
		RemoteIP:    clientIP(r),
		UserAgent:   r.UserAgent(),
		ReceivedAt:  receivedAt,
	})
	if err != nil {
		a.handleServiceError(w, reqID, err)
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInboxDisabled) {
			a.metrics.PersistFailure(r.Context(), routeKey(r))
		}
		return
	}

	a.metrics.CaptureRecorded(r.Context(), token, int64(len(body)))
	a.publishCapturedRequest(captured)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleInboxStream(w http.ResponseWriter, r *http.Request, inboxID string) {
	reqID := requestIDFromContext(r.Context())
	if _, err := a.services.GetInbox(r.Context(), inboxID); err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, reqID, "stream_not_supported", "Streaming nao suportado", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	events, cancel := a.sse.Subscribe(inboxID)
	defer cancel()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, ok := <-events:
			if !ok {
				return
			}
			_, _ = w.Write([]byte("event: captured_request\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func (a *API) publishCapturedRequest(item *domain.CapturedRequest) {
	if item == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"id":              item.ID,
		"inbox_id":        item.InboxID,
		"method":          item.Method,
		"path":            item.Path,
		"content_type":    item.ContentType,
		"body_size_bytes": item.BodySizeBytes,
		"remote_ip":       item.RemoteIP,
		"received_at":     item.ReceivedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	a.sse.Publish(item.InboxID, payload)
}
func (a *API) handleListRequests(w http.ResponseWriter, r *http.Request, inboxID string) {
	reqID := requestIDFromContext(r.Context())
	page := readPage(r)
	filter, err := readFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro invÃƒÂ¡lido", nil)
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
		writeError(w, http.StatusMethodNotAllowed, reqID, "method_not_allowed", "MÃƒÂ©todo nÃƒÂ£o suportado", nil)
		return
	}
	if a.cfg.CronSecret != "" {
		if r.Header.Get("X-Cron-Secret") != a.cfg.CronSecret {
			writeError(w, http.StatusUnauthorized, reqID, "unauthorized", "Cron nÃƒÂ£o autorizado", nil)
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

func decodeJSONPayload(r *http.Request, out any, requireBody bool) error {
	limited := io.LimitReader(r.Body, maxAdminPayloadBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(payload) > maxAdminPayloadBytes {
		return domain.ErrPayloadTooLarge
	}
	if requireBody && len(bytes.TrimSpace(payload)) == 0 {
		return io.EOF
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid_json_trailing")
	}
	return nil
}

func (a *API) writeJSONPayloadError(w http.ResponseWriter, reqID string, err error) {
	switch {
	case errors.Is(err, io.EOF):
		writeError(w, http.StatusBadRequest, reqID, "invalid_payload", "Payload vazio nao permitido", nil)
	case errors.Is(err, domain.ErrPayloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, reqID, "payload_too_large", "Payload acima do limite", map[string]any{"max_bytes": maxAdminPayloadBytes})
	default:
		writeError(w, http.StatusBadRequest, reqID, "invalid_json", "JSON invalido", nil)
	}
}

func (a *API) readIdempotencyResponse(method, path, key string) (int, []byte, bool) {
	if key == "" {
		return 0, nil, false
	}
	cacheKey := method + ":" + path + ":" + key
	now := time.Now().UTC()
	a.idempoMu.Lock()
	defer a.idempoMu.Unlock()
	for k, v := range a.idempoResponses {
		if now.After(v.ExpiresAt) {
			delete(a.idempoResponses, k)
		}
	}
	entry, ok := a.idempoResponses[cacheKey]
	if !ok || now.After(entry.ExpiresAt) {
		return 0, nil, false
	}
	body := make([]byte, len(entry.Body))
	copy(body, entry.Body)
	return entry.StatusCode, body, true
}

func (a *API) storeIdempotencyResponse(method, path, key string, statusCode int, body []byte) {
	if key == "" {
		return
	}
	cacheKey := method + ":" + path + ":" + key
	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)
	a.idempoMu.Lock()
	a.idempoResponses[cacheKey] = idempotencyEntry{
		StatusCode: statusCode,
		Body:       bodyCopy,
		ExpiresAt:  time.Now().UTC().Add(idempotencyTTL),
	}
	a.idempoMu.Unlock()
}

func isAllowedCaptureMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
		return true
	default:
		return false
	}
}

func requiresPayload(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
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
		writeError(w, http.StatusForbidden, reqID, "cors_origin_not_allowed", "Origem nÃƒÂ£o permitida por CORS", nil)
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

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
