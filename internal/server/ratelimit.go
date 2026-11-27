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
// the TCP peer, or with TrustProxy the last X-Forwarded-For address: the one
// the proxy itself appended. Earlier addresses come from the client and could
// be forged to escape the limit.
func (s *Server) loginKeys(r *http.Request, email string) (ipKey, emailKey string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.TrustProxy {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if ip := parseForwardedIP(parts[len(parts)-1]); ip != nil {
				host = ip.String()
			}
		}
	}
	return "ip:" + host, "email:" + strings.ToLower(strings.TrimSpace(email))
}

// parseForwardedIP reads one X-Forwarded-For entry: a bare IP, or one with a
// port ("1.2.3.4:5678", "[2001:db8::1]:443") as some proxies send, or an IPv6
// zone. It returns nil when the entry is not an address.
func parseForwardedIP(entry string) net.IP {
	entry = strings.TrimSpace(entry)
	if h, _, err := net.SplitHostPort(entry); err == nil {
		entry = h
	}
	entry = strings.TrimSuffix(strings.TrimPrefix(entry, "["), "]")
	if i := strings.IndexByte(entry, '%'); i >= 0 {
		entry = entry[:i]
	}
	return net.ParseIP(entry)
}
