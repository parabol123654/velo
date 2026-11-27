package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// QA for --trust-proxy (Known gap 10): every endpoint that sets the session
// cookie, odd X-Forwarded-Proto / X-Forwarded-For values, and the throttle's
// email counter with the flag on.

const qaAdminSetup = `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`
const qaAdminCreds = `{"email":"admin@velo.dev","password":"password123"}`

// qaPost sends a JSON POST; each header value in lines becomes its own header
// line (Header.Add), so repeated headers can be tested.
func qaPost(t *testing.T, client *http.Client, url, body string, lines map[string][]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	for k, vs := range lines {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func qaSecure(c string) bool { return strings.Contains(c, "; Secure") }

// qaInvite creates a member invite as admin and returns its code.
func qaInvite(t *testing.T, ts *httptest.Server, admin string) string {
	t.Helper()
	_, b := call(t, "POST", ts.URL+"/api/invites", admin, `{"role":"member"}`)
	var inv db.Invite
	if err := json.Unmarshal([]byte(b), &inv); err != nil || inv.Code == "" {
		t.Fatalf("create invite: %v %s", err, b)
	}
	return inv.Code
}

// checkCookieAttrs asserts the attributes every session cookie must carry.
func checkCookieAttrs(t *testing.T, what, c string, secure bool) {
	t.Helper()
	if c == "" {
		t.Fatalf("%s: no session cookie", what)
	}
	if got := qaSecure(c); got != secure {
		t.Errorf("%s: Secure=%v, want %v (%s)", what, got, secure, c)
	}
	if !strings.Contains(c, "HttpOnly") {
		t.Errorf("%s: not HttpOnly: %s", what, c)
	}
	if !strings.Contains(c, "SameSite=Lax") {
		t.Errorf("%s: not SameSite=Lax: %s", what, c)
	}
	if !strings.Contains(c, "Path=/") {
		t.Errorf("%s: no Path=/: %s", what, c)
	}
}

// Every cookie-setting endpoint (setup, login, accept-invite, logout) follows
// the same rule, with and without the flag, over plain HTTP.
func TestQATrustProxyAllCookieEndpoints(t *testing.T) {
	https := map[string][]string{"X-Forwarded-Proto": {"https"}}
	for _, tc := range []struct {
		trust   bool
		headers map[string][]string
		secure  bool
	}{
		{false, nil, false},
		{false, https, false},
		{true, nil, false},
		{true, https, true},
	} {
		name := fmt.Sprintf("trust=%v,xfp=%v", tc.trust, tc.headers != nil)
		t.Run(name, func(t *testing.T) {
			ts, srv := newTestServer(t)
			srv.TrustProxy = tc.trust

			resp := qaPost(t, http.DefaultClient, ts.URL+"/api/auth/setup", qaAdminSetup, tc.headers)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("setup: %d", resp.StatusCode)
			}
			checkCookieAttrs(t, "setup", sessionCookie(resp), tc.secure)

			_, b := call(t, "POST", ts.URL+"/api/auth/login", "", qaAdminCreds)
			admin := sessionOf(t, b)

			resp = qaPost(t, http.DefaultClient, ts.URL+"/api/auth/login", qaAdminCreds, tc.headers)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("login: %d", resp.StatusCode)
			}
			checkCookieAttrs(t, "login", sessionCookie(resp), tc.secure)

			code := qaInvite(t, ts, admin)
			resp = qaPost(t, http.DefaultClient, ts.URL+"/api/auth/accept-invite",
				`{"code":"`+code+`","email":"inv@velo.dev","name":"Inv","password":"password123"}`, tc.headers)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("accept-invite: %d", resp.StatusCode)
			}
			checkCookieAttrs(t, "accept-invite", sessionCookie(resp), tc.secure)

			resp = qaPost(t, http.DefaultClient, ts.URL+"/api/auth/logout", "", tc.headers)
			c := sessionCookie(resp)
			checkCookieAttrs(t, "logout", c, tc.secure)
			if !strings.Contains(c, "Max-Age=0") {
				t.Errorf("logout does not expire the cookie: %s", c)
			}
		})
	}
}

