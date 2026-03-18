package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
)

const (
	defaultMonitoringWindow        = 15 * time.Minute
	maxMonitoringWindow            = 30 * 24 * time.Hour
	healthErrorRateThreshold       = 2.0
	healthP95ThresholdMs           = int64(500)
	defaultMonitoringLiveSnapshot  = 50
	maxMonitoringLiveSnapshotLimit = 500
)

type HookObservationInput struct {
	ObservedAt    time.Time
	InboxID       string
	Method        string
	Path          string
	StatusCode    int
	LatencyMs     int64
	BodySizeBytes int64
	HasBody       bool
	RemoteIP      string
	UserAgent     string
	Country       string
	ContentType   string
}

func (s *Services) ResolveInboxIDByToken(ctx context.Context, token string) (string, error) {
	inbox, err := s.store.GetInboxByToken(ctx, token)
	if err != nil {
		return "", err
	}
	return inbox.ID, nil
}

func (s *Services) RecordHookObservation(ctx context.Context, in HookObservationInput) (*domain.MonitoringLiveEvent, error) {
	observedAt := in.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if in.LatencyMs < 0 {
		in.LatencyMs = 0
	}
	if in.BodySizeBytes < 0 {
		in.BodySizeBytes = 0
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = "UNKNOWN"
	}
	statusCode := in.StatusCode
	if statusCode <= 0 {
		statusCode = 500
	}
	statusClass := statusClassFromCode(statusCode)
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	if country == "" {
		country = "UNKNOWN"
	}
	textBlob := strings.ToLower(fmt.Sprintf("%s %s %s %s %s %s %s", method, in.Path, statusClass, in.RemoteIP, country, in.UserAgent, in.ContentType))

	obs := &domain.HookObservation{
		ObservedAt:    observedAt,
		BucketMinute:  observedAt.Truncate(time.Minute),
		InboxID:       strings.TrimSpace(in.InboxID),
		Method:        method,
		Path:          in.Path,
		StatusCode:    statusCode,
		StatusClass:   statusClass,
		LatencyMs:     in.LatencyMs,
		BodySizeBytes: in.BodySizeBytes,
		HasBody:       in.HasBody,
		RemoteIP:      in.RemoteIP,
		UserAgent:     in.UserAgent,
		Country:       country,
		ContentType:   in.ContentType,
		TextBlob:      textBlob,
	}
	if err := s.store.RecordHookObservation(ctx, obs); err != nil {
		return nil, err
	}

	return &domain.MonitoringLiveEvent{
		ObservedAt:    obs.ObservedAt,
		InboxID:       obs.InboxID,
		Method:        obs.Method,
		Path:          obs.Path,
		StatusCode:    obs.StatusCode,
		StatusClass:   obs.StatusClass,
		LatencyMs:     obs.LatencyMs,
		BodySizeBytes: obs.BodySizeBytes,
		RemoteIP:      obs.RemoteIP,
		Country:       obs.Country,
		UserAgent:     obs.UserAgent,
	}, nil
}

func (s *Services) GetMonitoringSummary(ctx context.Context, q domain.MonitoringQuery) (*domain.MonitoringSummary, error) {
	normalized, err := normalizeMonitoringQuery(q)
	if err != nil {
		return nil, err
	}
	return s.store.GetMonitoringSummary(ctx, normalized)
}

func (s *Services) GetMonitoringTimeseries(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringTimeseriesPoint, error) {
	normalized, err := normalizeMonitoringQuery(q)
	if err != nil {
		return nil, err
	}
	return s.store.GetMonitoringTimeseries(ctx, normalized)
}

