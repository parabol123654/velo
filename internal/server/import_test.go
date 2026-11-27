package server

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

const importBody = `{"source":"github","issues":[
	{"external_id":"o/r#1","title":"Đăng nhập lỗi","labels":["Lỗi"],"comments":[{"external_id":"9","author":"@bob","body":"Repro"}]},
	{"external_id":"o/r#2","title":"Child","parent":"o/r#1","state_category":"completed"}]}`

// #16: POST /api/projects/{ref}/import imports a batch once, reports a dry
// run without writing, and sends one project.updated event per real import.
func TestImportEndpoint(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	users, _ := srv.store.ListUsers()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := bufio.NewReader(resp.Body)
	events.ReadString('\n') // connected

	code, b := call(t, "POST", ts.URL+"/api/projects/DEV/import?dry_run=1", admin, importBody)
	if code != http.StatusOK || !strings.Contains(b, `"dry_run":true`) || !strings.Contains(b, `"created":2`) {
		t.Fatalf("dry run: %d %s", code, b)
	}
	if issues, _ := srv.store.ListIssues(db.IssueFilter{ProjectRef: "DEV"}); len(issues) != 0 {
		t.Fatalf("dry run wrote %d issues", len(issues))
	}

	tok, _, err := srv.store.CreateAPIToken(users[0].ID, "importer", db.TokenScope{})
	if err != nil {
		t.Fatal(err)
	}
	code, b = call(t, "POST", ts.URL+"/api/projects/DEV/import", tok, importBody)
	if code != http.StatusOK || !strings.Contains(b, `"created":2`) || !strings.Contains(b, `"identifier":"DEV-2"`) {
		t.Fatalf("import: %d %s", code, b)
	}
	line := ""
	for !strings.Contains(line, `"type":`) { // skip the connection greeting
		if line, err = events.ReadString('\n'); err != nil {
			t.Fatalf("no event after import: %v", err)
		}
	}
	if !strings.Contains(line, `"type":"project.updated"`) || !strings.Contains(line, `"key":"DEV"`) {
		t.Fatalf("event: %s", line)
	}
	comments, _ := srv.store.ListComments(mustIssue(t, srv, "DEV-1").ID)
	if len(comments) != 1 || comments[0].Via != "importer" {
		t.Fatalf("comment via: %+v", comments)
	}

	code, b = call(t, "POST", ts.URL+"/api/projects/DEV/import", tok, importBody)
	if code != http.StatusOK || !strings.Contains(b, `"created":0`) || !strings.Contains(b, `"skipped":2`) || !strings.Contains(b, `"labels_created":[]`) {
		t.Fatalf("rerun: %d %s", code, b)
	}
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/projects/DEV/import", `{"source":"github","issues":[{"external_id":"x","title":""}]}`, http.StatusBadRequest},
		{"/api/projects/DEV/import", `not json`, http.StatusBadRequest},
		{"/api/projects/NOPE/import", importBody, http.StatusNotFound},
		{"/api/projects/DEV/import?dry_run=yes", importBody, http.StatusBadRequest},
		{"/api/projects/DEV/import", importBody + `{"source":"x"}`, http.StatusBadRequest},
		{"/api/projects/DEV/import?dry_run=T", importBody, http.StatusBadRequest},
		{"/api/projects/DEV/import", importBody + `}`, http.StatusBadRequest},
	} {
		if code, b := call(t, "POST", ts.URL+tc.path, admin, tc.body); code != tc.want {
			t.Fatalf("%s %s: %d %s, want %d", tc.path, tc.body, code, b, tc.want)
		}
	}
}

// Import writes, so a read-only token cannot use it, and a project-restricted
// token can only import into its projects.
func TestImportEndpointTokenScopes(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	users, _ := srv.store.ListUsers()
	read, _, _ := srv.store.CreateAPIToken(users[0].ID, "reader", db.TokenScope{Access: "read"})
	dev, _, _ := srv.store.CreateAPIToken(users[0].ID, "dev", db.TokenScope{Projects: []string{"DEV"}})

	for _, tc := range []struct {
		name, tok, path string
		want            int
	}{
		{"read-only token", read, "/api/projects/DEV/import?dry_run=1", http.StatusForbidden},
		{"other project", dev, "/api/projects/OPS/import", http.StatusForbidden},
		{"own project", dev, "/api/projects/DEV/import", http.StatusOK},
		{"no auth", "", "/api/projects/DEV/import", http.StatusUnauthorized},
	} {
		if code, b := call(t, "POST", ts.URL+tc.path, tc.tok, importBody); code != tc.want {
			t.Fatalf("%s: %d %s, want %d", tc.name, code, b, tc.want)
		}
	}
	if issues, _ := srv.store.ListIssues(db.IssueFilter{ProjectRef: "OPS"}); len(issues) != 0 {
		t.Fatalf("refused import wrote %d OPS issues", len(issues))
	}
}

// A batch bigger than the import limit is a 400 that says so, not a 500 and
// not a truncated import.
func TestImportEndpointTooLarge(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	big := `{"source":"s","issues":[{"external_id":"1","title":"t","description":"` + strings.Repeat("x", maxImportBody+1) + `"}]}`
	code, b := call(t, "POST", ts.URL+"/api/projects/DEV/import", admin, big)
	if code != http.StatusBadRequest || !strings.Contains(b, "larger than") {
		t.Fatalf("oversized batch: %d %s", code, b)
	}
}

func mustIssue(t *testing.T, srv *Server, ref string) *db.Issue {
	t.Helper()
	iss, err := srv.store.GetIssue(ref)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}