// Failed requests must not set a cookie, whatever the headers say.
func TestQATrustProxyNoCookieOnFailure(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.TrustProxy = true
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	https := map[string][]string{"X-Forwarded-Proto": {"https"}}
	for path, body := range map[string]string{
		"/api/auth/login":         `{"email":"admin@velo.dev","password":"wrong"}`,
		"/api/auth/setup":         qaAdminSetup, // already set up
		"/api/auth/accept-invite": `{"code":"nope","email":"x@velo.dev","name":"X","password":"password123"}`,
	} {
		resp := qaPost(t, http.DefaultClient, ts.URL+path, body, https)
		if resp.StatusCode < 400 {
			t.Errorf("%s: expected an error, got %d", path, resp.StatusCode)
		}
		if c := sessionCookie(resp); c != "" {
			t.Errorf("%s: failed request set a cookie: %s", path, c)
		}
	}
}

// Odd X-Forwarded-Proto values with the flag on.
func TestQATrustProxyForwardedProtoVariants(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	for _, tc := range []struct {
		lines  []string
		secure bool
	}{
		{[]string{"HTTPS"}, true},
		{[]string{"Https"}, true},
		{[]string{" https "}, true},
		{[]string{"\thttps"}, true},
		{[]string{"https", "https"}, true},
		{[]string{""}, false},
		{[]string{"http"}, false},
		{[]string{"HTTP"}, false},
		{[]string{"httpss"}, false},
		{[]string{"wss"}, false},
		{[]string{"ftp"}, false},
		{[]string{"https;"}, false},
		{[]string{strings.Repeat("https", 2000)}, false},
		// Comma lists come from proxy chains, which the spec does not
		// support (one proxy). The safe answer is "not https"; Secure on a
		// plain-HTTP origin would make the browser drop the cookie.
		{[]string{"https, http"}, false},
		{[]string{"http, https"}, false},
		{[]string{"https,https"}, false},
	} {
		for _, trust := range []bool{false, true} {
			srv.TrustProxy = trust
			want := tc.secure && trust
			for _, path := range []string{"/api/auth/login", "/api/auth/logout"} {
				resp := qaPost(t, http.DefaultClient, ts.URL+path, qaAdminCreds,
					map[string][]string{"X-Forwarded-Proto": tc.lines})
				c := sessionCookie(resp)
				if c == "" {
					t.Fatalf("%s %q: no cookie (status %d)", path, tc.lines, resp.StatusCode)
				}
				if got := qaSecure(c); got != want {
					t.Errorf("trust=%v X-Forwarded-Proto %q %s: Secure=%v, want %v", trust, tc.lines, path, got, want)
				}
			}
		}
	}
}

// Repeated X-Forwarded-Proto lines: the last line is the one the trusted
// proxy added, as with X-Forwarded-For.
func TestQATrustProxyRepeatedProtoLines(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	for _, tc := range []struct {
		lines  []string
		secure bool
	}{
		{[]string{"http", "https"}, true},
		{[]string{"https", "http"}, false}, // client claims https, proxy says http
		{[]string{"https", ""}, false},
		{[]string{"https", "https, http"}, false},
		{[]string{"http", " HTTPS "}, true},
	} {
		for _, trust := range []bool{false, true} {
			srv.TrustProxy = trust
			for _, path := range []string{"/api/auth/login", "/api/auth/logout"} {
				c := sessionCookie(qaPost(t, http.DefaultClient, ts.URL+path, qaAdminCreds,
					map[string][]string{"X-Forwarded-Proto": tc.lines}))
				if got, want := qaSecure(c), tc.secure && trust; got != want {
					t.Errorf("trust=%v lines %q %s: Secure=%v, want %v", trust, tc.lines, path, got, want)
				}
			}
		}
	}
}