func (s *Services) GetMonitoringBreakdown(ctx context.Context, q domain.MonitoringQuery, dimension string, limit int) ([]domain.MonitoringBreakdownItem, error) {
	normalized, err := normalizeMonitoringQuery(q)
	if err != nil {
		return nil, err
	}
	dim := strings.ToLower(strings.TrimSpace(dimension))
	switch dim {
	case "method", "status", "path", "ip", "country", "user_agent":
	default:
		return nil, domain.ErrInvalidInput
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	return s.store.GetMonitoringBreakdown(ctx, normalized, dim, limit)
}

func (s *Services) GetMonitoringInboxHealth(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringInboxHealth, error) {
	normalized, err := normalizeMonitoringQuery(q)
	if err != nil {
		return nil, err
	}
	stats, err := s.store.ListMonitoringInboxStats(ctx, normalized)
	if err != nil {
		return nil, err
	}

	items := make([]domain.MonitoringInboxHealth, 0, len(stats))
	for _, st := range stats {
		errorRate := 0.0
		if st.Requests > 0 {
			errorRate = (float64(st.Errors) / float64(st.Requests)) * 100
		}
		p95 := percentileFromBuckets(st.Buckets, st.Requests, 0.95)
		state := "healthy"
		switch {
		case st.Requests == 0:
			state = "no_traffic"
		case errorRate >= healthErrorRateThreshold || p95 >= healthP95ThresholdMs:
			state = "unstable"
		}
		items = append(items, domain.MonitoringInboxHealth{
			InboxID:      st.InboxID,
			InboxName:    st.InboxName,
			State:        state,
			Requests:     st.Requests,
			Errors:       st.Errors,
			ErrorRate:    errorRate,
			LatencyP95Ms: p95,
		})
	}
	return items, nil
}

func (s *Services) GetMonitoringLiveSnapshot(ctx context.Context, q domain.MonitoringQuery, limit int) ([]domain.MonitoringLiveEvent, error) {
	normalized, err := normalizeMonitoringQuery(q)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultMonitoringLiveSnapshot
	}
	if limit > maxMonitoringLiveSnapshotLimit {
		limit = maxMonitoringLiveSnapshotLimit
	}
	return s.store.ListMonitoringLiveEvents(ctx, normalized, limit)
}

func normalizeMonitoringQuery(q domain.MonitoringQuery) (domain.MonitoringQuery, error) {
	now := time.Now().UTC()
	if q.To.IsZero() && q.From.IsZero() {
		q.To = now
		q.From = now.Add(-defaultMonitoringWindow)
	} else if q.To.IsZero() {
		q.To = q.From.Add(defaultMonitoringWindow)
	} else if q.From.IsZero() {
		q.From = q.To.Add(-defaultMonitoringWindow)
	}
	q.From = q.From.UTC()
	q.To = q.To.UTC()
	if q.From.After(q.To) {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}
	if q.To.Sub(q.From) > maxMonitoringWindow {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}

	if q.MinBodySize != nil && *q.MinBodySize < 0 {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}
	if q.MaxBodySize != nil && *q.MaxBodySize < 0 {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}
	if q.MinBodySize != nil && q.MaxBodySize != nil && *q.MinBodySize > *q.MaxBodySize {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}

	q.Method = strings.ToUpper(strings.TrimSpace(q.Method))
	q.StatusClass = strings.ToLower(strings.TrimSpace(q.StatusClass))
	if q.StatusClass != "" && q.StatusClass != "2xx" && q.StatusClass != "4xx" && q.StatusClass != "5xx" {
		return domain.MonitoringQuery{}, domain.ErrInvalidInput
	}
	q.Country = strings.ToUpper(strings.TrimSpace(q.Country))
	q.PathContains = strings.TrimSpace(q.PathContains)
	q.RemoteIP = strings.TrimSpace(q.RemoteIP)
	q.TextQuery = strings.TrimSpace(q.TextQuery)
	q.InboxID = strings.TrimSpace(q.InboxID)

	return q, nil
}

func statusClassFromCode(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "5xx"
	}
}

func percentileFromBuckets(cumulative map[int64]int64, total int64, q float64) int64 {
	if total <= 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * q))
	for _, bound := range domain.LatencyHistogramBoundsMs {
		if cumulative[bound] >= target {
			return bound
		}
	}
	return domain.LatencyHistogramBoundsMs[len(domain.LatencyHistogramBoundsMs)-1]
}
