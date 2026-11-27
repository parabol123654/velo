package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// #17: /api/filters is per user; tokens act as their user, read-only tokens only read.
func TestSavedFiltersAPI(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/users", admin, `{"email":"member@velo.dev","name":"Member","password":"password123"}`)
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"member@velo.dev","password":"password123"}`)
	member := sessionOf(t, body)

	code, b := call(t, "POST", ts.URL+"/api/filters", admin, `{"name":"Mine","query":"assignee:me is:open"}`)
	if code != http.StatusCreated || !strings.Contains(b, `"query":"assignee:me is:open"`) {
		t.Fatalf("create: %d %s", code, b)
	}
	id := idOf(b)
	if code, b := call(t, "POST", ts.URL+"/api/filters", admin, `{"name":"Bad","query":"asignee:me"}`); code != http.StatusBadRequest || !strings.Contains(b, "unknown filter") {
		t.Fatalf("bad query: %d %s", code, b)
	}
	if code, b := call(t, "POST", ts.URL+"/api/filters", admin, `{"name":"mine","query":"is:open"}`); code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %s", code, b)
	}
	if code, b := call(t, "PATCH", ts.URL+"/api/filters/"+id, admin, `{"name":"Mine, open"}`); code != http.StatusOK || !strings.Contains(b, `"name":"Mine, open"`) || !strings.Contains(b, "assignee:me") {
		t.Fatalf("rename: %d %s", code, b)
	}

	// Another user sees none of them and cannot touch them.
	if _, b := call(t, "GET", ts.URL+"/api/filters", member, ""); strings.TrimSpace(b) != "[]" {
		t.Fatalf("member sees admin's filters: %s", b)
	}
	for _, m := range []string{"PATCH", "DELETE"} {
		if code, _ := call(t, m, ts.URL+"/api/filters/"+id, member, `{"name":"x"}`); code != http.StatusNotFound {
			t.Fatalf("member %s on admin's filter: %d", m, code)
		}
	}

	// Tokens act as their user; a project-restricted one too (filters have no project).
	users, _ := srv.store.ListUsers()
	var adminID string
	for _, u := range users {
		if u.Role == "admin" {
			adminID = u.ID
		}
	}
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	devTok, _, err := srv.store.CreateAPIToken(adminID, "dev", db.TokenScope{Projects: []string{"DEV"}})
	if err != nil {
		t.Fatal(err)
	}
	roTok, _, _ := srv.store.CreateAPIToken(adminID, "ro", db.TokenScope{Access: "read"})
	if _, b := call(t, "GET", ts.URL+"/api/filters", devTok, ""); !strings.Contains(b, "Mine, open") {
		t.Fatalf("token cannot list its user's filters: %s", b)
	}
	if _, b := call(t, "GET", ts.URL+"/api/filters", roTok, ""); !strings.Contains(b, "Mine, open") {
		t.Fatalf("read-only token cannot list: %s", b)
	}
	if code, _ := call(t, "POST", ts.URL+"/api/filters", roTok, `{"name":"x","query":"is:open"}`); code != http.StatusForbidden {
		t.Fatalf("read-only token created a filter: %d", code)
	}

	if code, _ := call(t, "DELETE", ts.URL+"/api/filters/"+id, admin, ""); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if _, b := call(t, "GET", ts.URL+"/api/filters", admin, ""); strings.TrimSpace(b) != "[]" {
		t.Fatalf("after delete: %s", b)
	}
}
