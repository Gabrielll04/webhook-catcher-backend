package metrics

import "context"

type NoopRecorder struct{}

func (NoopRecorder) CaptureRecorded(context.Context, string, int64)   {}
func (NoopRecorder) RouteLatency(context.Context, string, int, int64) {}
func (NoopRecorder) PersistFailure(context.Context, string)           {}
func (NoopRecorder) SetActiveInboxes(context.Context, int)            {}
