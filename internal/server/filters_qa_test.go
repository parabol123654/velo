package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func qaCreateFilter(t *testing.T, e *qaQueryEnv, auth, name, query string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"name": name, "query": query})
	c, body := call(t, "POST", e.url+"/api/filters", auth, string(b))
	if c != http.StatusCreated {
		t.Fatalf("create %q: %d %s", name, c, body)
	}
	return idOf(body)
}

// QA: another user's filter is invisible through every method and every
// credential kind, and the 404 is the same as for an id that does not exist.
func TestQAFiltersOtherUserEveryMethod(t *testing.T) {
	e := newQAQueryEnv(t)
	id := qaCreateFilter(t, e, e.admin, "Admin's", "assignee:me")
	_, missing := call(t, "PATCH", e.url+"/api/filters/no-such-id", e.bob, `{"name":"x"}`)
	for _, auth := range []string{e.bob, e.bobTok} {
		if _, b := call(t, "GET", e.url+"/api/filters", auth, ""); strings.Contains(b, id) || strings.Contains(b, "Admin's") {
			t.Errorf("bob lists admin's filter: %s", b)
		}
		for _, tc := range []struct{ method, body string }{
			{"PATCH", `{"name":"x"}`},
			{"PATCH", `{"query":"is:open"}`},
			{"PATCH", `{"position":-1}`},
			{"PATCH", `{}`},
			{"DELETE", ""},
		} {
			c, b := call(t, tc.method, e.url+"/api/filters/"+id, auth, tc.body)
			if c != http.StatusNotFound {
				t.Errorf("bob %s %s: %d %s", tc.method, tc.body, c, b)
			}
			if tc.method == "PATCH" && b != missing {
				t.Errorf("404 body differs from a missing id: %q vs %q", b, missing)
			}
		}
		// No GET by id that could leak it.
		if c, b := call(t, "GET", e.url+"/api/filters/"+id, auth, ""); c == http.StatusOK || strings.Contains(b, "assignee:me") {
			t.Errorf("GET by id leaks: %d %s", c, b)
		}
	}
	// Unchanged for the owner.
	if _, b := call(t, "GET", e.url+"/api/filters", e.admin, ""); !strings.Contains(b, `"name":"Admin's"`) || !strings.Contains(b, `"query":"assignee:me"`) {
		t.Fatalf("admin's filter changed: %s", b)
	}
}

// QA: no login, read-only token (every write method), project-restricted token.
func TestQAFiltersTokenRules(t *testing.T) {
	e := newQAQueryEnv(t)
	id := qaCreateFilter(t, e, e.admin, "Mine", "is:open")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/filters", ""},
		{"POST", "/api/filters", `{"name":"x","query":"is:open"}`},
		{"PATCH", "/api/filters/" + id, `{"name":"x"}`},
		{"DELETE", "/api/filters/" + id, ""},
	} {
		if c, _ := call(t, tc.method, e.url+tc.path, "", tc.body); c != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: %d", tc.method, tc.path, c)
		}
		if c, _ := call(t, tc.method, e.url+tc.path, "garbage", tc.body); c != http.StatusUnauthorized {
			t.Errorf("bad token %s %s: %d", tc.method, tc.path, c)
		}
		if tc.method != "GET" {
			if c, _ := call(t, tc.method, e.url+tc.path, e.readTok, tc.body); c != http.StatusForbidden {
				t.Errorf("read-only token %s %s: %d", tc.method, tc.path, c)
			}
		}
	}
	if _, b := call(t, "GET", e.url+"/api/filters", e.admin, ""); !strings.Contains(b, `"name":"Mine"`) {
		t.Fatalf("a refused request changed the filter: %s", b)
	}
	// DEV-only token: full CRUD on its user's filters.
	fid := qaCreateFilter(t, e, e.devTok, "from dev token", "label:bug")
	if c, b := call(t, "PATCH", e.url+"/api/filters/"+fid, e.devTok, `{"name":"renamed"}`); c != http.StatusOK {
		t.Errorf("dev token rename: %d %s", c, b)
	}
	if c, _ := call(t, "DELETE", e.url+"/api/filters/"+fid, e.devTok, ""); c != http.StatusOK {
		t.Errorf("dev token delete: %d", c)
	}
}

