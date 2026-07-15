package httpapi

import (
	"strconv"
	"sync"
	"time"
)

type limitResult struct {
	allowed          bool
	remaining, reset int
}
type Limiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	requests map[string][]time.Time
	now      func() time.Time
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, requests: make(map[string][]time.Time), now: time.Now}
}

func (l *Limiter) allow(key string) limitResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	times := l.requests[key]
	first := 0
	for first < len(times) && !times[first].After(cutoff) {
		first++
	}
	times = append([]time.Time(nil), times[first:]...)
	if len(times) >= l.limit {
		reset := int(times[0].Add(l.window).Sub(now).Seconds()) + 1
		if reset < 1 {
			reset = 1
		}
		l.requests[key] = times
		return limitResult{allowed: false, remaining: 0, reset: reset}
	}
	times = append(times, now)
	l.requests[key] = times
	return limitResult{allowed: true, remaining: l.limit - len(times), reset: int(l.window.Seconds())}
}

func (l *Limiter) headers(set func(string, string), result limitResult) {
	set("X-RateLimit-Limit", strconv.Itoa(l.limit))
	set("X-RateLimit-Remaining", strconv.Itoa(result.remaining))
	set("X-RateLimit-Reset", strconv.Itoa(result.reset))
}
