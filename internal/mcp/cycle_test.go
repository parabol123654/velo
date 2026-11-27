package mcp

import (
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// #2: update_cycle changes only the fields it is given.
func TestMCPUpdateCycleIsPartial(t *testing.T) {
	api, st := newMCPTestAPI(t)
	p, _ := st.GetProject("VELO")
	if _, err := st.CreateCycle(p.ID, "Sprint 1", "Ship search", "2026-12-01", "2026-12-14", "upcoming"); err != nil {
		t.Fatal(err)
	}

	text, err := dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "sprint 1", "status": "active"})
	if err != nil {
		t.Fatalf("update_cycle: %v", err)
	}
	for _, want := range []string{`"status": "active"`, `"description": "Ship search"`, `"start_date": "2026-12-01"`, `"end_date": "2026-12-14"`, `"name": "Sprint 1"`} {
		if !strings.Contains(text, want) {
			t.Errorf("after a status-only update, missing %s in:\n%s", want, text)
		}
	}
	// Counts are filled in, as in list responses.
	if !strings.Contains(text, `"issue_count": 0`) {
		t.Errorf("no issue_count in:\n%s", text)
	}
	// The stored cycle agrees with the response.
	cycles, _ := st.ListCycles(p.ID)
	if len(cycles) != 1 || cycles[0].Status != "active" || cycles[0].Description != "Ship search" || cycles[0].StartDate != "2026-12-01" || cycles[0].EndDate != "2026-12-14" {
		t.Fatalf("stored: %+v", cycles)
	}

	// Counts reflect the cycle's issues.
	if _, err := st.CreateIssue(cyclesOwner(t, st), db.CreateIssueInput{ProjectRef: "VELO", Title: "x", CycleID: "Sprint 1"}); err != nil {
		t.Fatal(err)
	}
	text, _ = dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": "Sprint 1", "status": "active"})
	if !strings.Contains(text, `"issue_count": 1`) {
		t.Errorf("issue_count after adding an issue:\n%s", text)
	}
	// Rename and clear the description; by ID this time.
	text, err = dispatchTool(api, "update_cycle", map[string]any{"project": "VELO", "cycle": cycles[0].ID, "name": "Sprint 1b", "description": ""})
	if err != nil || !strings.Contains(text, `"name": "Sprint 1b"`) || strings.Contains(text, "Ship search") || !strings.Contains(text, `"status": "active"`) {
		t.Fatalf("rename by ID: %v\n%s", err, text)
	}

	// Errors name the problem.
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"project": "VELO", "cycle": "Sprint 9", "status": "active"}, "Sprint 9"},
		{map[string]any{"project": "VELO", "cycle": "Sprint 1b", "status": "done"}, "status"},
		{map[string]any{"project": "VELO", "cycle": "Sprint 1b", "name": " "}, "name"},
	} {
		if _, err := dispatchTool(api, "update_cycle", tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want an error mentioning %q, got %v", tc.args, tc.want, err)
		}
	}
}

func cyclesOwner(t *testing.T, st *db.Store) string {
	t.Helper()
	users, err := st.ListUsers()
	if err != nil || len(users) == 0 {
		t.Fatal(err)
	}
	return users[0].ID
}