// QA: a disabled user's session and token cannot use filters; re-enabled the
// token works again and the filters are still there.
func TestQAFiltersDisabledUser(t *testing.T) {
	e := newQAQueryEnv(t)
	qaCreateFilter(t, e, e.bob, "Bob's", "assignee:me")
	if c, b := call(t, "PATCH", e.url+"/api/users/"+e.bobID, e.admin, `{"disabled":true}`); c != http.StatusOK {
		t.Fatalf("disable: %d %s", c, b)
	}
	for _, auth := range []string{e.bob, e.bobTok} {
		if c, _ := call(t, "GET", e.url+"/api/filters", auth, ""); c != http.StatusUnauthorized {
			t.Errorf("disabled user lists: %d", c)
		}
		if c, _ := call(t, "POST", e.url+"/api/filters", auth, `{"name":"x","query":"is:open"}`); c != http.StatusUnauthorized {
			t.Errorf("disabled user creates: %d", c)
		}
	}
	call(t, "PATCH", e.url+"/api/users/"+e.bobID, e.admin, `{"disabled":false}`)
	if _, b := call(t, "GET", e.url+"/api/filters", e.bobTok, ""); !strings.Contains(b, "Bob's") {
		t.Fatalf("after re-enable: %s", b)
	}
}

// QA: a filter naming a project the token cannot see can be saved; using it
// through that token does not return the other project's issues.
func TestQAFiltersOtherProjectQuery(t *testing.T) {
	e := newQAQueryEnv(t)
	qaCreateFilter(t, e, e.devTok, "ops bugs", "project:OPS label:bug")
	_, b := call(t, "GET", e.url+"/api/filters", e.devTok, "")
	var list []struct{ Query string }
	_ = json.Unmarshal([]byte(b), &list)
	if len(list) != 1 {
		t.Fatalf("list: %s", b)
	}
	// As the board would: /api/issues?q=<saved query>, with and without project=DEV.
	for _, path := range []string{"/api/issues", "/api/issues?project=DEV", "/api/search"} {
		c, ids, body := e.get(t, path, list[0].Query, e.devTok)
		if strings.Contains(ids, "OPS") || strings.Contains(body, "secret login") {
			t.Errorf("%s leaks OPS through a saved filter: %d %s", path, c, body)
		}
	}
	// The admin's full token does see it.
	if _, ids, _ := e.get(t, "/api/issues", list[0].Query, e.adminTok); ids != "OPS-1" {
		t.Errorf("full token: %q", ids)
	}
}

// QA: bad bodies.
func TestQAFiltersBadBodies(t *testing.T) {
	e := newQAQueryEnv(t)
	id := qaCreateFilter(t, e, e.admin, "Mine", "is:open")
	for _, tc := range []struct {
		label, method, path, body string
		want                      int
	}{
		{"create empty body", "POST", "/api/filters", "", http.StatusBadRequest},
		{"create not JSON", "POST", "/api/filters", "name=x", http.StatusBadRequest},
		{"create array", "POST", "/api/filters", `[]`, http.StatusBadRequest},
		{"create name number", "POST", "/api/filters", `{"name":1,"query":"is:open"}`, http.StatusBadRequest},
		{"create query object", "POST", "/api/filters", `{"name":"y","query":{}}`, http.StatusBadRequest},
		{"create nulls", "POST", "/api/filters", `{"name":null,"query":null}`, http.StatusBadRequest},
		{"create spaces name", "POST", "/api/filters", `{"name":"   ","query":"is:open"}`, http.StatusBadRequest},
		{"create 81 chars", "POST", "/api/filters", `{"name":"` + strings.Repeat("a", 81) + `","query":"is:open"}`, http.StatusBadRequest},
		{"create dup other case", "POST", "/api/filters", `{"name":"MINE","query":"is:open"}`, http.StatusConflict},
		{"patch empty body", "PATCH", "/api/filters/" + id, "", http.StatusBadRequest},
		{"patch position string", "PATCH", "/api/filters/" + id, `{"position":"1"}`, http.StatusBadRequest},
		{"patch name number", "PATCH", "/api/filters/" + id, `{"name":5}`, http.StatusBadRequest},
		{"patch position huge", "PATCH", "/api/filters/" + id, `{"position":1e400}`, http.StatusBadRequest},
		{"patch bad query", "PATCH", "/api/filters/" + id, `{"query":"is:nosuch"}`, http.StatusBadRequest},
		{"patch blank name", "PATCH", "/api/filters/" + id, `{"name":"  "}`, http.StatusBadRequest},
		{"patch null fields", "PATCH", "/api/filters/" + id, `{"name":null,"query":null,"position":null}`, http.StatusOK},
		{"patch unknown fields", "PATCH", "/api/filters/" + id, `{"nme":"x","user_id":"someone"}`, http.StatusOK},
		{"patch empty object", "PATCH", "/api/filters/" + id, `{}`, http.StatusOK},
		{"patch own name other case", "PATCH", "/api/filters/" + id, `{"name":"MINE"}`, http.StatusOK},
	} {
		if c, b := call(t, tc.method, e.url+tc.path, e.admin, tc.body); c != tc.want {
			t.Errorf("%s: want %d, got %d %s", tc.label, tc.want, c, b)
		}
	}
	_, b := call(t, "GET", e.url+"/api/filters", e.admin, "")
	if !strings.Contains(b, `"name":"MINE"`) || !strings.Contains(b, `"query":"is:open"`) || strings.Count(b, `"id"`) != 1 {
		t.Fatalf("final state: %s", b)
	}
}

