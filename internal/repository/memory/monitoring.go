package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
)

type memoryMinuteRollup struct {
	RequestsTotal int64
	ErrorsTotal   int64
	Status2xx     int64
	Status4xx     int64
	Status5xx     int64
	LatencySumMs  int64
	BodyBytes     int64
	Buckets       map[int64]int64
}

type memoryAggregate struct {
	RequestsTotal int64
	ErrorsTotal   int64
	Status2xx     int64
	Status4xx     int64
	Status5xx     int64
	LatencySumMs  int64
	Buckets       map[int64]int64
}

func newMemoryAggregate() memoryAggregate {
	b := make(map[int64]int64, len(domain.LatencyHistogramBoundsMs))
	for _, bound := range domain.LatencyHistogramBoundsMs {
		b[bound] = 0
	}
	return memoryAggregate{Buckets: b}
}

func (s *Store) RecordHookObservation(_ context.Context, obs *domain.HookObservation) error {
	if obs == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	copyObs := *obs
	copyObs.ObservedAt = copyObs.ObservedAt.UTC()
	copyObs.BucketMinute = copyObs.BucketMinute.UTC()
	s.monitoringEvents = append(s.monitoringEvents, copyObs)

	rollupKey := minuteRollupKey(copyObs.BucketMinute, copyObs.InboxID)
	rollup, ok := s.minuteRollups[rollupKey]
	if !ok {
		rollup = memoryMinuteRollup{Buckets: map[int64]int64{}}
		for _, bound := range domain.LatencyHistogramBoundsMs {
			rollup.Buckets[bound] = 0
		}
	}
	rollup.RequestsTotal++
	if copyObs.StatusClass == "4xx" || copyObs.StatusClass == "5xx" {
		rollup.ErrorsTotal++
	}
	switch copyObs.StatusClass {
	case "2xx":
		rollup.Status2xx++
	case "4xx":
		rollup.Status4xx++
	case "5xx":
		rollup.Status5xx++
	}
	rollup.LatencySumMs += copyObs.LatencyMs
	rollup.BodyBytes += copyObs.BodySizeBytes
	for _, bound := range domain.LatencyHistogramBoundsMs {
		if copyObs.LatencyMs <= bound {
			rollup.Buckets[bound]++
		}
	}
	s.minuteRollups[rollupKey] = rollup

	dimValues := map[string]string{
		"method":     copyObs.Method,
		"status":     copyObs.StatusClass,
		"path":       copyObs.Path,
		"ip":         copyObs.RemoteIP,
		"country":    copyObs.Country,
		"user_agent": copyObs.UserAgent,
	}
	for dim, value := range dimValues {
		key := minuteDimensionKey(copyObs.BucketMinute, copyObs.InboxID, dim, value)
		s.minuteDims[key]++
	}

	return nil
}

func (s *Store) GetMonitoringSummary(_ context.Context, q domain.MonitoringQuery) (*domain.MonitoringSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agg := newMemoryAggregate()
	lastMinute := q.To.UTC().Truncate(time.Minute)
	var requestsLastMinute int64
	for _, obs := range s.monitoringEvents {
		if !matchesMonitoringQuery(obs, q) {
			continue
		}
		accumulateMonitoringAggregate(&agg, obs)
		if obs.BucketMinute.Equal(lastMinute) {
			requestsLastMinute++
		}
	}

	durationMin := q.To.Sub(q.From).Minutes()
	if durationMin <= 0 {
		durationMin = 1
	}
	errorRate := 0.0
	latAvg := 0.0
	if agg.RequestsTotal > 0 {
		errorRate = (float64(agg.ErrorsTotal) / float64(agg.RequestsTotal)) * 100
		latAvg = float64(agg.LatencySumMs) / float64(agg.RequestsTotal)
	}

	return &domain.MonitoringSummary{
		RequestsTotal:      agg.RequestsTotal,
		ErrorsTotal:        agg.ErrorsTotal,
		ErrorRate:          errorRate,
		LatencyAvgMs:       latAvg,
		LatencyP95Ms:       percentileFromCumulativeBuckets(agg.Buckets, agg.RequestsTotal, 0.95),
		LatencyP99Ms:       percentileFromCumulativeBuckets(agg.Buckets, agg.RequestsTotal, 0.99),
		RequestsPerMinute:  float64(agg.RequestsTotal) / durationMin,
		RequestsLastMinute: requestsLastMinute,
		StatusCodes: map[string]int64{
			"2xx": agg.Status2xx,
			"4xx": agg.Status4xx,
			"5xx": agg.Status5xx,
		},
	}, nil
}

