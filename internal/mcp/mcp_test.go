package mcp

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/web"
)

func TestMCPStdioAgentWorkflow(t *testing.T) {
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	admin, err := st.CreateFirstAdmin("agent@velo.dev", "AI Pair", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	rawTok, _, err := st.CreateAPIToken(admin.ID, "MCP Test", db.TokenScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject("VELO", "Velo Tracker", "Fast local tracker"); err != nil {
		t.Fatal(err)
	}

	distFS, _ := web.DistFS()
	srv, err := server.New(st, tmp, distFS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	api := client.New(ts.URL, rawTok)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_issue","arguments":{"project":"VELO","title":"Support MCP stdio server","description":"Allow AI agents to manage tickets locally","priority":4,"labels":["feature"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"update_issue","arguments":{"ref":"VELO-1","state":"In Progress"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add_comment","arguments":{"ref":"VELO-1","body":"Implemented and verified via go test."}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"MCP stdio"}}}`,
	}

	var out bytes.Buffer
	if err := ServeStdio(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, api); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// 6 responses expected because notifications/initialized has no id and must NOT produce a response!
	if len(lines) != 6 {
		t.Fatalf("expected 6 JSON-RPC responses (notification ignored), got %d:\n%s", len(lines), out.String())
	}

	if !strings.Contains(lines[2], "VELO-1") {
		t.Fatalf("create_issue response missing VELO-1: %s", lines[2])
	}
	if !strings.Contains(lines[3], "In Progress") {
		t.Fatalf("update_issue response missing In Progress: %s", lines[3])
	}
	if !strings.Contains(lines[5], "VELO-1") {
		t.Fatalf("search_issues response missing VELO-1: %s", lines[5])
	}

	var checkResp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	for idx, l := range lines {
		if err := json.Unmarshal([]byte(l), &checkResp); err != nil {
			t.Fatalf("line %d invalid JSON: %v", idx, err)
		}
		if checkResp.Result.IsError {
			t.Fatalf("line %d returned tool error: %s", idx, l)
		}
	}
}

func TestMCPProjectToolsAndFriendlyRefs(t *testing.T) {
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, _ := st.CreateFirstAdmin("agent@velo.dev", "Agent", "secret123")
	rawTok, _, _ := st.CreateAPIToken(admin.ID, "MCP", db.TokenScope{})
	if _, err := st.CreateUserDirect("dev@velo.dev", "Dev", "secret123", "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject("VELO", "Velo", ""); err != nil {
		t.Fatal(err)
	}
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, tmp, distFS)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	api := client.New(ts.URL, rawTok)

	// Each step: tool, arguments, text the result must contain.
	steps := []struct{ tool, args, want string }{
		{"create_label", `{"project":"VELO","name":"infra","color":"#ef4444"}`, `"infra"`},
		{"create_cycle", `{"project":"VELO","name":"Sprint 1","start_date":"2026-12-01"}`, `"Sprint 1"`},
		{"get_project", `{"project":"VELO"}`, `"Sprint 1"`},
		{"list_users", `{}`, `dev@velo.dev`},
		{"create_issue", `{"project":"VELO","title":"Parent","assignee":"me","cycle":"Sprint 1","labels":["infra"]}`, `"cycle_name": "Sprint 1"`},
		{"create_issue", `{"project":"VELO","title":"Child","parent":"VELO-1"}`, `"parent_identifier": "VELO-1"`},
		{"update_issue", `{"ref":"VELO-2","assignee":"dev@velo.dev"}`, `"assignee_name": "Dev"`},
		{"list_issues", `{"project":"VELO","assignee":"me"}`, `"VELO-1"`},
		{"list_issues", `{"project":"VELO","parent":"VELO-1"}`, `"VELO-2"`},
		{"list_issues", `{"project":"VELO","cycle":"Sprint 1","label":"infra"}`, `"VELO-1"`},
		{"update_issue", `{"ref":"VELO-1","cycle":"","assignee":""}`, `"identifier": "VELO-1"`},
		{"export_project", `{"project":"VELO"}`, `VELO-2`},
		{"export_project", `{"project":"VELO","format":"json"}`, `"identifier":"VELO-1"`},
		{"delete_issue", `{"ref":"VELO-2"}`, `Deleted VELO-2`},
	}
	for _, s := range steps {
		var args map[string]any
		if err := json.Unmarshal([]byte(s.args), &args); err != nil {
			t.Fatal(err)
		}
		text, err := dispatchTool(api, s.tool, args)
		if err != nil {
			t.Fatalf("%s %s: %v", s.tool, s.args, err)
		}
		if !strings.Contains(text, s.want) {
			t.Fatalf("%s %s: result lacks %q:\n%s", s.tool, s.args, s.want, text)
		}
	}

	// Cleared fields, and filters that must not match.
	iss, _ := st.GetIssue("VELO-1")
	if iss.CycleID != "" || iss.AssigneeID != "" {
		t.Fatalf("clearing cycle/assignee failed: %+v", iss)
	}
	if text, _ := dispatchTool(api, "list_issues", map[string]any{"project": "VELO", "assignee": "dev@velo.dev"}); strings.Contains(text, "VELO-1") {
		t.Fatalf("assignee filter leaked: %s", text)
	}
	if _, err := dispatchTool(api, "update_issue", map[string]any{"ref": "VELO-1", "cycle": "Nope"}); err == nil {
		t.Fatal("unknown cycle name should fail")
	}
}

// newMCPTestAPI starts a server with one admin, project VELO and the given
// issue titles, and returns a client using the admin's API token.
func newMCPTestAPI(t *testing.T, titles ...string) (*client.Client, *db.Store) {
	t.Helper()
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("agent@velo.dev", "Agent", "secret123")
	rawTok, _, _ := st.CreateAPIToken(admin.ID, "MCP", db.TokenScope{})
	if _, err := st.CreateProject("VELO", "Velo", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range titles {
		if _, err := st.CreateIssue(admin.ID, db.CreateIssueInput{ProjectRef: "VELO", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, tmp, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return client.New(ts.URL, rawTok), st
}

func TestMCPLinks(t *testing.T) {
	api, _ := newMCPTestAPI(t, "Schema", "API on top")
	text, err := dispatchTool(api, "link_issues", map[string]any{"ref": "VELO-2", "kind": "blocked_by", "target": "VELO-1"})
	if err != nil || !strings.Contains(text, `"blocked_by"`) {
		t.Fatalf("link_issues: %v %s", err, text)
	}
	text, _ = dispatchTool(api, "list_issues", map[string]any{"project": "VELO", "unblocked": true})
	if !strings.Contains(text, "VELO-1") || strings.Contains(text, "VELO-2") {
		t.Fatalf("unblocked filter: %s", text)
	}
	if text, _ := dispatchTool(api, "get_issue", map[string]any{"ref": "VELO-2"}); !strings.Contains(text, `"blocked_by_count": 1`) {
		t.Fatalf("get_issue lacks blocked count: %s", text)
	}
	if _, err := dispatchTool(api, "unlink_issues", map[string]any{"ref": "VELO-1", "target": "VELO-2"}); err != nil {
		t.Fatal(err)
	}
	text, _ = dispatchTool(api, "list_issues", map[string]any{"project": "VELO", "unblocked": true})
	if !strings.Contains(text, "VELO-2") {
		t.Fatalf("unlinked issue still blocked: %s", text)
	}
}

func TestMCPNextAndClaim(t *testing.T) {
	api, _ := newMCPTestAPI(t, "first")
	text, err := dispatchTool(api, "next_issue", map[string]any{"project": "VELO"})
	if err != nil || !strings.Contains(text, `"identifier": "VELO-1"`) {
		t.Fatalf("next_issue: %v %s", err, text)
	}
	text, err = dispatchTool(api, "claim_issue", map[string]any{"ref": "VELO-1"})
	if err != nil || !strings.Contains(text, `"state_name": "In Progress"`) {
		t.Fatalf("claim_issue: %v %s", err, text)
	}
	if text, _ := dispatchTool(api, "next_issue", map[string]any{"project": "VELO"}); !strings.HasPrefix(text, "No issue is waiting") {
		t.Fatalf("empty queue: %s", text)
	}
}

func TestMCPCheckItem(t *testing.T) {
	api, st := newMCPTestAPI(t)
	users, _ := st.ListUsers()
	st.CreateIssue(users[0].ID, db.CreateIssueInput{ProjectRef: "VELO", Title: "x", Description: "- [ ] schema\n- [ ] api"})
	text, err := dispatchTool(api, "check_item", map[string]any{"ref": "VELO-1", "item": "api"})
	if err != nil || text != "- [ ] schema\n- [x] api" {
		t.Fatalf("check_item: %v %q", err, text)
	}
	text, err = dispatchTool(api, "check_item", map[string]any{"ref": "VELO-1", "index": float64(2), "checked": false})
	if err != nil || text != "- [ ] schema\n- [ ] api" {
		t.Fatalf("uncheck by index: %v %q", err, text)
	}
	if _, err := dispatchTool(api, "check_item", map[string]any{"ref": "VELO-1", "item": "deploy"}); err == nil {
		t.Fatal("unknown item should fail")
	}
}

func TestMCPResourcesAndPrompts(t *testing.T) {
	api, st := newMCPTestAPI(t, "Add login", "Write docs")
	users, _ := st.ListUsers()
	st.CreateCycle(mustProjectID(t, st, "VELO"), "Sprint 1", "", "", "", "active")
	cyc := "Sprint 1"
	st.UpdateIssue(users[0].ID, "VELO-1", db.UpdateIssueInput{CycleID: &cyc})
	st.LinkIssues(users[0].ID, "VELO-1", "blocks", "VELO-2")
	st.AddComment(users[0].ID, "VELO-1", "Started on this")

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/templates/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"velo://issue/VELO-1"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"velo://project/VELO"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"velo://issue/VELO-99"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":8,"method":"prompts/get","params":{"name":"work_on_issue","arguments":{"ref":"VELO-1"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"prompts/get","params":{"name":"split_issue","arguments":{"ref":"VELO-2"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"prompts/get","params":{"name":"summarize_cycle","arguments":{"project":"VELO","cycle":"Sprint 1"}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"prompts/get","params":{"name":"work_on_issue","arguments":{}}}`,
	}
	var out bytes.Buffer
	if err := ServeStdio(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, api); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(requests) {
		t.Fatalf("got %d responses for %d requests", len(lines), len(requests))
	}
	want := []string{
		`"resources":{}`,                   // initialize advertises resources
		`velo://project/VELO`,              // resources/list
		`velo://issue/{identifier}`,        // templates
		`# VELO-1: Add login`,              // issue resource
		`In Progress (started)`,            // project resource lists states
		`"code":-32002`,                    // unknown issue
		`"name":"summarize_cycle"`,         // prompts/list
		`Call claim_issue with ref VELO-1`, // work_on_issue
		`parent VELO-2`,                    // split_issue
		`VELO-1 [Todo] Add login`,          // summarize_cycle lists the cycle's issue
		`argument \"ref\" is required`,     // missing argument
	}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Fatalf("response %d lacks %q:\n%s", i+1, w, lines[i])
		}
	}
	for _, w := range []string{"blocks VELO-2", "Started on this", "Cycle: Sprint 1"} {
		if !strings.Contains(lines[3], w) {
			t.Fatalf("issue resource lacks %q:\n%s", w, lines[3])
		}
	}
	if strings.Contains(lines[9], "VELO-2") {
		t.Fatalf("cycle summary includes an issue outside the cycle:\n%s", lines[9])
	}
}

func mustProjectID(t *testing.T, st *db.Store, key string) string {
	t.Helper()
	p, err := st.GetProject(key)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestMCPUnknownLabels(t *testing.T) {
	api, _ := newMCPTestAPI(t, "Existing")
	if _, err := dispatchTool(api, "create_issue", map[string]any{"project": "VELO", "title": "x", "labels": []any{"bug", "nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("create_issue with unknown label: want error naming it, got %v", err)
	}
	if text, _ := dispatchTool(api, "list_issues", map[string]any{"project": "VELO"}); strings.Contains(text, `"x"`) {
		t.Fatalf("refused create stored an issue: %s", text)
	}
	if _, err := dispatchTool(api, "update_issue", map[string]any{"ref": "VELO-1", "title": "changed", "labels": []any{"nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("update_issue with unknown label: want error naming it, got %v", err)
	}
	if text, _ := dispatchTool(api, "get_issue", map[string]any{"ref": "VELO-1"}); !strings.Contains(text, "Existing") {
		t.Fatalf("refused update changed the issue: %s", text)
	}
}