// Real TLS: every endpoint is Secure, with or without the flag, and the
// header cannot downgrade it.
func TestQATrustProxyRealTLS(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(fmt.Sprintf("trust=%v", trust), func(t *testing.T) {
			_, srv := newTestServer(t)
			srv.TrustProxy = trust
			tlsTS := httptest.NewTLSServer(srv)
			defer tlsTS.Close()
			client := tlsTS.Client()
			down := map[string][]string{"X-Forwarded-Proto": {"http"}}

			resp := qaPost(t, client, tlsTS.URL+"/api/auth/setup", qaAdminSetup, down)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("setup: %d", resp.StatusCode)
			}
			checkCookieAttrs(t, "setup", sessionCookie(resp), true)

			resp = qaPost(t, client, tlsTS.URL+"/api/auth/login", qaAdminCreds, down)
			checkCookieAttrs(t, "login", sessionCookie(resp), true)

			// Get an admin bearer token over TLS for the invite.
			req, _ := http.NewRequest("POST", tlsTS.URL+"/api/auth/login", strings.NewReader(qaAdminCreds))
			r2, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				SessionToken string `json:"session_token"`
			}
			_ = json.NewDecoder(r2.Body).Decode(&v)
			r2.Body.Close()
			req, _ = http.NewRequest("POST", tlsTS.URL+"/api/invites", strings.NewReader(`{"role":"member"}`))
			req.Header.Set("Authorization", "Bearer "+v.SessionToken)
			r3, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var inv db.Invite
			_ = json.NewDecoder(r3.Body).Decode(&inv)
			r3.Body.Close()
			if inv.Code == "" {
				t.Fatalf("no invite code (status %d)", r3.StatusCode)
			}

			resp = qaPost(t, client, tlsTS.URL+"/api/auth/accept-invite",
				`{"code":"`+inv.Code+`","email":"inv@velo.dev","name":"Inv","password":"password123"}`, down)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("accept-invite: %d", resp.StatusCode)
			}
			checkCookieAttrs(t, "accept-invite", sessionCookie(resp), true)

			resp = qaPost(t, client, tlsTS.URL+"/api/auth/logout", "", down)
			checkCookieAttrs(t, "logout", sessionCookie(resp), true)
		})
	}
}

// qaThrottle drives failed logins against one server.
type qaThrottle struct {
	t  *testing.T
	ts *httptest.Server
	n  int
}

// failIP makes one failed login for a fresh email, so only the IP key counts.
func (q *qaThrottle) failIP(xff ...string) int {
	q.n++
	email := fmt.Sprintf("qa%d@velo.dev", q.n)
	var h map[string][]string
	if len(xff) > 0 {
		h = map[string][]string{"X-Forwarded-For": xff}
	}
	return qaPost(q.t, http.DefaultClient, q.ts.URL+"/api/auth/login",
		`{"email":"`+email+`","password":"wrong"}`, h).StatusCode
}

// lock makes 5 failed logins with the given header lines and checks the 6th is 429.
func (q *qaThrottle) lock(xff ...string) {
	q.t.Helper()
	for i := 0; i < 5; i++ {
		if c := q.failIP(xff...); c != http.StatusUnauthorized {
			q.t.Fatalf("lock %q attempt %d: %d", xff, i+1, c)
		}
	}
	if c := q.failIP(xff...); c != http.StatusTooManyRequests {
		q.t.Fatalf("lock %q: 6th attempt %d, want 429", xff, c)
	}
}

func newQAThrottle(t *testing.T, trust bool) (*qaThrottle, *Server) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	srv.TrustProxy = trust
	return &qaThrottle{t: t, ts: ts}, srv
}

// Several X-Forwarded-For header lines: the last address of the last line is
// the one the proxy appended.
func TestQAForwardedForRepeatedLines(t *testing.T) {
	q, _ := newQAThrottle(t, true)
	q.lock("203.0.113.7")
	if c := q.failIP("9.9.9.9", "203.0.113.7"); c != http.StatusTooManyRequests {
		t.Errorf("client line + proxy line: %d, want 429", c)
	}
	if c := q.failIP("9.9.9.9, 8.8.8.8", "1.1.1.1, 203.0.113.7"); c != http.StatusTooManyRequests {
		t.Errorf("two lines with lists: %d, want 429", c)
	}
	if c := q.failIP("203.0.113.7", "9.9.9.9"); c != http.StatusUnauthorized {
		t.Errorf("locked address in an earlier line counted: %d, want 401", c)
	}
}

