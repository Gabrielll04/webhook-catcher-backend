package httpapi

import "sync"

type sseBroker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan []byte]struct{}
}

func newSSEBroker() *sseBroker {
	return &sseBroker{subscribers: make(map[string]map[chan []byte]struct{})}
}

func (b *sseBroker) Subscribe(inboxID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	if _, ok := b.subscribers[inboxID]; !ok {
		b.subscribers[inboxID] = make(map[chan []byte]struct{})
	}
	b.subscribers[inboxID][ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if group, ok := b.subscribers[inboxID]; ok {
			if _, exists := group[ch]; exists {
				delete(group, ch)
				close(ch)
			}
			if len(group) == 0 {
				delete(b.subscribers, inboxID)
			}
		}
		b.mu.Unlock()
	}

	return ch, cancel
}

func (b *sseBroker) Publish(inboxID string, data []byte) {
	b.mu.RLock()
	group, ok := b.subscribers[inboxID]
	if !ok || len(group) == 0 {
		b.mu.RUnlock()
		return
	}
	channels := make([]chan []byte, 0, len(group))
	for ch := range group {
		channels = append(channels, ch)
	}
	b.mu.RUnlock()

	for _, ch := range channels {
		payload := append([]byte(nil), data...)
		select {
		case ch <- payload:
		default:
		}
	}
}
