package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
)

const monitoringLiveChannelKey = "__monitoring_live__"

func (a *API) handleMonitoringSummary(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q, err := readMonitoringQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro invalido", nil)
		return
	}
	out, err := a.services.GetMonitoringSummary(r.Context(), q)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleMonitoringTimeseries(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q, err := readMonitoringQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro invalido", nil)
		return
	}
	points, err := a.services.GetMonitoringTimeseries(r.Context(), q)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": points})
}

func (a *API) handleMonitoringBreakdown(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q, err := readMonitoringQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro invalido", nil)
		return
	}
	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dimension == "" {
		dimension = "method"
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 10)
	items, err := a.services.GetMonitoringBreakdown(r.Context(), q, dimension, limit)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dimension": strings.ToLower(dimension), "data": items})
}

func (a *API) handleMonitoringInboxesHealth(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q, err := readMonitoringQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, reqID, "invalid_filter", "Filtro invalido", nil)
		return
	}
	items, err := a.services.GetMonitoringInboxHealth(r.Context(), q)
	if err != nil {
		a.handleServiceError(w, reqID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *API) handleMonitoringLiveStream(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
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

	events, cancel := a.sse.Subscribe(monitoringLiveChannelKey)
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
			_, _ = w.Write([]byte("event: hook_observation\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			// Compatibility for clients using EventSource.onmessage.
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

func (a *API) publishMonitoringLiveEvent(event *domain.MonitoringLiveEvent) {
	if event == nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	a.sse.Publish(monitoringLiveChannelKey, payload)
}

func readMonitoringQuery(r *http.Request) (domain.MonitoringQuery, error) {
	qv := r.URL.Query()
	now := time.Now().UTC()
	q := domain.MonitoringQuery{
		InboxID:      strings.TrimSpace(qv.Get("inbox_id")),
		Method:       strings.TrimSpace(qv.Get("method")),
		StatusClass:  strings.TrimSpace(firstNonEmpty(qv.Get("status_class"), qv.Get("status"))),
		PathContains: strings.TrimSpace(qv.Get("path_contains")),
		RemoteIP:     strings.TrimSpace(qv.Get("remote_ip")),
		Country:      strings.TrimSpace(qv.Get("country")),
		TextQuery:    strings.TrimSpace(qv.Get("q")),
	}

	if from := strings.TrimSpace(qv.Get("from")); from != "" {
		t, err := parseMonitoringTimeValue(from, now)
		if err != nil {
			return domain.MonitoringQuery{}, err
		}
		q.From = t
	}
	if to := strings.TrimSpace(qv.Get("to")); to != "" {
		t, err := parseMonitoringTimeValue(to, now)
		if err != nil {
			return domain.MonitoringQuery{}, err
		}
		q.To = t
	}
	if hasBody := strings.TrimSpace(qv.Get("has_body")); hasBody != "" {
		value, err := strconv.ParseBool(hasBody)
		if err != nil {
			return domain.MonitoringQuery{}, err
		}
		q.HasBody = &value
	}
	if min := strings.TrimSpace(qv.Get("min_body_size")); min != "" {
		value, err := strconv.ParseInt(min, 10, 64)
		if err != nil {
			return domain.MonitoringQuery{}, err
		}
		q.MinBodySize = &value
	}
	if max := strings.TrimSpace(qv.Get("max_body_size")); max != "" {
		value, err := strconv.ParseInt(max, 10, 64)
		if err != nil {
			return domain.MonitoringQuery{}, err
		}
		q.MaxBodySize = &value
	}
	return q, nil
}

func parseMonitoringTimeValue(value string, now time.Time) (time.Time, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return time.Time{}, errors.New("empty time value")
	}
	lower := strings.ToLower(raw)
	if lower == "now" {
		return now.UTC(), nil
	}
	if strings.HasPrefix(lower, "now+") || strings.HasPrefix(lower, "now-") {
		offset := raw[3:]
		d, err := time.ParseDuration(offset)
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(d).UTC(), nil
	}
	return time.Parse(time.RFC3339, raw)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