// IPv6 and IPv4-mapped addresses are normalized, so spelling variants share
// one counter.
func TestQAForwardedForIPv6(t *testing.T) {
	q, _ := newQAThrottle(t, true)
	q.lock("2001:db8::1")
	for _, same := range []string{"2001:DB8::1", "2001:db8:0:0:0:0:0:1", "9.9.9.9, 2001:db8::1", " 2001:db8::1 "} {
		if c := q.failIP(same); c != http.StatusTooManyRequests {
			t.Errorf("%q: %d, want 429 (same client)", same, c)
		}
	}
	if c := q.failIP("2001:db8::2"); c != http.StatusUnauthorized {
		t.Errorf("other IPv6 client locked: %d", c)
	}

	q.lock("203.0.113.50")
	if c := q.failIP("::ffff:203.0.113.50"); c != http.StatusTooManyRequests {
		t.Errorf("IPv4-mapped form escaped the lock: %d", c)
	}
}

// Entries with a port, brackets or an IPv6 zone are read as the address
// they contain, so they share that address's counter.
func TestQAForwardedForPortAndZone(t *testing.T) {
	q, _ := newQAThrottle(t, true)
	q.lock("2001:db8::1")
	for _, v := range []string{"[2001:db8::1]:443", "[2001:db8::1]", " [2001:DB8::1]:1 ", "9.9.9.9, [2001:db8::1]:443"} {
		if c := q.failIP(v); c != http.StatusTooManyRequests {
			t.Errorf("%q: %d, want 429 (same client as 2001:db8::1)", v, c)
		}
	}
	q.lock("203.0.113.7")
	for _, v := range []string{"203.0.113.7:8080", "203.0.113.7:0", "[203.0.113.7]:1"} {
		if c := q.failIP(v); c != http.StatusTooManyRequests {
			t.Errorf("%q: %d, want 429 (same client as 203.0.113.7)", v, c)
		}
	}
	q.lock("fe80::1")
	if c := q.failIP("fe80::1%eth0"); c != http.StatusTooManyRequests {
		t.Errorf("zone form escaped the lock: %d", c)
	}
	if c := q.failIP("203.0.113.8:8080"); c != http.StatusUnauthorized {
		t.Errorf("another client with a port is locked: %d", c)
	}
}

// Values that are not an address (trailing comma, empty last entry, garbage)
// fall back to the TCP peer, as SPEC says, and share its counter.
func TestQAForwardedForFallbackToPeer(t *testing.T) {
	invalid := []string{
		"203.0.113.7,",
		"203.0.113.7, ",
		"203.0.113.7, , ",
		",",
		"unknown",
		"_hidden",
		"203.0.113.300",
		"203.0.113.7:99999:1",
		"[]",
		"[]:80",
		"%eth0",
		strings.Repeat("a", 4000),
	}
	q, _ := newQAThrottle(t, true)
	q.lock() // no header: locks the TCP peer 127.0.0.1
	for _, v := range invalid {
		if c := q.failIP(v); c != http.StatusTooManyRequests {
			t.Errorf("%q did not fall back to the locked TCP peer: %d", v, c)
		}
	}
	// Valid last entries are still independent of the locked peer.
	for _, v := range []string{", 203.0.113.60", ",,203.0.113.61", "garbage, 203.0.113.62", "2001:db8::9"} {
		if c := q.failIP(v); c != http.StatusUnauthorized {
			t.Errorf("%q: %d, want 401 (own counter)", v, c)
		}
	}
	// An X-Forwarded-For naming the peer itself shares the peer's counter.
	if c := q.failIP("127.0.0.1"); c != http.StatusTooManyRequests {
		t.Errorf("XFF=127.0.0.1: %d, want 429", c)
	}
}

// A long header (thousands of client-supplied entries) still keys on the last one.
func TestQAForwardedForLongHeader(t *testing.T) {
	q, _ := newQAThrottle(t, true)
	parts := make([]string, 0, 5001)
	for i := 0; i < 5000; i++ {
		parts = append(parts, fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256))
	}
	long := strings.Join(append(parts, "203.0.113.70"), ", ")
	q.lock(long)
	if c := q.failIP("203.0.113.70"); c != http.StatusTooManyRequests {
		t.Errorf("long header was not keyed by its last address: %d", c)
	}
	if c := q.failIP(strings.Join(append(parts, "203.0.113.71"), ", ")); c != http.StatusUnauthorized {
		t.Errorf("different last address locked: %d", c)
	}
}

