package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

type qaQueryEnv struct {
	url              string
	admin, bob       string // sessions
	adminTok, bobTok string // full tokens
	readTok, devTok  string // read-only token, DEV-only token (admin's)
	adminID, bobID   string
}

// newQAQueryEnv: DEV-1 admin bug p4 Todo; DEV-2 bob bug p2 In Progress (child of DEV-1);
// DEV-3 unassigned Done p3; DEV-4 blocked by DEV-2; OPS-1 admin bug p4 "secret login".
func newQAQueryEnv(t *testing.T) *qaQueryEnv {
	t.Helper()
	ts, srv := newTestServer(t)
	e := &qaQueryEnv{url: ts.URL}
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	e.admin = sessionOf(t, body)
	st := srv.store
	bob, err := st.CreateUserDirect("bob@velo.dev", "Bob", "password123", "member")
	if err != nil {
		t.Fatal(err)
	}
	e.bobID = bob.ID
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"bob@velo.dev","password":"password123"}`)
	e.bob = sessionOf(t, body)
	users, _ := st.ListUsers()
	for _, u := range users {
		if u.Email == "admin@velo.dev" {
			e.adminID = u.ID
		}
	}
	for _, k := range []string{"DEV", "OPS"} {
		if _, err := st.CreateProject(k, k, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []db.CreateIssueInput{
		{ProjectRef: "DEV", Title: "đăng nhập lỗi", StateID: "Todo", Priority: 4, AssigneeID: e.adminID, LabelIDs: []string{"bug"}},
		{ProjectRef: "DEV", Title: "login slow", StateID: "In Progress", Priority: 2, AssigneeID: bob.ID, LabelIDs: []string{"bug"}, ParentRef: "DEV-1"},
		{ProjectRef: "DEV", Title: "docs", StateID: "Done", Priority: 3},
		{ProjectRef: "DEV", Title: "waiting", StateID: "Todo", Priority: 1},
		{ProjectRef: "OPS", Title: "secret login", StateID: "Todo", Priority: 4, AssigneeID: e.adminID, LabelIDs: []string{"bug"}},
	} {
		if _, err := st.CreateIssue(e.adminID, in); err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
	}
	if _, _, err := st.LinkIssues(e.adminID, "DEV-4", "blocked_by", "DEV-2"); err != nil {
		t.Fatal(err)
	}
	tok := func(session, scope string) string {
		t.Helper()
		c, b := call(t, "POST", ts.URL+"/api/tokens", session, scope)
		if c != http.StatusCreated {
			t.Fatalf("token %s: %d %s", scope, c, b)
		}
		var v struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(b), &v)
		return v.Token
	}
	e.adminTok = tok(e.admin, `{"name":"full"}`)
	e.bobTok = tok(e.bob, `{"name":"bob"}`)
	e.readTok = tok(e.admin, `{"name":"reader","access":"read"}`)
	e.devTok = tok(e.admin, `{"name":"dev","projects":["DEV"]}`)
	return e
}

// get returns the status and, on 200, the sorted identifiers.
func (e *qaQueryEnv) get(t *testing.T, path, q, auth string) (int, string, string) {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	c, b := call(t, "GET", e.url+path+sep+"q="+url.QueryEscape(q), auth, "")
	if c != http.StatusOK {
		return c, "", b
	}
	var issues []db.Issue
	if err := json.Unmarshal([]byte(b), &issues); err != nil {
		t.Fatalf("%s %q: %v %s", path, q, err, b)
	}
	var ids []string
	for _, it := range issues {
		ids = append(ids, it.Identifier)
	}
	sort.Strings(ids)
	return c, strings.Join(ids, ","), b
}

// QA: results on both endpoints, for session and token callers.
func TestQAQueryHTTPResults(t *testing.T) {
	e := newQAQueryEnv(t)
	for _, tc := range []struct{ path, q, auth, want string }{
		{"/api/issues", "assignee:me", e.admin, "DEV-1,OPS-1"},
		{"/api/issues", "assignee:me", e.adminTok, "DEV-1,OPS-1"},
		{"/api/issues", "assignee:me", e.bob, "DEV-2"},
		{"/api/issues", "assignee:me", e.bobTok, "DEV-2"},
		{"/api/search", "assignee:me", e.bob, "DEV-2"},
		{"/api/search", "ASSIGNEE:Me", e.bobTok, "DEV-2"},
		{"/api/search", "assignee:me", e.readTok, "DEV-1,OPS-1"},
		{"/api/issues", "is:open label:bug project:DEV", e.readTok, "DEV-1,DEV-2"},
		{"/api/issues", "is:blocked", e.admin, "DEV-4"},
		{"/api/issues", "has:parent", e.admin, "DEV-2"},
		{"/api/issues?project=DEV", "no:parent is:open", e.admin, "DEV-1,DEV-4"},
		{"/api/issues?label=bug", "is:open project:DEV", e.admin, "DEV-1,DEV-2"},
		{"/api/issues?category=completed", "is:open", e.admin, ""},
		{"/api/issues?assignee=me", "label:bug", e.bob, "DEV-2"},
		{"/api/search", "dang nhap is:open", e.admin, "DEV-1"},
		{"/api/search", "DEV-1 priority:>=4", e.admin, "DEV-1"},
		{"/api/search", "dev-2 has:parent", e.admin, "DEV-2"},
		{"/api/search", "DEV-2 no:parent", e.admin, ""},
		{"/api/search", "login priority:<high", e.admin, "DEV-2"},
		{"/api/search", "", e.admin, ""},
		{"/api/search", "lỗi:đăng is:open", e.admin, ""},
		{"/api/issues", "ñ:x", e.admin, ""},
		{"/api/search", " \t\n", e.admin, ""},
		// project-restricted token: never another project's issue
		{"/api/issues", "project:OPS", e.devTok, ""},
		{"/api/search", "project:OPS", e.devTok, ""},
		{"/api/search", "project:ops login", e.devTok, ""},
		{"/api/search", "OPS-1", e.devTok, ""},
		{"/api/search", "secret", e.devTok, ""},
		{"/api/search", "assignee:me priority:>=4", e.devTok, "DEV-1"},
		{"/api/issues?project=DEV", "project:OPS", e.admin, "conflict"},
	} {
		c, got, body := e.get(t, tc.path, tc.q, tc.auth)
		if tc.want == "conflict" {
			if c != http.StatusBadRequest {
				t.Errorf("%s %q: want 400, got %d %s", tc.path, tc.q, c, body)
			}
			continue
		}
		if c != http.StatusOK {
			t.Errorf("%s %q: %d %s", tc.path, tc.q, c, body)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %q: got [%s] want [%s]", tc.path, tc.q, got, tc.want)
		}
	}
}

// QA: every malformed query is a 400 with a message, on both endpoints, and
// conflicts between parameters and q are 400.
func TestQAQueryHTTPErrors(t *testing.T) {
	e := newQAQueryEnv(t)
	for _, tc := range []struct{ path, q, want string }{
		{"/api/issues", "asignee:me", "unknown filter"},
		{"/api/issues", "priority:>=5", "priority"},
		{"/api/issues", "priority:=-1", "priority"},
		{"/api/issues", "priority:>>3", "priority"},
		{"/api/issues", `label:""`, "needs a value"},
		{"/api/issues", `state:"In "Progress"`, "unclosed quote"},
		{"/api/issues", "is:open is:closed", "cannot both hold"},
		{"/api/issues", "is:blocked is:unblocked", "cannot both hold"},
		{"/api/issues", "has:label", "takes parent"},
		{"/api/issues", "state:Todo state:Done", "given twice"},
		{"/api/issues", "has:parent no:parent", "given twice"},
		{"/api/issues?parent=root", "has:parent", "both as a parameter"},
		{"/api/issues?parent=DEV-1", "no:parent", "both as a parameter"},
		{"/api/issues?unblocked=true", "is:blocked", "cannot both hold"},
		{"/api/issues?priority=3", "priority:>=2", "both as a parameter"},
		{"/api/issues?assignee=me", "assignee:none", "both as a parameter"},
		{"/api/issues?cycle=none", "cycle:none", "both as a parameter"},
		{"/api/issues?state=Todo", "state:todo", "both as a parameter"},
		{"/api/search", "asignee:me", "unknown filter"},
		{"/api/search", "priority:9", "priority"},
		{"/api/search", "is:done", "is: takes"},
		{"/api/search", "fix: login", "unknown filter"},
		{"/api/search", `crash "on login`, "unclosed quote"},
		{"/api/search", "is:open\x00", "is: takes"},
	} {
		for _, auth := range []string{e.admin, e.readTok, e.devTok} {
			c, _, body := e.get(t, tc.path, tc.q, auth)
			if c != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Errorf("%s %q: want 400 containing %q, got %d %s", tc.path, tc.q, tc.want, c, body)
			}
		}
	}
	// Unauthenticated: 401 before parsing.
	if c, _, _ := e.get(t, "/api/search", "asignee:me", ""); c != http.StatusUnauthorized {
		t.Errorf("no auth: %d", c)
	}
	// Unknown project in q: a 4xx, not a 500.
	for _, p := range []string{"/api/issues", "/api/search"} {
		if c, _, b := e.get(t, p, "project:NOPE", e.admin); c < 400 || c >= 500 {
			t.Errorf("%s project:NOPE: %d %s", p, c, b)
		}
	}
}

