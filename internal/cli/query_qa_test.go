package cli

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

func qaQueryCLI(t *testing.T) *qaCycleCLI {
	t.Helper()
	e := newQACycleCLI(t)
	users, _ := e.st.ListUsers()
	me := users[0].ID
	for _, in := range []db.CreateIssueInput{
		{ProjectRef: "DEV", Title: "đăng nhập lỗi", AssigneeID: me, Priority: 4, CycleID: "Sprint 1"}, // DEV-1
		{ProjectRef: "DEV", Title: "login slow", Priority: 2, ParentRef: "DEV-1"},                     // DEV-2
		{ProjectRef: "DEV", Title: "docs", StateID: "Done", AssigneeID: me, Priority: 3},              // DEV-3
		{ProjectRef: "OPS", Title: "ops login", AssigneeID: me, Priority: 4},                          // OPS-1
	} {
		if _, err := e.st.CreateIssue(me, in); err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
	}
	return e
}

func qaIDs(t *testing.T, out string) string {
	t.Helper()
	var issues []db.Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		t.Fatalf("bad --json output: %v\n%s", err, out)
	}
	var ids []string
	for _, it := range issues {
		ids = append(ids, it.Identifier)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// QA: velo issue list --q / --query with --json, results and errors.
func TestQACLIIssueListQuery(t *testing.T) {
	e := qaQueryCLI(t)
	list := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"issue", "list", "--url", e.url, "--token", e.tok, "--json"}, args...))
		})
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--q", "assignee:me is:open"}, "DEV-1,OPS-1"},
		{[]string{"--query", "assignee:me is:open project:DEV"}, "DEV-1"},
		{[]string{"--project", "DEV", "--q", "has:parent"}, "DEV-2"},
		{[]string{"--q", `cycle:"sprint 1" dang nhap`}, "DEV-1"},
		{[]string{"--q", "priority:>=high is:closed"}, "DEV-3"},
		{[]string{"--q", "DEV-1 is:open"}, "DEV-1"},
		{[]string{"--q", "login no:parent"}, "OPS-1"},
		{[]string{"--assignee", "me", "--q", "project:OPS"}, "OPS-1"},
		{[]string{"--q", ""}, "DEV-1,DEV-2,DEV-3,OPS-1"},
	} {
		out, err := list(tc.args...)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		if got := qaIDs(t, out); got != tc.want {
			t.Errorf("%v: got [%s] want [%s]", tc.args, got, tc.want)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--q", "asignee:me"}, "unknown filter"},
		{[]string{"--q", "priority:>>3"}, "priority"},
		{[]string{"--q", `state:"In Progress`}, "unclosed quote"},
		{[]string{"--assignee", "me", "--q", "assignee:none"}, "both as a parameter"},
		{[]string{"--parent", "root", "--q", "has:parent"}, "both as a parameter"},
		{[]string{"--priority", "4", "--q", "priority:>=1"}, "both as a parameter"},
		{[]string{"--unblocked", "--q", "is:blocked"}, "cannot both hold"},
	} {
		out, err := list(tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error with %q, got %v (stdout %q)", tc.args, tc.want, err, out)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("%v: printed output despite the error: %q", tc.args, out)
		}
	}
}

// QA: velo search with filters prints the table; errors are returned (exit 1).
func TestQACLISearchQuery(t *testing.T) {
	e := qaQueryCLI(t)
	search := func(q string) (string, error) {
		return captureStdout(t, func() error {
			return Run([]string{"search", q, "--url", e.url, "--token", e.tok})
		})
	}
	out, err := search("assignee:me is:open login")
	if err != nil || !strings.Contains(out, "OPS-1") || strings.Contains(out, "DEV-") {
		t.Fatalf("search: %v\n%s", err, out)
	}
	out, err = search("đăng nhập priority:urgent")
	if err != nil || !strings.Contains(out, "DEV-1") || strings.Contains(out, "OPS-1") {
		t.Fatalf("vietnamese + filter: %v\n%s", err, out)
	}
	out, err = search("is:open")
	if err != nil || !strings.Contains(out, "DEV-1") || !strings.Contains(out, "DEV-2") || strings.Contains(out, "DEV-3") {
		t.Fatalf("filter-only search: %v\n%s", err, out)
	}
	for q, want := range map[string]string{
		"asignee:me":                "unknown filter",
		"fix: login":                "unknown filter",
		"is:open is:closed":         "cannot both hold",
		`"unclosed`:                 "unclosed quote",
		"priority:9":                "priority",
		"assignee:me assignee:none": "given twice",
	} {
		if _, err := search(q); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("search %q: want error with %q, got %v", q, want, err)
		}
	}
}
