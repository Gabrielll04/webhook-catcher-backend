package domain

import "time"

type InboxStatus string

const (
	InboxStatusActive   InboxStatus = "active"
	InboxStatusDisabled InboxStatus = "disabled"
)

type Inbox struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Token         string      `json:"token"`
	Status        InboxStatus `json:"status"`
	Description   *string     `json:"description,omitempty"`
	RetentionDays *int        `json:"retention_days,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	DisabledAt    *time.Time  `json:"disabled_at,omitempty"`
	DeletedAt     *time.Time  `json:"deleted_at,omitempty"`
}

type CapturedRequest struct {
	ID             string            `json:"id"`
	InboxID        string            `json:"inbox_id"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	QueryParams    map[string]string `json:"query_params_json"`
	Headers        map[string]string `json:"headers_json"`
	BodyRaw        []byte            `json:"body_raw"`
	BodySizeBytes  int64             `json:"body_size_bytes"`
	ContentType    string            `json:"content_type"`
	RemoteIP       string            `json:"remote_ip"`
	UserAgent      string            `json:"user_agent"`
	ReceivedAt     time.Time         `json:"received_at"`
	RequestHash    string            `json:"request_hash,omitempty"`
	TruncatedBody  bool              `json:"truncated_body"`
	RejectedReason *string           `json:"rejected_reason,omitempty"`
}

type CapturedRequestSummary struct {
	ID            string    `json:"id"`
	Method        string    `json:"method"`
	ContentType   string    `json:"content_type"`
	BodySizeBytes int64     `json:"body_size_bytes"`
	RemoteIP      string    `json:"remote_ip"`
	ReceivedAt    time.Time `json:"received_at"`
}

type InboxPatch struct {
	Name          *string
	Description   *string
	Status        *InboxStatus
	RetentionDays *int
}

type ListFilter struct {
	Method      string
	ContentType string
	HasBody     *bool
	From        *time.Time
	To          *time.Time
	Order       string
}

type Pagination struct {
	Page     int
	PageSize int
}