// QA: large queries must never produce a 500.
func TestQAQueryHTTPLargeQueries(t *testing.T) {
	e := newQAQueryEnv(t)
	rep := func(s string, n int) string { return strings.TrimSpace(strings.Repeat(s+" ", n)) }
	for name, q := range map[string]string{
		"2000 free words":  rep("login", 2000),
		"1500 label:bug":   rep("label:bug", 1500),
		"1500 is:open":     rep("is:open", 1500),
		"50KB token":       strings.Repeat("x", 50000),
		"emoji":            rep("🐛", 2000),
		"vietnamese mixed": rep("đăng nhập is:open", 300),
	} {
		for _, p := range []string{"/api/issues", "/api/search"} {
			if c, _, b := e.get(t, p, q, e.admin); c >= 500 {
				t.Errorf("%s %s: %d %.200s", p, name, c, b)
			}
		}
	}
}

// QA: the search limit applies to filter-only searches.
func TestQAQueryHTTPSearchLimit(t *testing.T) {
	e := newQAQueryEnv(t)
	for i := 0; i < 60; i++ {
		call(t, "POST", e.url+"/api/issues", e.admin, `{"project_ref":"DEV","title":"bulk"}`)
	}
	for _, tc := range []struct {
		limit string
		want  int
	}{{"", 20}, {"&limit=50", 50}, {"&limit=51", 20}, {"&limit=-1", 20}, {"&limit=abc", 20}} {
		c, ids, b := e.get(t, "/api/search?x=1"+tc.limit, "is:open", e.admin)
		if c != http.StatusOK || len(strings.Split(ids, ",")) != tc.want {
			t.Errorf("limit %q: %d, %d results %.100s", tc.limit, c, len(strings.Split(ids, ",")), b)
		}
	}
}

// QA: oversized queries are a 400 that says why, on both endpoints.
func TestQAQueryHTTPOversizedIs400(t *testing.T) {
	e := newQAQueryEnv(t)
	for _, q := range []string{strings.Repeat("a", 1001), strings.TrimSpace(strings.Repeat("label:bug ", 51)), strings.Repeat("ạ", 400)} {
		for _, p := range []string{"/api/issues", "/api/search"} {
			if c, _, b := e.get(t, p, q, e.admin); c != http.StatusBadRequest || !strings.Contains(b, "query") {
				t.Errorf("%s %.20q...: want 400 naming the query, got %d %.200s", p, q, c, b)
			}
		}
	}
	if c, ids, b := e.get(t, "/api/search", strings.TrimSpace(strings.Repeat("is:open ", 50)), e.admin); c != http.StatusOK || ids == "" {
		t.Errorf("50 tokens should work: %d %s", c, b)
	}
}
