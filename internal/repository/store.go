package repository

import (
	"context"
	"time"

	"webhook-catcher/internal/domain"
)

type Store interface {
	CreateInbox(ctx context.Context, inbox *domain.Inbox) error
	ListInboxes(ctx context.Context, page domain.Pagination) ([]domain.Inbox, int, error)
	GetInboxByID(ctx context.Context, id string) (*domain.Inbox, error)
	GetInboxByToken(ctx context.Context, token string) (*domain.Inbox, error)
	UpdateInbox(ctx context.Context, id string, patch domain.InboxPatch, now time.Time) (*domain.Inbox, error)
	SoftDeleteInbox(ctx context.Context, id string, now time.Time) error

	CreateCapturedRequest(ctx context.Context, req *domain.CapturedRequest) error
	ListCapturedRequests(ctx context.Context, inboxID string, filter domain.ListFilter, page domain.Pagination) ([]domain.CapturedRequestSummary, int, error)
	GetCapturedRequestByID(ctx context.Context, inboxID, requestID string) (*domain.CapturedRequest, error)

	RecordHookObservation(ctx context.Context, obs *domain.HookObservation) error
	GetMonitoringSummary(ctx context.Context, q domain.MonitoringQuery) (*domain.MonitoringSummary, error)
	GetMonitoringTimeseries(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringTimeseriesPoint, error)
	GetMonitoringBreakdown(ctx context.Context, q domain.MonitoringQuery, dimension string, limit int) ([]domain.MonitoringBreakdownItem, error)
	ListMonitoringInboxStats(ctx context.Context, q domain.MonitoringQuery) ([]domain.MonitoringInboxStat, error)
	ListMonitoringLiveEvents(ctx context.Context, q domain.MonitoringQuery, limit int) ([]domain.MonitoringLiveEvent, error)

	DeleteExpiredCapturedRequests(ctx context.Context, now time.Time, defaultRetentionDays int) (int64, error)
	CountActiveInboxes(ctx context.Context) (int, error)
	Ping(ctx context.Context) error
}
