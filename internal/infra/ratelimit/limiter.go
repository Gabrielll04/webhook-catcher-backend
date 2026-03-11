package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	entries  map[string]*entry
	limitRPM int
}

type entry struct {
	windowStart time.Time
	count       int
}

func New(limitRPM int) *Limiter {
	if limitRPM <= 0 {
		limitRPM = 120
	}
	return &Limiter{
		entries:  make(map[string]*entry),
		limitRPM: limitRPM,
	}
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok || now.Sub(e.windowStart) >= time.Minute {
		l.entries[key] = &entry{windowStart: now, count: 1}
		return true
	}

	if e.count >= l.limitRPM {
		return false
	}
	e.count++
	return true
}