func (s *Store) GetMonitoringTimeseries(_ context.Context, q domain.MonitoringQuery) ([]domain.MonitoringTimeseriesPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	series := make(map[time.Time]memoryAggregate)
	for _, obs := range s.monitoringEvents {
		if !matchesMonitoringQuery(obs, q) {
			continue
		}
		bucket := obs.BucketMinute.UTC()
		agg, ok := series[bucket]
		if !ok {
			agg = newMemoryAggregate()
		}
		accumulateMonitoringAggregate(&agg, obs)
		series[bucket] = agg
	}

	points := make([]domain.MonitoringTimeseriesPoint, 0)
	start := q.From.UTC().Truncate(time.Minute)
	end := q.To.UTC().Truncate(time.Minute)
	for ts := start; !ts.After(end); ts = ts.Add(time.Minute) {
		agg, ok := series[ts]
		if !ok {
			agg = newMemoryAggregate()
		}
		latAvg := 0.0
		if agg.RequestsTotal > 0 {
			latAvg = float64(agg.LatencySumMs) / float64(agg.RequestsTotal)
		}
		points = append(points, domain.MonitoringTimeseriesPoint{
			TS:       ts,
			Requests: agg.RequestsTotal,
			Errors:   agg.ErrorsTotal,
			LatAvgMs: latAvg,
			P95Ms:    percentileFromCumulativeBuckets(agg.Buckets, agg.RequestsTotal, 0.95),
			P99Ms:    percentileFromCumulativeBuckets(agg.Buckets, agg.RequestsTotal, 0.99),
		})
	}
	return points, nil
}

func (s *Store) GetMonitoringBreakdown(_ context.Context, q domain.MonitoringQuery, dimension string, limit int) ([]domain.MonitoringBreakdownItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}
	counts := map[string]int64{}
	var total int64
	for _, obs := range s.monitoringEvents {
		if !matchesMonitoringQuery(obs, q) {
			continue
		}
		total++
		value := observationDimensionValue(obs, dimension)
		if value == "" {
			continue
		}
		counts[value]++
	}

	items := make([]domain.MonitoringBreakdownItem, 0, len(counts))
	for value, count := range counts {
		percentage := 0.0
		if total > 0 {
			percentage = (float64(count) / float64(total)) * 100
		}
		items = append(items, domain.MonitoringBreakdownItem{Dimension: dimension, Value: value, Count: count, Percentage: percentage})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Value < items[j].Value
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) ListMonitoringInboxStats(_ context.Context, q domain.MonitoringQuery) ([]domain.MonitoringInboxStat, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inboxes := make([]domain.Inbox, 0, len(s.inboxes))
	for _, inbox := range s.inboxes {
		if inbox.DeletedAt != nil {
			continue
		}
		if q.InboxID != "" && inbox.ID != q.InboxID {
			continue
		}
		inboxes = append(inboxes, inbox)
	}
	sort.Slice(inboxes, func(i, j int) bool { return inboxes[i].CreatedAt.Before(inboxes[j].CreatedAt) })

	statsMap := map[string]*domain.MonitoringInboxStat{}
	for _, inbox := range inboxes {
		b := make(map[int64]int64, len(domain.LatencyHistogramBoundsMs))
		for _, bound := range domain.LatencyHistogramBoundsMs {
			b[bound] = 0
		}
		statsMap[inbox.ID] = &domain.MonitoringInboxStat{InboxID: inbox.ID, InboxName: inbox.Name, Buckets: b}
	}

	for _, obs := range s.monitoringEvents {
		if !matchesMonitoringQuery(obs, q) {
			continue
		}
		st, ok := statsMap[obs.InboxID]
		if !ok {
			continue
		}
		st.Requests++
		if obs.StatusClass == "4xx" || obs.StatusClass == "5xx" {
			st.Errors++
		}
		st.LatencySumMs += obs.LatencyMs
		for _, bound := range domain.LatencyHistogramBoundsMs {
			if obs.LatencyMs <= bound {
				st.Buckets[bound]++
			}
		}
	}

	out := make([]domain.MonitoringInboxStat, 0, len(inboxes))
	for _, inbox := range inboxes {
		if st, ok := statsMap[inbox.ID]; ok {
			out = append(out, *st)
		}
	}
	return out, nil
}

