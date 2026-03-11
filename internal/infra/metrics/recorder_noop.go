package metrics

func NewRecorder() Recorder {
	return NoopRecorder{}
}
