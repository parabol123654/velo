package mcp

import (
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// #17 Done when: list_issues accepts a query in the issue query syntax.
func TestMCPListIssuesQuerySyntax(t *testing.T) {
	api, st := newMCPTestAPI(t)
	me := cyclesOwner(t, st)
	for _, in := range []db.CreateIssueInput{
		{ProjectRef: "VELO", Title: "agent task", AssigneeID: me, LabelIDs: []string{"bug"}, Priority: 3},
		{ProjectRef: "VELO", Title: "someone else", LabelIDs: []string{"bug"}, Priority: 3},
		{ProjectRef: "VELO", Title: "finished", AssigneeID: me, StateID: "Done", LabelIDs: []string{"bug"}, Priority: 3},
	} {
		if _, err := st.CreateIssue(me, in); err != nil {
			t.Fatal(err)
		}
	}
	text, err := dispatchTool(api, "list_issues", map[string]any{"query": "assignee:me label:bug priority:>=high is:open"})
	if err != nil || !strings.Contains(text, "agent task") || strings.Contains(text, "someone else") || strings.Contains(text, "finished") {
		t.Fatalf("list_issues query: %v\n%s", err, text)
	}
	if _, err := dispatchTool(api, "list_issues", map[string]any{"query": "asignee:me"}); err == nil || !strings.Contains(err.Error(), "unknown filter") {
		t.Fatalf("typo in key: want an error naming it, got %v", err)
	}
	text, err = dispatchTool(api, "search_issues", map[string]any{"query": "label:bug task"})
	if err != nil || !strings.Contains(text, "agent task") || strings.Contains(text, "someone else") {
		t.Fatalf("search_issues query: %v\n%s", err, text)
	}
}
