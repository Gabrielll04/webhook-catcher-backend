package metrics

import "context"

type Recorder interface {
	CaptureRecorded(ctx context.Context, inboxID string, size int64)
	RouteLatency(ctx context.Context, route string, status int, latencyMs int64)
	PersistFailure(ctx context.Context, route string)
	SetActiveInboxes(ctx context.Context, total int)
}
