package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// #17: GET /api/issues and /api/search take the issue query syntax in q.
func TestIssueQueryOverHTTP(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	for _, b := range []string{
		`{"project_ref":"DEV","title":"login crash","assignee_id":"me","label_ids":["bug"],"priority":4}`,
		`{"project_ref":"DEV","title":"login slow","label_ids":["bug"],"priority":2}`,
		`{"project_ref":"DEV","title":"docs","assignee_id":"me","state_id":"Done","priority":3}`,
	} {
		if c, r := call(t, "POST", ts.URL+"/api/issues", admin, b); c != http.StatusCreated {
			t.Fatalf("create: %d %s", c, r)
		}
	}
	titles := func(path, q string) (int, string) {
		c, b := call(t, "GET", ts.URL+path+"q="+url.QueryEscape(q), admin, "")
		return c, b
	}
	for _, tc := range []struct {
		path, q string
		want    []string
		notWant []string
	}{
		{"/api/issues?project=DEV&", "assignee:me is:open", []string{"login crash"}, []string{"login slow", "docs"}},
		{"/api/issues?project=DEV&", "label:bug priority:>=3", []string{"login crash"}, []string{"login slow"}},
		{"/api/issues?", "is:closed", []string{"docs"}, []string{"login"}},
		{"/api/issues?", "login priority:<high", []string{"login slow"}, []string{"login crash", "docs"}},
		{"/api/search?", "assignee:me crash", []string{"login crash"}, []string{"docs", "login slow"}},
	} {
		c, b := titles(tc.path, tc.q)
		if c != http.StatusOK {
			t.Errorf("%s%s: %d %s", tc.path, tc.q, c, b)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(b, `"title":"`+w+`"`) {
				t.Errorf("%q: missing %q", tc.q, w)
			}
		}
		for _, n := range tc.notWant {
			if strings.Contains(b, `"title":"`+n) {
				t.Errorf("%q: should not find %q", tc.q, n)
			}
		}
	}
	// A project-restricted token cannot reach another project through q.
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"OPS","title":"ops secret"}`)
	users, _ := srv.store.ListUsers()
	devTok, _, err := srv.store.CreateAPIToken(users[0].ID, "dev only", db.TokenScope{Projects: []string{"DEV"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/issues?q=", "/api/search?q="} {
		for _, q := range []string{"project:OPS", "project:OPS secret", "secret", "is:open"} {
			c, b := call(t, "GET", ts.URL+path+url.QueryEscape(q), devTok, "")
			if strings.Contains(b, "ops secret") {
				t.Errorf("DEV-only token got an OPS issue from %s%s: %d %s", path, q, c, b)
			}
		}
	}

	for _, tc := range []struct{ path, q, want string }{
		{"/api/issues?", "asignee:me", `unknown filter \"asignee:\"`},
		{"/api/issues?state=Todo&", "state:Done", "both as a parameter and in the query"},
		{"/api/search?", `state:"In Progress`, "unclosed quote"},
	} {
		if c, b := titles(tc.path, tc.q); c != http.StatusBadRequest || !strings.Contains(b, tc.want) {
			t.Errorf("%s%s: want 400 with %q, got %d %s", tc.path, tc.q, tc.want, c, b)
		}
	}
}
