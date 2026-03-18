package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
)

func (s *Store) RecordHookObservation(ctx context.Context, obs *domain.HookObservation) error {
	if obs == nil {
		return nil
	}
	bucket := obs.BucketMinute.UTC().Format(time.RFC3339)
	observedAt := obs.ObservedAt.UTC().Format(time.RFC3339Nano)
	inboxKey := obs.InboxID
	if inboxKey == "" {
		inboxKey = "_unknown"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	const insertEvent = `
		INSERT INTO monitoring_hook_events (
			observed_at,bucket_minute,inbox_id,method,path,status_code,status_class,latency_ms,
			body_size_bytes,has_body,remote_ip,user_agent,country,content_type,text_blob
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = tx.ExecContext(ctx, insertEvent,
		observedAt,
		bucket,
		nullableEmptyString(obs.InboxID),
		obs.Method,
		obs.Path,
		obs.StatusCode,
		obs.StatusClass,
		obs.LatencyMs,
		obs.BodySizeBytes,
		boolToInt(obs.HasBody),
		obs.RemoteIP,
		obs.UserAgent,
		obs.Country,
		obs.ContentType,
		obs.TextBlob,
	)
	if err != nil {
		return err
	}

	le := func(bound int64) int {
		if obs.LatencyMs <= bound {
			return 1
		}
		return 0
	}
	const upsertRollup = `
		INSERT INTO monitoring_minute_rollups (
			bucket_minute,inbox_id,requests_total,errors_total,status_2xx,status_4xx,status_5xx,
			latency_sum_ms,body_bytes_total,
			lat_le_10,lat_le_25,lat_le_50,lat_le_100,lat_le_250,lat_le_500,lat_le_1000,lat_le_2000,lat_le_5000
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_minute,inbox_id) DO UPDATE SET
			requests_total = requests_total + excluded.requests_total,
			errors_total = errors_total + excluded.errors_total,
			status_2xx = status_2xx + excluded.status_2xx,
			status_4xx = status_4xx + excluded.status_4xx,
			status_5xx = status_5xx + excluded.status_5xx,
			latency_sum_ms = latency_sum_ms + excluded.latency_sum_ms,
			body_bytes_total = body_bytes_total + excluded.body_bytes_total,
			lat_le_10 = lat_le_10 + excluded.lat_le_10,
			lat_le_25 = lat_le_25 + excluded.lat_le_25,
			lat_le_50 = lat_le_50 + excluded.lat_le_50,
			lat_le_100 = lat_le_100 + excluded.lat_le_100,
			lat_le_250 = lat_le_250 + excluded.lat_le_250,
			lat_le_500 = lat_le_500 + excluded.lat_le_500,
			lat_le_1000 = lat_le_1000 + excluded.lat_le_1000,
			lat_le_2000 = lat_le_2000 + excluded.lat_le_2000,
			lat_le_5000 = lat_le_5000 + excluded.lat_le_5000`
	_, err = tx.ExecContext(ctx, upsertRollup,
		bucket,
		inboxKey,
		1,
		boolToInt(obs.StatusClass == "4xx" || obs.StatusClass == "5xx"),
		boolToInt(obs.StatusClass == "2xx"),
		boolToInt(obs.StatusClass == "4xx"),
		boolToInt(obs.StatusClass == "5xx"),
		obs.LatencyMs,
		obs.BodySizeBytes,
		le(10), le(25), le(50), le(100), le(250), le(500), le(1000), le(2000), le(5000),
	)
	if err != nil {
		return err
	}

	const upsertDim = `
		INSERT INTO monitoring_minute_dimensions (bucket_minute,inbox_id,dimension,value,count)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(bucket_minute,inbox_id,dimension,value) DO UPDATE SET
			count = count + 1`
	dims := map[string]string{
		"method":     obs.Method,
		"status":     obs.StatusClass,
		"path":       obs.Path,
		"ip":         obs.RemoteIP,
		"country":    obs.Country,
		"user_agent": obs.UserAgent,
	}
	for dim, value := range dims {
		if _, err = tx.ExecContext(ctx, upsertDim, bucket, inboxKey, dim, value); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetMonitoringSummary(ctx context.Context, q domain.MonitoringQuery) (*domain.MonitoringSummary, error) {
	whereClause, args := buildMonitoringEventsWhere(q)
	query := fmt.Sprintf(`
		SELECT observed_at, status_class, latency_ms, bucket_minute
		FROM monitoring_hook_events
		WHERE %s`, whereClause)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type aggregate struct {
		requests int64
		errors   int64
		latency  int64
		status2  int64
		status4  int64
		status5  int64
		buckets  map[int64]int64
	}
	agg := aggregate{buckets: map[int64]int64{}}
	for _, bound := range domain.LatencyHistogramBoundsMs {
		agg.buckets[bound] = 0
	}

	lastMinute := q.To.UTC().Truncate(time.Minute)
	var requestsLastMinute int64
	for rows.Next() {
		var observed, statusClass, bucket string
		var latency int64
		if err := rows.Scan(&observed, &statusClass, &latency, &bucket); err != nil {
			return nil, err
		}
		agg.requests++
		if statusClass == "4xx" || statusClass == "5xx" {
			agg.errors++
		}
		switch statusClass {
		case "2xx":
			agg.status2++
		case "4xx":
			agg.status4++
		case "5xx":
			agg.status5++
		}
		agg.latency += latency
		for _, bound := range domain.LatencyHistogramBoundsMs {
			if latency <= bound {
				agg.buckets[bound]++
			}
		}
		bucketTime, parseErr := time.Parse(time.RFC3339, bucket)
		if parseErr == nil && bucketTime.Equal(lastMinute) {
			requestsLastMinute++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	durationMin := q.To.Sub(q.From).Minutes()
	if durationMin <= 0 {
		durationMin = 1
	}
	errorRate := 0.0
	latAvg := 0.0
	if agg.requests > 0 {
		errorRate = (float64(agg.errors) / float64(agg.requests)) * 100
		latAvg = float64(agg.latency) / float64(agg.requests)
	}
	return &domain.MonitoringSummary{
		RequestsTotal:      agg.requests,
		ErrorsTotal:        agg.errors,
		ErrorRate:          errorRate,
		LatencyAvgMs:       latAvg,
		LatencyP95Ms:       percentileFromBuckets(agg.buckets, agg.requests, 0.95),
		LatencyP99Ms:       percentileFromBuckets(agg.buckets, agg.requests, 0.99),
		RequestsPerMinute:  float64(agg.requests) / durationMin,
		RequestsLastMinute: requestsLastMinute,
		StatusCodes: map[string]int64{
			"2xx": agg.status2,
			"4xx": agg.status4,
			"5xx": agg.status5,
		},
	}, nil
}

func (s *Store) GetMonitoringTimeseries(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringTimeseriesPoint, error) {
	whereClause, args := buildMonitoringEventsWhere(q)
	query := fmt.Sprintf(`
		SELECT bucket_minute, status_class, latency_ms
		FROM monitoring_hook_events
		WHERE %s`, whereClause)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type aggregate struct {
		requests int64
		errors   int64
		latency  int64
		buckets  map[int64]int64
	}
	series := map[time.Time]aggregate{}

	for rows.Next() {
		var bucket, statusClass string
		var latency int64
		if err := rows.Scan(&bucket, &statusClass, &latency); err != nil {
			return nil, err
		}
		ts, parseErr := time.Parse(time.RFC3339, bucket)
		if parseErr != nil {
			return nil, parseErr
		}
		agg, ok := series[ts]
		if !ok {
			agg = aggregate{buckets: map[int64]int64{}}
			for _, bound := range domain.LatencyHistogramBoundsMs {
				agg.buckets[bound] = 0
			}
		}
		agg.requests++
		if statusClass == "4xx" || statusClass == "5xx" {
			agg.errors++
		}
		agg.latency += latency
		for _, bound := range domain.LatencyHistogramBoundsMs {
			if latency <= bound {
				agg.buckets[bound]++
			}
		}
		series[ts] = agg
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	start := q.From.UTC().Truncate(time.Minute)
	end := q.To.UTC().Truncate(time.Minute)
	points := make([]domain.MonitoringTimeseriesPoint, 0)
	for ts := start; !ts.After(end); ts = ts.Add(time.Minute) {
		agg, ok := series[ts]
		if !ok {
			agg = aggregate{buckets: map[int64]int64{}}
			for _, bound := range domain.LatencyHistogramBoundsMs {
				agg.buckets[bound] = 0
			}
		}
		latAvg := 0.0
		if agg.requests > 0 {
			latAvg = float64(agg.latency) / float64(agg.requests)
		}
		points = append(points, domain.MonitoringTimeseriesPoint{
			TS:       ts,
			Requests: agg.requests,
			Errors:   agg.errors,
			LatAvgMs: latAvg,
			P95Ms:    percentileFromBuckets(agg.buckets, agg.requests, 0.95),
			P99Ms:    percentileFromBuckets(agg.buckets, agg.requests, 0.99),
		})
	}
	return points, nil
}

func (s *Store) GetMonitoringBreakdown(ctx context.Context, q domain.MonitoringQuery, dimension string, limit int) ([]domain.MonitoringBreakdownItem, error) {
	if limit <= 0 {
		limit = 10
	}
	field := breakdownField(dimension)
	if field == "" {
		return nil, domain.ErrInvalidInput
	}
	whereClause, args := buildMonitoringEventsWhere(q)

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM monitoring_hook_events WHERE %s", whereClause)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT %s AS value, COUNT(*) AS c
		FROM monitoring_hook_events
		WHERE %s
		GROUP BY value
		ORDER BY c DESC, value ASC
		LIMIT ?`, field, whereClause)
	argsWithLimit := append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, argsWithLimit...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]domain.MonitoringBreakdownItem, 0, limit)
	for rows.Next() {
		var value string
		var count int64
		if err := rows.Scan(&value, &count); err != nil {
			return nil, err
		}
		pct := 0.0
		if total > 0 {
			pct = (float64(count) / float64(total)) * 100
		}
		items = append(items, domain.MonitoringBreakdownItem{
			Dimension:  dimension,
			Value:      value,
			Count:      count,
			Percentage: pct,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ListMonitoringInboxStats(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringInboxStat, error) {
	inboxWhere := "deleted_at IS NULL"
	args := make([]any, 0)
	if q.InboxID != "" {
		inboxWhere += " AND id = ?"
		args = append(args, q.InboxID)
	}
	inboxQuery := fmt.Sprintf("SELECT id,name FROM inboxes WHERE %s ORDER BY created_at ASC", inboxWhere)
	inboxRows, err := s.db.QueryContext(ctx, inboxQuery, args...)
	if err != nil {
		return nil, err
	}
	defer inboxRows.Close()

	statsMap := map[string]domain.MonitoringInboxStat{}
	order := make([]string, 0)
	for inboxRows.Next() {
		var id, name string
		if err := inboxRows.Scan(&id, &name); err != nil {
			return nil, err
		}
		b := make(map[int64]int64, len(domain.LatencyHistogramBoundsMs))
		for _, bound := range domain.LatencyHistogramBoundsMs {
			b[bound] = 0
		}
		statsMap[id] = domain.MonitoringInboxStat{InboxID: id, InboxName: name, Buckets: b}
		order = append(order, id)
	}
	if err := inboxRows.Err(); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return []domain.MonitoringInboxStat{}, nil
	}

	whereClause, eventArgs := buildMonitoringEventsWhere(q)
	eventQuery := fmt.Sprintf(`
		SELECT inbox_id, status_class, latency_ms
		FROM monitoring_hook_events
		WHERE %s`, whereClause)
	eventRows, err := s.db.QueryContext(ctx, eventQuery, eventArgs...)
	if err != nil {
		return nil, err
	}
	defer eventRows.Close()

	for eventRows.Next() {
		var inboxID sql.NullString
		var statusClass string
		var latency int64
		if err := eventRows.Scan(&inboxID, &statusClass, &latency); err != nil {
			return nil, err
		}
		if !inboxID.Valid {
			continue
		}
		st, ok := statsMap[inboxID.String]
		if !ok {
			continue
		}
		st.Requests++
		if statusClass == "4xx" || statusClass == "5xx" {
			st.Errors++
		}
		st.LatencySumMs += latency
		for _, bound := range domain.LatencyHistogramBoundsMs {
			if latency <= bound {
				st.Buckets[bound]++
			}
		}
		statsMap[inboxID.String] = st
	}
	if err := eventRows.Err(); err != nil {
		return nil, err
	}

	result := make([]domain.MonitoringInboxStat, 0, len(order))
	for _, inboxID := range order {
		result = append(result, statsMap[inboxID])
	}
	return result, nil
}

func buildMonitoringEventsWhere(q domain.MonitoringQuery) (string, []any) {
	where := []string{"observed_at >= ?", "observed_at <= ?"}
	args := []any{q.From.UTC().Format(time.RFC3339Nano), q.To.UTC().Format(time.RFC3339Nano)}

	if q.InboxID != "" {
		where = append(where, "inbox_id = ?")
		args = append(args, q.InboxID)
	}
	if q.Method != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToUpper(q.Method))
	}
	if q.StatusClass != "" {
		where = append(where, "status_class = ?")
		args = append(args, strings.ToLower(q.StatusClass))
	}
	if q.PathContains != "" {
		where = append(where, "LOWER(path) LIKE ?")
		args = append(args, "%"+strings.ToLower(q.PathContains)+"%")
	}
	if q.HasBody != nil {
		where = append(where, "has_body = ?")
		args = append(args, boolToInt(*q.HasBody))
	}
	if q.MinBodySize != nil {
		where = append(where, "body_size_bytes >= ?")
		args = append(args, *q.MinBodySize)
	}
	if q.MaxBodySize != nil {
		where = append(where, "body_size_bytes <= ?")
		args = append(args, *q.MaxBodySize)
	}
	if q.RemoteIP != "" {
		where = append(where, "remote_ip = ?")
		args = append(args, q.RemoteIP)
	}
	if q.Country != "" {
		where = append(where, "country = ?")
		args = append(args, strings.ToUpper(q.Country))
	}
	if q.TextQuery != "" {
		where = append(where, "text_blob LIKE ?")
		args = append(args, "%"+strings.ToLower(q.TextQuery)+"%")
	}

	return strings.Join(where, " AND "), args
}

func breakdownField(dimension string) string {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "method":
		return "method"
	case "status":
		return "status_class"
	case "path":
		return "path"
	case "ip":
		return "remote_ip"
	case "country":
		return "country"
	case "user_agent":
		return "user_agent"
	default:
		return ""
	}
}

func percentileFromBuckets(cumulative map[int64]int64, total int64, q float64) int64 {
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

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableEmptyString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
