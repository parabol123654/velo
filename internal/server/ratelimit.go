package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginLimiter slows down password guessing: after maxFails failed logins
// within period, a key (client IP or email) is locked until period has passed
// since its first failure. State is in memory only; a restart clears it.
type loginLimiter struct {
	mu       sync.Mutex
	fails    map[string]*failWindow
	maxFails int
	period   time.Duration
	now      func() time.Time
}

type failWindow struct {
	count int
	start time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		fails:    map[string]*failWindow{},
		maxFails: 5,
		period:   15 * time.Minute,
		now:      time.Now,
	}
}

// retryAfter returns how long the caller must wait, or 0 if none of keys is locked.
func (l *loginLimiter) retryAfter(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var wait time.Duration
	now := l.now()
	for _, k := range keys {
		w, ok := l.fails[k]
		if !ok {
			continue
		}
		end := w.start.Add(l.period)
		if !now.Before(end) {
			delete(l.fails, k)
			continue
		}
		if w.count >= l.maxFails && end.Sub(now) > wait {
			wait = end.Sub(now)
		}
	}
	return wait
}

func (l *loginLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.fails) > 10000 { // bound memory under a flood of distinct keys
		for k, w := range l.fails {
			if !now.Before(w.start.Add(l.period)) {
				delete(l.fails, k)
			}
		}
	}
	for _, k := range keys {
		w, ok := l.fails[k]
		if !ok || !now.Before(w.start.Add(l.period)) {
			w = &failWindow{start: now}
			l.fails[k] = w
		}
		w.count++
	}
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}

// loginKeys identifies a login attempt by client IP and by email. The IP is
// the TCP peer; behind a reverse proxy every client shares the proxy's IP.
func loginKeys(r *http.Request, email string) (ipKey, emailKey string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host, "email:" + strings.ToLower(strings.TrimSpace(email))
}