// QA: 80 Vietnamese characters through the API (SPEC: "1-80 characters").
func TestQAFiltersVietnameseNameLength(t *testing.T) {
	e := newQAQueryEnv(t)
	name := strings.Repeat("Lỗi ", 20) // 80 runes, trimmed to 79
	b, _ := json.Marshal(map[string]string{"name": name, "query": "is:open"})
	if c, body := call(t, "POST", e.url+"/api/filters", e.admin, string(b)); c != http.StatusCreated {
		t.Errorf("79-character Vietnamese name: %d %s", c, body)
	}
}

// QA: concurrent same-name creates over HTTP: one 201, the rest 409, no 500.
func TestQAFiltersConcurrentSameName(t *testing.T) {
	e := newQAQueryEnv(t)
	const n = 16
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			auth := e.admin
			if i%2 == 1 {
				auth = e.adminTok // same user through a token
			}
			codes[i], _ = call(t, "POST", e.url+"/api/filters", auth, `{"name":"Race","query":"is:open"}`)
		}(i)
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("want 1 created, got %d (%v)", created, codes)
	}
}

// QA: deleting a user over HTTP removes their filters; others' filters stay.
func TestQAFiltersDeleteUserHTTP(t *testing.T) {
	e := newQAQueryEnv(t)
	// bob has assigned issues in e (cannot be deleted); make a fresh user instead.
	c, b := call(t, "POST", e.url+"/api/users", e.admin, `{"email":"carol@velo.dev","name":"Carol","password":"password123"}`)
	if c != http.StatusCreated && c != http.StatusOK {
		t.Fatalf("create carol: %d %s", c, b)
	}
	carolID := idOf(b)
	if carolID == "" {
		var v struct{ User struct{ ID string } }
		_ = json.Unmarshal([]byte(b), &v)
		carolID = v.User.ID
	}
	_, lb := call(t, "POST", e.url+"/api/auth/login", "", `{"email":"carol@velo.dev","password":"password123"}`)
	carol := sessionOf(t, lb)
	qaCreateFilter(t, e, carol, "carol's", "is:open")
	qaCreateFilter(t, e, e.bob, "bob's", "is:open")
	qaCreateFilter(t, e, e.admin, "admin's", "is:open")
	if c, b := call(t, "DELETE", e.url+"/api/users/"+carolID, e.admin, ""); c != http.StatusOK && c != http.StatusNoContent {
		t.Fatalf("delete carol: %d %s", c, b)
	}
	if c, _ := call(t, "GET", e.url+"/api/filters", carol, ""); c != http.StatusUnauthorized {
		t.Errorf("deleted user's session still works: %d", c)
	}
	if _, b := call(t, "GET", e.url+"/api/filters", e.bob, ""); !strings.Contains(b, "bob's") {
		t.Errorf("bob's filter lost: %s", b)
	}
	if _, b := call(t, "GET", e.url+"/api/filters", e.admin, ""); !strings.Contains(b, "admin's") || strings.Contains(b, "carol") {
		t.Errorf("admin's list: %s", b)
	}
}
