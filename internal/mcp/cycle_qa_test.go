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

func qaCycles(t *testing.T, st *db.Store, project string) []db.Cycle {
	t.Helper()
	p, err := st.GetProject(project)
	if err != nil {
		t.Fatal(err)
	}
	cycles, err := st.ListCycles(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cycles
}

// Same name twice, names differing only in case, and a name equal to
// another cycle's ID.
func TestQAMCPUpdateCycleNameResolution(t *testing.T) {
	api, st := newMCPTestAPI(t)
	p, _ := st.GetProject("VELO")
	dupA, _ := st.CreateCycle(p.ID, "Sprint", "a", "", "", "active")
	dupB, _ := st.CreateCycle(p.ID, "SPRINT", "b", "", "", "active")
	target, _ := st.CreateCycle(p.ID, "Target", "t", "", "", "upcoming")
	// A cycle whose name is the target's ID.
	impostor, _ := st.CreateCycle(p.ID, target.ID, "imp", "", "", "upcoming")

	for _, ref := range []string{"Sprint", "sprint", "SPRINT", " sprint "} {
		_, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": ref, "status": "completed"})
		if err == nil || !strings.Contains(err.Error(), "use the cycle ID") {
			t.Errorf("ambiguous %q: want an ambiguity error, got %v", ref, err)
		}
	}
	for _, c := range qaCycles(t, st, "VELO") {
		if c.Status == "completed" {
			t.Fatalf("ambiguous name changed a cycle: %+v", c)
		}
	}
	// By ID each duplicate can be changed.
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": dupB.ID, "status": "completed"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range qaCycles(t, st, "VELO") {
		if (c.ID == dupB.ID) != (c.Status == "completed") {
			t.Fatalf("by ID changed the wrong cycle: %+v", c)
		}
	}
	_ = dupA

	// The ID wins over a name that equals it.
	text, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": target.ID, "description": "by id"})
	if err != nil || !strings.Contains(text, `"name": "Target"`) {
		t.Fatalf("ID ref: %v\n%s", err, text)
	}
	for _, c := range qaCycles(t, st, "VELO") {
		if c.ID == impostor.ID && c.Description != "imp" {
			t.Errorf("the cycle named like an ID was changed: %+v", c)
		}
	}

	// Rename, then find by the new name; the old name is gone.
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "target", "name": "Hit"}); err != nil {
		t.Fatal(err)
	}
	if text, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "HIT", "status": "active"}); err != nil || !strings.Contains(text, `"status": "active"`) || !strings.Contains(text, `"description": "by id"`) {
		t.Fatalf("by new name: %v\n%s", err, text)
	}
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Target", "status": "active"}); err == nil {
		t.Errorf("old name still resolves")
	}
	// Renaming onto another cycle's name (any case) makes that name ambiguous.
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "hit", "name": "sprint"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Sprint", "status": "upcoming"}); err == nil || !strings.Contains(err.Error(), "3 cycles") {
		t.Errorf("three cycles named sprint: %v", err)
	}
}

// No change fields, null values, missing project/cycle, other project.
func TestQAMCPUpdateCycleOddArgs(t *testing.T) {
	api, st := newMCPTestAPI(t)
	p, _ := st.GetProject("VELO")
	c, _ := st.CreateCycle(p.ID, "Sprint 1", "Ship search", "2026-12-01", "2026-12-14", "upcoming")
	if _, err := st.CreateProject("OPS", "Ops", ""); err != nil {
		t.Fatal(err)
	}
	unchanged := func(what string) {
		t.Helper()
		got := qaCycles(t, st, "VELO")[0]
		if got.Name != "Sprint 1" || got.Description != "Ship search" || got.StartDate != "2026-12-01" || got.EndDate != "2026-12-14" || got.Status != "upcoming" {
			t.Errorf("%s changed the cycle: %+v", what, got)
		}
	}

	// No change fields, or only nulls (which mean "keep", as over HTTP): an
	// error that says so, and nothing changes.
	for _, args := range []map[string]any{
		{"project": "VELO", "cycle": "Sprint 1"},
		{"project": "VELO", "cycle": "Sprint 1", "name": nil, "status": nil, "description": nil},
	} {
		if _, err := dispatchTool(api, "update_cycle", args); err == nil || !strings.Contains(err.Error(), "nothing to change") {
			t.Errorf("%v: want \"nothing to change\", got %v", args, err)
		}
	}
	unchanged("no fields / only nulls")

	for _, args := range []map[string]any{
		{"cycle": "Sprint 1", "status": "active"},
		{"project": "VELO", "status": "active"},
		{"project": "NOPE", "cycle": "Sprint 1", "status": "active"},
		{"project": "OPS", "cycle": "Sprint 1", "status": "active"},
		{"project": "OPS", "cycle": c.ID, "status": "active"}, // ID of another project's cycle
		{"project": "VELO", "cycle": "", "status": "active"},
	} {
		if _, err := dispatchTool(api, "update_cycle", args); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	unchanged("bad refs")

	// Wrong types become strings (status 1 is invalid; name 42 is a name).
	if _, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Sprint 1", "status": 1}); err == nil {
		t.Errorf("numeric status accepted")
	}
	if text, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Sprint 1", "name": 42}); err != nil || !strings.Contains(text, `"name": "42"`) {
		t.Errorf("numeric name: %v %s", err, text)
	}
}

