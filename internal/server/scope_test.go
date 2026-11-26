package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

func TestTokenScopes(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)

	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"dev thing"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"OPS","title":"ops thing"}`)
	_, body = call(t, "POST", ts.URL+"/api/issues/OPS-1/comments", admin, `{"body":"secret ops note"}`)
	opsComment := idOf(body)
	_, body = call(t, "POST", ts.URL+"/api/projects/OPS/cycles", admin, `{"name":"Ops sprint"}`)
	opsCycle := idOf(body)
	_, body = call(t, "POST", ts.URL+"/api/projects/OPS/labels", admin, `{"name":"ops-only"}`)
	opsLabel := idOf(body)

	token := func(scope string) string {
		t.Helper()
		code, b := call(t, "POST", ts.URL+"/api/tokens", admin, scope)
		if code != http.StatusCreated {
			t.Fatalf("create token %s: %d %s", scope, code, b)
		}
		var v struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(b), &v)
		return v.Token
	}
	full := token(`{"name":"full"}`)
	readOnly := token(`{"name":"reader","access":"read"}`)
	devOnly := token(`{"name":"dev agent","projects":["DEV"]}`)

	if code, _ := call(t, "POST", ts.URL+"/api/tokens", admin, `{"name":"x","access":"admin"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid access accepted: %d", code)
	}
	if code, _ := call(t, "POST", ts.URL+"/api/tokens", admin, `{"name":"x","projects":["NOPE"]}`); code != http.StatusBadRequest {
		t.Fatalf("unknown project accepted: %d", code)
	}

	expect := func(name, method, path, tok, body string, want int) string {
		t.Helper()
		code, b := call(t, method, ts.URL+path, tok, body)
		if code != want {
			t.Fatalf("%s: %s %s = %d, want %d (%s)", name, method, path, code, want, b)
		}
		return b
	}

	// Read-only token: GET works, writes are refused.
	expect("read list", "GET", "/api/issues", readOnly, "", 200)
	expect("read create", "POST", "/api/issues", readOnly, `{"project_ref":"DEV","title":"x"}`, 403)
	expect("read update", "PATCH", "/api/issues/DEV-1", readOnly, `{"priority":2}`, 403)
	expect("read comment", "POST", "/api/issues/DEV-1/comments", readOnly, `{"body":"x"}`, 403)

	// No token may manage credentials or reach admin endpoints, even an admin's full token.
	expect("token lists tokens", "GET", "/api/tokens", full, "", 403)
	expect("token mints token", "POST", "/api/tokens", full, `{"name":"y"}`, 403)
	expect("token changes password", "POST", "/api/auth/password", full, `{"current_password":"password123","new_password":"x1234567"}`, 403)
	expect("token admin", "GET", "/api/invites", full, "", 403)
	expect("full token works", "PATCH", "/api/issues/OPS-1", full, `{"priority":1}`, 200)

	// Project-restricted token: sees and changes only DEV.
	if b := expect("dev projects", "GET", "/api/projects", devOnly, "", 200); strings.Contains(b, "OPS") || !strings.Contains(b, "DEV") {
		t.Fatalf("restricted project list: %s", b)
	}
	if b := expect("dev issues", "GET", "/api/issues", devOnly, "", 200); strings.Contains(b, "OPS-1") || !strings.Contains(b, "DEV-1") {
		t.Fatalf("restricted issue list: %s", b)
	}
	if b := expect("dev issues filter OPS", "GET", "/api/issues?project=OPS", devOnly, "", 200); strings.Contains(b, "OPS-1") {
		t.Fatalf("project filter leaked: %s", b)
	}
	if b := expect("dev search", "GET", "/api/search?q=thing", devOnly, "", 200); strings.Contains(b, "OPS-1") {
		t.Fatalf("search leaked: %s", b)
	}
	for _, c := range []struct{ name, method, path, body string }{
		{"get project", "GET", "/api/projects/OPS", ""},
		{"rename project", "PATCH", "/api/projects/OPS", `{"name":"x"}`},
		{"export", "GET", "/api/projects/OPS/export", ""},
		{"states", "GET", "/api/projects/OPS/states", ""},
		{"create label", "POST", "/api/projects/OPS/labels", `{"name":"x"}`},
		{"delete label", "DELETE", "/api/labels/" + opsLabel, ""},
		{"create cycle", "POST", "/api/projects/OPS/cycles", `{"name":"x"}`},
		{"update cycle", "PATCH", "/api/cycles/" + opsCycle, `{"name":"x"}`},
		{"delete cycle", "DELETE", "/api/cycles/" + opsCycle, ""},
		{"get issue", "GET", "/api/issues/OPS-1", ""},
		{"update issue", "PATCH", "/api/issues/OPS-1", `{"priority":4}`},
		{"delete issue", "DELETE", "/api/issues/OPS-1", ""},
		{"create issue", "POST", "/api/issues", `{"project_ref":"OPS","title":"x"}`},
		{"comments", "GET", "/api/issues/OPS-1/comments", ""},
		{"add comment", "POST", "/api/issues/OPS-1/comments", `{"body":"x"}`},
		{"activities", "GET", "/api/issues/OPS-1/activities", ""},
		{"edit comment", "PATCH", "/api/comments/" + opsComment, `{"body":"x"}`},
		{"delete comment", "DELETE", "/api/comments/" + opsComment, ""},
		{"create project", "POST", "/api/projects", `{"key":"NEW","name":"x"}`},
		{"import", "POST", "/api/projects/import", `{}`},
	} {
		expect("dev token: "+c.name, c.method, c.path, devOnly, c.body, 403)
	}
	expect("dev token own project", "POST", "/api/issues", devOnly, `{"project_ref":"DEV","title":"by agent"}`, 201)
	if iss, _ := srv.store.GetIssue("OPS-1"); iss.Priority != 1 {
		t.Fatalf("OPS-1 changed through a restricted token: priority %d", iss.Priority)
	}
}

