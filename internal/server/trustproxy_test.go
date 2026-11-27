package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Known gap 10: the session cookie is Secure over HTTPS, and --trust-proxy
// decides whether X-Forwarded-Proto and X-Forwarded-For are believed.

// post sends a JSON POST with extra headers and returns the response.
func post(t *testing.T, client *http.Client, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// sessionCookie returns the Set-Cookie header for the session cookie.
func sessionCookie(resp *http.Response) string {
	for _, c := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessionCookieName+"=") {
			return c
		}
	}
	return ""
}

func TestSessionCookieSecure(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	const creds = `{"email":"admin@velo.dev","password":"password123"}`
	https := map[string]string{"X-Forwarded-Proto": "https"}

	for _, tc := range []struct {
		trust   bool
		headers map[string]string
		secure  bool
	}{
		{false, nil, false},
		{false, https, false}, // anyone can send the header; ignored without the flag
		{true, nil, false},
		{true, https, true},
		{true, map[string]string{"X-Forwarded-Proto": "http"}, false},
	} {
		srv.TrustProxy = tc.trust
		for _, path := range []string{"/api/auth/login", "/api/auth/logout"} {
			c := sessionCookie(post(t, http.DefaultClient, ts.URL+path, creds, tc.headers))
			if c == "" {
				t.Fatalf("%s set no session cookie", path)
			}
			if got := strings.Contains(c, "; Secure"); got != tc.secure {
				t.Errorf("trust=%v headers=%v %s: Secure=%v, want %v (%s)", tc.trust, tc.headers, path, got, tc.secure, c)
			}
		}
	}

	// Real TLS: always Secure, with or without the flag.
	tlsTS := httptest.NewTLSServer(srv)
	defer tlsTS.Close()
	for _, trust := range []bool{false, true} {
		srv.TrustProxy = trust
		if c := sessionCookie(post(t, tlsTS.Client(), tlsTS.URL+"/api/auth/login", creds, nil)); !strings.Contains(c, "; Secure") {
			t.Errorf("TLS, trust=%v: cookie not Secure: %s", trust, c)
		}
	}
}

func TestLoginThrottleBehindProxy(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	n := 0
	// fail makes one failed login for a fresh email, so only the IP key counts.
	fail := func(xff string) int {
		n++
		email := strings.Repeat("x", n) + "@velo.dev"
		return post(t, http.DefaultClient, ts.URL+"/api/auth/login", `{"email":"`+email+`","password":"wrong"}`,
			map[string]string{"X-Forwarded-For": xff}).StatusCode
	}

	// Without the flag the header is ignored: every client is the TCP peer,
	// and changing the header does not escape the lock.
	srv.TrustProxy = false
	for i := 0; i < 5; i++ {
		fail("203.0.113." + string(rune('1'+i)))
	}
	if c := fail("198.51.100.9"); c != http.StatusTooManyRequests {
		t.Fatalf("without --trust-proxy, a new X-Forwarded-For escaped the lock: %d", c)
	}

	// With the flag each client has its own counter, keyed by the address the
	// proxy appended (the last one); what the client put first is ignored.
	srv.logins = newLoginLimiter()
	srv.TrustProxy = true
	for i := 0; i < 5; i++ {
		if c := fail("9.9.9." + string(rune('1'+i)) + ", 203.0.113.7"); c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	if c := fail("1.1.1.1, 203.0.113.7"); c != http.StatusTooManyRequests {
		t.Fatalf("spoofed first address escaped the lock: %d", c)
	}
	if c := fail("203.0.113.8"); c != http.StatusUnauthorized {
		t.Fatalf("another client behind the proxy is locked too: %d", c)
	}
	// A missing or malformed header falls back to the TCP peer.
	for _, bad := range []string{"", "not-an-ip", "203.0.113.9, garbage"} {
		if c := fail(bad); c != http.StatusUnauthorized {
			t.Fatalf("X-Forwarded-For %q: %d", bad, c)
		}
	}
}
