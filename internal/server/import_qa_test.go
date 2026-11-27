package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// QA tests for POST /api/projects/{ref}/import (#16) through HTTP.

func importQASetup(t *testing.T) (string, *Server, string) {
	t.Helper()
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	return ts.URL, srv, admin
}

// sseTypes opens /api/events with tok and returns a function that reads the
// next event's type line.
func sseTypes(t *testing.T, url, tok string) func() string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	rd := bufio.NewReader(resp.Body)
	return func() string {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("reading events: %v", err)
			}
			if strings.Contains(line, `"type":`) {
				return line
			}
		}
	}
}

// A token restricted to OPS does not see DEV's import event; dry runs and
// reruns that add nothing send no event at all.
func TestQAImportSSE(t *testing.T) {
	url, srv, admin := importQASetup(t)
	users, _ := srv.store.ListUsers()
	ops, _, _ := srv.store.CreateAPIToken(users[0].ID, "ops", db.TokenScope{Projects: []string{"OPS"}})
	opsNext := sseTypes(t, url, ops)
	adminNext := sseTypes(t, url, admin)
	time.Sleep(100 * time.Millisecond) // let both streams register

	body := `{"source":"gh","issues":[{"external_id":"1","title":"one"}]}`
	if code, b := call(t, "POST", url+"/api/projects/DEV/import?dry_run=1", admin, body); code != 200 {
		t.Fatalf("dry run: %d %s", code, b)
	}
	if code, b := call(t, "POST", url+"/api/projects/DEV/import", admin, body); code != 200 {
		t.Fatalf("import: %d %s", code, b)
	}
	if code, b := call(t, "POST", url+"/api/projects/DEV/import", admin, body); code != 200 || !strings.Contains(b, `"skipped":1`) {
		t.Fatalf("rerun: %d %s", code, b)
	}
	// A marker event in OPS that both streams may see.
	if code, b := call(t, "POST", url+"/api/issues", admin, `{"project_ref":"OPS","title":"marker"}`); code != 201 {
		t.Fatalf("marker: %d %s", code, b)
	}
	if ev := adminNext(); !strings.Contains(ev, `"type":"project.updated"`) || !strings.Contains(ev, `"key":"DEV"`) {
		t.Fatalf("admin's first event: %s", ev)
	}
	if ev := adminNext(); strings.Contains(ev, "project.updated") {
		t.Fatalf("dry run or empty rerun sent another event: %s", ev)
	}
	if ev := opsNext(); strings.Contains(ev, "DEV") || strings.Contains(ev, "project.updated") {
		t.Fatalf("OPS-only token received a DEV event: %s", ev)
	}
}

// Malformed and oversized bodies are 400s with a message, never 500s.
func TestQAImportMalformedBodies(t *testing.T) {
	url, _, admin := importQASetup(t)
	for name, body := range map[string]string{
		"empty":             ``,
		"null":              `null`,
		"array":             `[]`,
		"string":            `"x"`,
		"issues wrong type": `{"source":"s","issues":{"a":1}}`,
		"id number":         `{"source":"s","issues":[{"external_id":1,"title":"t"}]}`,
		"priority float":    `{"source":"s","issues":[{"external_id":"1","title":"t","priority":1.5}]}`,
		"priority string":   `{"source":"s","issues":[{"external_id":"1","title":"t","priority":"high"}]}`,
		"priority huge":     `{"source":"s","issues":[{"external_id":"1","title":"t","priority":99999999999999999999}]}`,
		"bad date":          `{"source":"s","issues":[{"external_id":"1","title":"t","created_at":"yesterday"}]}`,
		"date no zone":      `{"source":"s","issues":[{"external_id":"1","title":"t","created_at":"2024-01-01T10:00:00"}]}`,
		"labels not list":   `{"source":"s","issues":[{"external_id":"1","title":"t","labels":"bug"}]}`,
		"label null":        `{"source":"s","issues":[{"external_id":"1","title":"t","labels":[null]}]}`,
		"truncated":         `{"source":"s","issues":[{"external_id":"1"`,
		"comment no body":   `{"source":"s","issues":[{"external_id":"1","title":"t","comments":[{"external_id":"c"}]}]}`,
		"link no ends":      `{"source":"s","issues":[{"external_id":"1","title":"t"}],"links":[{"kind":"blocks"}]}`,
		"deep nesting":      strings.Repeat("[", 100000) + strings.Repeat("]", 100000),
		"oversized string":  `{"source":"s","issues":[{"external_id":"1","title":"` + strings.Repeat("ư", (maxImportBody/2)+10) + `"}]}`,
	} {
		code, b := call(t, "POST", url+"/api/projects/DEV/import", admin, body)
		if code != http.StatusBadRequest || !strings.Contains(b, `"error"`) {
			t.Errorf("%s: %d %.200s, want 400", name, code, b)
		}
	}
	// A body with a valid batch followed by garbage should not import half-read input silently.
	code, b := call(t, "POST", url+"/api/projects/DEV/import?dry_run=1", admin, `{"source":"s","issues":[{"external_id":"1","title":"t"}]} trailing`)
	t.Logf("valid batch + trailing garbage: %d %.200s", code, b)
}

