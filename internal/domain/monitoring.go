package domain

import "time"

var LatencyHistogramBoundsMs = []int64{10, 25, 50, 100, 250, 500, 1000, 2000, 5000}

type MonitoringQuery struct {
	From         time.Time
	To           time.Time
	InboxID      string
	Method       string
	StatusClass  string
	PathContains string
	HasBody      *bool
	MinBodySize  *int64
	MaxBodySize  *int64
	RemoteIP     string
	Country      string
	TextQuery    string
}

type MonitoringSummary struct {
	RequestsTotal      int64            `json:"requests_total"`
	ErrorsTotal        int64            `json:"errors_total"`
	ErrorRate          float64          `json:"error_rate"`
	LatencyAvgMs       float64          `json:"latency_avg_ms"`
	LatencyP95Ms       int64            `json:"latency_p95_ms"`
	LatencyP99Ms       int64            `json:"latency_p99_ms"`
	RequestsPerMinute  float64          `json:"requests_per_minute"`
	RequestsLastMinute int64            `json:"requests_last_minute"`
	StatusCodes        map[string]int64 `json:"status_codes"`
}

type MonitoringTimeseriesPoint struct {
	TS       time.Time `json:"ts"`
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
	LatAvgMs float64   `json:"lat_avg_ms"`
	P95Ms    int64     `json:"p95_ms"`
	P99Ms    int64     `json:"p99_ms"`
}

type MonitoringBreakdownItem struct {
	Dimension  string  `json:"dimension"`
	Value      string  `json:"value"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

type MonitoringInboxStat struct {
	InboxID      string          `json:"inbox_id"`
	InboxName    string          `json:"inbox_name"`
	Requests     int64           `json:"requests"`
	Errors       int64           `json:"errors"`
	LatencySumMs int64           `json:"-"`
	Buckets      map[int64]int64 `json:"-"`
}

type MonitoringInboxHealth struct {
	InboxID      string  `json:"inbox_id"`
	InboxName    string  `json:"inbox_name"`
	State        string  `json:"state"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	ErrorRate    float64 `json:"error_rate"`
	LatencyP95Ms int64   `json:"latency_p95_ms"`
}

type HookObservation struct {
	ObservedAt    time.Time
	BucketMinute  time.Time
	InboxID       string
	Method        string
	Path          string
	StatusCode    int
	StatusClass   string
	LatencyMs     int64
	BodySizeBytes int64
	HasBody       bool
	RemoteIP      string
	UserAgent     string
	Country       string
	ContentType   string
	TextBlob      string
}

type MonitoringLiveEvent struct {
	ObservedAt    time.Time `json:"observed_at"`
	InboxID       string    `json:"inbox_id,omitempty"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	StatusCode    int       `json:"status_code"`
	StatusClass   string    `json:"status_class"`
	LatencyMs     int64     `json:"latency_ms"`
	BodySizeBytes int64     `json:"body_size_bytes"`
	RemoteIP      string    `json:"remote_ip"`
	Country       string    `json:"country"`
	UserAgent     string    `json:"user_agent"`
}