// Without the flag nothing in X-Forwarded-For matters, including IPv6 and
// multi-line values: all requests share the TCP peer's counter.
func TestQAForwardedForIgnoredWithoutFlag(t *testing.T) {
	q, _ := newQAThrottle(t, false)
	vals := [][]string{{"2001:db8::1"}, {"203.0.113.1"}, {"1.1.1.1", "203.0.113.2"}, {"203.0.113.3,"}, nil}
	for i := 0; i < 5; i++ {
		if c := q.failIP(vals[i]...); c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	for _, v := range [][]string{{"198.51.100.1"}, {"2001:db8::42"}, nil} {
		if c := q.failIP(v...); c != http.StatusTooManyRequests {
			t.Errorf("without the flag %q escaped the lock: %d", v, c)
		}
	}
}

// With the flag, rotating X-Forwarded-For does not escape the email counter,
// and the lock also blocks the correct password.
func TestQAEmailCounterWithFlag(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	srv.TrustProxy = true
	bad := `{"email":"admin@velo.dev","password":"wrong"}`
	for i := 0; i < 5; i++ {
		xff := map[string][]string{"X-Forwarded-For": {fmt.Sprintf("198.51.100.%d", i+1)}}
		if c := qaPost(t, http.DefaultClient, ts.URL+"/api/auth/login", bad, xff).StatusCode; c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	for _, email := range []string{"admin@velo.dev", "ADMIN@velo.dev", "  admin@velo.dev "} {
		xff := map[string][]string{"X-Forwarded-For": {"198.51.100.99"}}
		resp := qaPost(t, http.DefaultClient, ts.URL+"/api/auth/login",
			`{"email":"`+email+`","password":"password123"}`, xff)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("email %q from a new IP: %d, want 429", email, resp.StatusCode)
		}
		if resp.Header.Get("Retry-After") == "" {
			t.Errorf("email %q: no Retry-After", email)
		}
		if c := sessionCookie(resp); c != "" {
			t.Errorf("429 set a cookie: %s", c)
		}
	}
}

// With the flag, a successful login clears the email counter but not the
// client's IP counter.
func TestQASuccessClearsEmailCounterWithFlag(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", qaAdminSetup)
	srv.TrustProxy = true
	bad := `{"email":"admin@velo.dev","password":"wrong"}`
	login := func(body, ip string) int {
		return qaPost(t, http.DefaultClient, ts.URL+"/api/auth/login", body,
			map[string][]string{"X-Forwarded-For": {ip}}).StatusCode
	}
	ipN := 0
	fresh := func() string { ipN++; return fmt.Sprintf("198.51.100.%d", ipN) }

	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if c := login(bad, fresh()); c != http.StatusUnauthorized {
				t.Fatalf("round %d attempt %d: %d", round, i+1, c)
			}
		}
		if c := login(qaAdminCreds, fresh()); c != http.StatusOK {
			t.Fatalf("round %d: correct login %d, want 200 (email counter not cleared?)", round, c)
		}
	}

	// IP counter: 4 failures from one client, a success, one more failure
	// reaches 5 and locks that client.
	q := &qaThrottle{t: t, ts: ts}
	for i := 0; i < 4; i++ {
		q.failIP("203.0.113.90")
	}
	if c := login(qaAdminCreds, "203.0.113.90"); c != http.StatusOK {
		t.Fatalf("success from client: %d", c)
	}
	q.failIP("203.0.113.90")
	if c := login(qaAdminCreds, "203.0.113.90"); c != http.StatusTooManyRequests {
		t.Errorf("success cleared the IP counter: %d, want 429", c)
	}
	if c := login(qaAdminCreds, "203.0.113.91"); c != http.StatusOK {
		t.Errorf("another client is locked: %d", c)
	}
}