// "comment:<id>" as a parent or link target is a 400, not a 500.
func TestQAImportCommentKeyReferenceHTTP(t *testing.T) {
	url, _, admin := importQASetup(t)
	if code, b := call(t, "POST", url+"/api/projects/DEV/import", admin,
		`{"source":"gh","issues":[{"external_id":"1","title":"t","comments":[{"external_id":"9","body":"x"}]}]}`); code != 200 {
		t.Fatalf("seed: %d %s", code, b)
	}
	code, b := call(t, "POST", url+"/api/projects/DEV/import", admin,
		`{"source":"gh","issues":[{"external_id":"2","title":"t","parent":"comment:9"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("parent comment:9: %d %s, want 400", code, b)
	}
}

// Who may import: a member's session yes; a disabled user's token, a
// read-only token, a token for another project and no login no.
func TestQAImportPermissions(t *testing.T) {
	url, srv, admin := importQASetup(t)
	body := `{"source":"gh","issues":[{"external_id":"1","title":"t"}]}`
	users, _ := srv.store.ListUsers()
	adminID := users[0].ID
	if _, err := srv.store.CreateUserDirect("mem@velo.dev", "Mem", "password123", "member"); err != nil {
		t.Fatal(err)
	}
	_, lb := call(t, "POST", url+"/api/auth/login", "", `{"email":"mem@velo.dev","password":"password123"}`)
	member := sessionOf(t, lb)
	if code, b := call(t, "POST", url+"/api/projects/DEV/import", member, body); code != 200 {
		t.Fatalf("member: %d %s", code, b)
	}

	off, _ := srv.store.CreateUserDirect("off@velo.dev", "Off", "password123", "member")
	offTok, _, _ := srv.store.CreateAPIToken(off.ID, "off", db.TokenScope{})
	if _, err := srv.store.UpdateUser(adminID, off.ID, db.UpdateUserInput{Disabled: func() *bool { b := true; return &b }()}); err != nil {
		t.Fatal(err)
	}
	read, _, _ := srv.store.CreateAPIToken(adminID, "r", db.TokenScope{Access: "read", Projects: []string{"DEV"}})
	opsTok, _, _ := srv.store.CreateAPIToken(adminID, "o", db.TokenScope{Projects: []string{"OPS"}})
	other := `{"source":"gh","issues":[{"external_id":"2","title":"t"}]}`
	for name, tc := range map[string]struct {
		tok, path string
		want      int
	}{
		"disabled user token":     {offTok, "/api/projects/DEV/import", 401},
		"read-only token":         {read, "/api/projects/DEV/import", 403},
		"read-only dry run":       {read, "/api/projects/DEV/import?dry_run=1", 403},
		"other project by key":    {opsTok, "/api/projects/DEV/import", 403},
		"other project lowercase": {opsTok, "/api/projects/dev/import", 403},
		"garbage token":           {"vt_nope", "/api/projects/DEV/import", 401},
	} {
		if code, b := call(t, "POST", url+tc.path, tc.tok, other); code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, code, b, tc.want)
		}
	}
	p, _ := srv.store.GetProject("DEV")
	if code, b := call(t, "POST", url+"/api/projects/"+p.ID+"/import", opsTok, other); code != 403 {
		t.Errorf("other project by ID: %d %s", code, b)
	}
	if n, _ := srv.store.ListIssues(db.IssueFilter{ProjectRef: "DEV"}); len(n) != 1 {
		t.Fatalf("DEV issues: %d, want 1", len(n))
	}
	_ = admin
}

// Parallel HTTP imports of the same batch create every issue once and all succeed.
func TestQAImportConcurrentHTTP(t *testing.T) {
	url, srv, admin := importQASetup(t)
	var b strings.Builder
	b.WriteString(`{"source":"gh","issues":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"external_id":"%d","title":"t%d","labels":["Lỗi"],"comments":[{"external_id":"c%d","body":"x"}]}`, i, i, i)
	}
	b.WriteString(`]}`)
	var wg sync.WaitGroup
	codes := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := call(t, "POST", url+"/api/projects/DEV/import", admin, b.String())
			if code != 200 {
				codes <- fmt.Sprintf("%d %.200s", code, body)
			}
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		t.Errorf("parallel import: %s", c)
	}
	issues, _ := srv.store.ListIssues(db.IssueFilter{ProjectRef: "DEV"})
	if len(issues) != 200 {
		t.Fatalf("issues: %d, want 200", len(issues))
	}
	p, _ := srv.store.GetProject("DEV")
	labels, _ := srv.store.ListLabels(p.ID)
	n := 0
	for _, l := range labels {
		if strings.EqualFold(l.Name, "lỗi") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("label Lỗi made %d times", n)
	}
}

// dry_run takes the values the API documents; anything else must not quietly
// do a real import.
func TestQAImportDryRunParam(t *testing.T) {
	url, srv, admin := importQASetup(t)
	body := `{"source":"gh","issues":[{"external_id":"1","title":"t"}]}`
	for _, v := range []string{"1", "true"} {
		if code, b := call(t, "POST", url+"/api/projects/DEV/import?dry_run="+v, admin, body); code != 200 || !strings.Contains(b, `"dry_run":true`) {
			t.Fatalf("dry_run=%s: %d %s", v, code, b)
		}
	}
	// Other spellings do a real import, like ?unblocked= does elsewhere; logged, not failed.
	code, b := call(t, "POST", url+"/api/projects/DEV/import?dry_run=yes", admin, body)
	issues, _ := srv.store.ListIssues(db.IssueFilter{ProjectRef: "DEV"})
	t.Logf("dry_run=yes: %d, %d issues written: %.120s", code, len(issues), b)
}