// update_cycle through ServeStdio: listed in tools/list, success result,
// and an error result for bad input; a token limited to another project
// cannot find or change the cycle.
func TestQAMCPUpdateCycleStdio(t *testing.T) {
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("agent@velo.dev", "Agent", "secret123")
	velo, _ := st.CreateProject("VELO", "Velo", "")
	st.CreateProject("OPS", "Ops", "")
	if _, err := st.CreateCycle(velo.ID, "Sprint 1", "Ship search", "2026-12-01", "2026-12-14", "upcoming"); err != nil {
		t.Fatal(err)
	}
	full, _, _ := st.CreateAPIToken(admin.ID, "MCP", db.TokenScope{})
	opsOnly, _, _ := st.CreateAPIToken(admin.ID, "ops", db.TokenScope{Projects: []string{"OPS"}})
	readOnly, _, _ := st.CreateAPIToken(admin.ID, "ro", db.TokenScope{Access: "read"})
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, tmp, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	type resp struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	serve := func(tok string, reqs ...string) []resp {
		t.Helper()
		var out bytes.Buffer
		if err := ServeStdio(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, client.New(ts.URL, tok)); err != nil {
			t.Fatal(err)
		}
		var rs []resp
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var r resp
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("bad line %s", line)
			}
			rs = append(rs, r)
		}
		return rs
	}
	rs := serve(full,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"update_cycle","arguments":{"project":"VELO","cycle":"SPRINT 1","status":"active","end_date":""}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update_cycle","arguments":{"project":"VELO","cycle":"Sprint 1","status":"finished"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"update_cycle","arguments":{"project":"VELO","cycle":"Sprint 1","description":null}}}`,
	)
	if len(rs) != 4 {
		t.Fatalf("want 4 responses, got %d", len(rs))
	}
	var listed bool
	for _, tool := range rs[0].Result.Tools {
		listed = listed || tool.Name == "update_cycle"
	}
	if !listed {
		t.Errorf("update_cycle not in tools/list")
	}
	if rs[1].Result.IsError || len(rs[1].Result.Content) == 0 || !strings.Contains(rs[1].Result.Content[0].Text, `"status": "active"`) || strings.Contains(rs[1].Result.Content[0].Text, "2026-12-14") || !strings.Contains(rs[1].Result.Content[0].Text, "2026-12-01") {
		t.Errorf("update: %+v", rs[1].Result)
	}
	if !rs[2].Result.IsError {
		t.Errorf("invalid status not an error result: %+v", rs[2].Result)
	}
	t.Logf("description:null via MCP -> %+v", rs[3].Result)

	for name, tok := range map[string]string{"OPS-only": opsOnly, "read-only": readOnly} {
		rs = serve(tok, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_cycle","arguments":{"project":"VELO","cycle":"Sprint 1","status":"completed"}}}`)
		if len(rs) != 1 || !rs[0].Result.IsError {
			t.Errorf("%s token: want an error result, got %+v", name, rs)
		}
	}
	if c := qaCycles(t, st, "VELO")[0]; c.Status != "active" {
		t.Errorf("a forbidden token changed the cycle: %+v", c)
	}
}

// update_cycle reports the cycle's issue counts like get_project does, not zeros.
func TestQAMCPUpdateCycleReportsIssueCounts(t *testing.T) {
	api, st := newMCPTestAPI(t)
	p, _ := st.GetProject("VELO")
	c, _ := st.CreateCycle(p.ID, "Sprint 1", "", "", "", "active")
	users, _ := st.ListUsers()
	for _, title := range []string{"a", "b"} {
		if _, err := st.CreateIssue(users[0].ID, db.CreateIssueInput{ProjectRef: "VELO", Title: title, CycleID: c.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if got := qaCycles(t, st, "VELO")[0].IssueCount; got != 2 {
		t.Fatalf("setup: issue_count %d", got)
	}
	text, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Sprint 1", "status": "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"issue_count": 2`) {
		t.Errorf("update_cycle reports wrong issue_count (the cycle has 2 issues):\n%s", text)
	}
}