func TestTokenScopedSSEAndAttribution(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	users, _ := srv.store.ListUsers()
	devTok, _, err := srv.store.CreateAPIToken(users[0].ID, "Claude Code", db.TokenScope{Projects: []string{"DEV"}})
	if err != nil {
		t.Fatal(err)
	}

	// SSE: the DEV-only stream gets DEV events and never OPS events.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+devTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	rd.ReadString('\n') // connected

	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"OPS","title":"ops secret"}`)
	call(t, "POST", ts.URL+"/api/issues/OPS-1/comments", admin, `{"body":"ops comment"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"dev visible"}`)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the DEV event: %v", err)
		}
		if strings.Contains(line, "OPS") || strings.Contains(line, "ops ") {
			t.Fatalf("restricted stream received an OPS event: %s", line)
		}
		if strings.Contains(line, "dev visible") {
			break
		}
	}

	// Attribution: token writes record the token name, session writes do not.
	call(t, "POST", ts.URL+"/api/issues/DEV-1/comments", devTok, `{"body":"from the agent"}`)
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", devTok, `{"priority":3}`)
	call(t, "POST", ts.URL+"/api/issues/DEV-1/comments", admin, `{"body":"from a person"}`)
	_, b := call(t, "GET", ts.URL+"/api/issues/DEV-1?detail=1", admin, "")
	var d struct {
		Comments   []db.Comment  `json:"comments"`
		Activities []db.Activity `json:"activities"`
	}
	if err := json.Unmarshal([]byte(b), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Comments) != 2 || d.Comments[0].Via != "Claude Code" || d.Comments[1].Via != "" {
		t.Fatalf("comment via: %+v", d.Comments)
	}
	last := d.Activities[len(d.Activities)-1]
	if last.Field != "priority" || last.Via != "Claude Code" {
		t.Fatalf("activity via: %+v", last)
	}
	if d.Activities[0].Via != "" {
		t.Fatalf("session-created issue has via: %+v", d.Activities[0])
	}
}

func idOf(body string) string {
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &v)
	return v.ID
}