func (s *Store) ListMonitoringLiveEvents(_ context.Context, q domain.MonitoringQuery, limit int) ([]domain.MonitoringLiveEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	filtered := make([]domain.HookObservation, 0, len(s.monitoringEvents))
	for _, obs := range s.monitoringEvents {
		if !matchesMonitoringQuery(obs, q) {
			continue
		}
		filtered = append(filtered, obs)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ObservedAt.After(filtered[j].ObservedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ObservedAt.Before(filtered[j].ObservedAt)
	})

	out := make([]domain.MonitoringLiveEvent, 0, len(filtered))
	for _, obs := range filtered {
		out = append(out, domain.MonitoringLiveEvent{
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
		})
	}
	return out, nil
}
func matchesMonitoringQuery(obs domain.HookObservation, q domain.MonitoringQuery) bool {
	if obs.ObservedAt.Before(q.From) || obs.ObservedAt.After(q.To) {
		return false
	}
	if q.InboxID != "" && obs.InboxID != q.InboxID {
		return false
	}
	if q.Method != "" && !strings.EqualFold(obs.Method, q.Method) {
		return false
	}
	if q.StatusClass != "" && !strings.EqualFold(obs.StatusClass, q.StatusClass) {
		return false
	}
	if q.PathContains != "" && !strings.Contains(strings.ToLower(obs.Path), strings.ToLower(q.PathContains)) {
		return false
	}
	if q.HasBody != nil && *q.HasBody != obs.HasBody {
		return false
	}
	if q.MinBodySize != nil && obs.BodySizeBytes < *q.MinBodySize {
		return false
	}
	if q.MaxBodySize != nil && obs.BodySizeBytes > *q.MaxBodySize {
		return false
	}
	if q.RemoteIP != "" && obs.RemoteIP != q.RemoteIP {
		return false
	}
	if q.Country != "" && !strings.EqualFold(obs.Country, q.Country) {
		return false
	}
	if q.TextQuery != "" {
		needle := strings.ToLower(strings.TrimSpace(q.TextQuery))
		haystack := strings.ToLower(obs.TextBlob)
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func observationDimensionValue(obs domain.HookObservation, dimension string) string {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "method":
		return obs.Method
	case "status":
		return obs.StatusClass
	case "path":
		return obs.Path
	case "ip":
		return obs.RemoteIP
	case "country":
		return obs.Country
	case "user_agent":
		return obs.UserAgent
	default:
		return ""
	}
}

func accumulateMonitoringAggregate(agg *memoryAggregate, obs domain.HookObservation) {
	agg.RequestsTotal++
	if obs.StatusClass == "4xx" || obs.StatusClass == "5xx" {
		agg.ErrorsTotal++
	}
	switch obs.StatusClass {
	case "2xx":
		agg.Status2xx++
	case "4xx":
		agg.Status4xx++
	case "5xx":
		agg.Status5xx++
	}
	agg.LatencySumMs += obs.LatencyMs
	for _, bound := range domain.LatencyHistogramBoundsMs {
		if obs.LatencyMs <= bound {
			agg.Buckets[bound]++
		}
	}
}

func percentileFromCumulativeBuckets(cumulative map[int64]int64, total int64, q float64) int64 {
	if total <= 0 {
		return 0
	}
	target := int64(float64(total)*q + 0.999999)
	for _, bound := range domain.LatencyHistogramBoundsMs {
		if cumulative[bound] >= target {
			return bound
		}
	}
	return domain.LatencyHistogramBoundsMs[len(domain.LatencyHistogramBoundsMs)-1]
}

func minuteRollupKey(bucket time.Time, inboxID string) string {
	return bucket.UTC().Format(time.RFC3339) + "|" + inboxID
}

func minuteDimensionKey(bucket time.Time, inboxID, dimension, value string) string {
	return fmt.Sprintf("%s|%s|%s|%s", bucket.UTC().Format(time.RFC3339), inboxID, dimension, value)
}
