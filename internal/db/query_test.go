package db

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

// #17: the issue query language, key by key.
func TestParseIssueQuery(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want IssueFilter
	}{
		{"", IssueFilter{}},
		{"login bug", IssueFilter{Query: "login bug"}},
		{`"fix: login"`, IssueFilter{Query: `"fix: login"`}},
		{"assignee:me", IssueFilter{AssigneeID: "me"}},
		{"assignee:none", IssueFilter{AssigneeID: "none"}},
		{"assignee:dev@velo.dev", IssueFilter{AssigneeID: "dev@velo.dev"}},
		{`state:"In Progress"`, IssueFilter{StateID: "In Progress"}},
		{"state:Todo", IssueFilter{StateID: "Todo"}},
		{"label:bug", IssueFilter{Labels: []string{"bug"}}},
		{"label:bug label:infra", IssueFilter{Labels: []string{"bug", "infra"}}},
		{"priority:3", IssueFilter{Priority: intp(3), PriorityOp: "="}},
		{"priority:>=3", IssueFilter{Priority: intp(3), PriorityOp: ">="}},
		{"priority:<2", IssueFilter{Priority: intp(2), PriorityOp: "<"}},
		{"priority:high", IssueFilter{Priority: intp(3), PriorityOp: "="}},
		{"priority:>=Urgent", IssueFilter{Priority: intp(4), PriorityOp: ">="}},
		{"priority:none", IssueFilter{Priority: intp(0), PriorityOp: "="}},
		{`cycle:"Sprint 1"`, IssueFilter{CycleID: "Sprint 1"}},
		{"cycle:none", IssueFilter{CycleID: "none"}},
		{"cycle:NONE assignee:None", IssueFilter{CycleID: "none", AssigneeID: "none"}},
		{"project:DEV", IssueFilter{ProjectRef: "DEV"}},
		{"parent:DEV-1", IssueFilter{ParentID: "DEV-1"}},
		{"has:parent", IssueFilter{ParentID: "any"}},
		{"no:parent", IssueFilter{ParentID: "root"}},
		{"is:open", IssueFilter{Status: "open"}},
		{"is:closed", IssueFilter{Status: "closed"}},
		{"is:blocked", IssueFilter{Blocked: true}},
		{"is:unblocked", IssueFilter{Unblocked: true}},
		{"is:open is:unblocked", IssueFilter{Status: "open", Unblocked: true}},
		{"ASSIGNEE:me IS:Open", IssueFilter{AssigneeID: "me", Status: "open"}},
		{`assignee:me state:"In Progress" label:bug priority:>=3 cycle:"Sprint 1" is:open has:parent crash on login`,
			IssueFilter{AssigneeID: "me", StateID: "In Progress", Labels: []string{"bug"}, Priority: intp(3), PriorityOp: ">=",
				CycleID: "Sprint 1", Status: "open", ParentID: "any", Query: "crash on login"}},
		{"10:30 DEV-1", IssueFilter{Query: "10:30 DEV-1"}}, // not a key: does not start with a letter
		{`  spaced   out  `, IssueFilter{Query: "spaced out"}},
		{"see http://example.com/x", IssueFilter{Query: "see http://example.com/x"}}, // a URL is text
		{"lỗi: đăng nhập", IssueFilter{Query: "lỗi: đăng nhập"}},                     // keys are ASCII
		{"đ:đ", IssueFilter{Query: "đ:đ"}},
	} {
		got, err := ParseIssueQuery(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseIssueQueryErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"asignee:me", `unknown filter "asignee:"`},
		{"fix: login", `unknown filter "fix:"`},
		{`state:"In Progress`, "unclosed quote"},
		{`crash "on login`, "unclosed quote"},
		{"assignee:", `"assignee:" needs a value`},
		{`state:""`, `"state:" needs a value`},
		{"priority:9", "priority"},
		{"priority:>=", "priority"},
		{"priority:hgih", "priority"},
		{"is:done", `is: takes open, closed, blocked or unblocked`},
		{"is:open is:closed", "is:open and is:closed"},
		{"is:blocked is:unblocked", "is:blocked and is:unblocked"},
		{"has:assignee", "has: takes parent"},
		{"no:label", "no: takes parent"},
		{"assignee:me assignee:none", `"assignee:" given twice`},
		{"has:parent no:parent", `"parent" given twice`},
		{strings.Repeat("a", 1001), "longer than 1000 bytes"},
		{strings.Repeat("label:x ", 51), "more than 50 words"},
		{"parent:DEV-1 has:parent", `"parent" given twice`},
		{"priority:1 priority:>=3", `"priority:" given twice`},
	} {
		_, err := ParseIssueQuery(tc.in)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: want a validation error containing %q, got %v", tc.in, tc.want, err)
		}
	}
	// The unknown-key error lists the keys an agent can use.
	_, err := ParseIssueQuery("asignee:me")
	for _, k := range []string{"assignee", "state", "label", "priority", "cycle", "project", "parent", "is", "has", "no"} {
		if err == nil || !strings.Contains(err.Error(), k+":") {
			t.Errorf("unknown-key error does not list %s: %v", k, err)
		}
	}
}

// Merging the parsed query into filters from separate parameters: both may
// be used, but not for the same field.
func TestMergeIssueFilter(t *testing.T) {
	base := IssueFilter{ProjectRef: "DEV", Limit: 10}
	q, _ := ParseIssueQuery("assignee:me label:bug crash")
	if err := base.Merge(q); err != nil {
		t.Fatal(err)
	}
	if base.ProjectRef != "DEV" || base.AssigneeID != "me" || len(base.Labels) != 1 || base.Query != "crash" || base.Limit != 10 {
		t.Fatalf("merged: %+v", base)
	}
	for _, tc := range []struct {
		base IssueFilter
		q    string
	}{
		{IssueFilter{StateID: "Todo"}, "state:Done"},
		{IssueFilter{ProjectRef: "DEV"}, "project:OPS"},
		{IssueFilter{Priority: intp(1)}, "priority:>=3"},
		{IssueFilter{Unblocked: true}, "is:blocked"},
		{IssueFilter{ParentID: "root"}, "has:parent"},
	} {
		q, err := ParseIssueQuery(tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if err := tc.base.Merge(q); err == nil || !errors.Is(err, ErrValidation) {
			t.Errorf("%+v + %q: want a conflict error, got %v", tc.base, tc.q, err)
		}
	}
}

// The filters the query adds reach SQL correctly.
func TestIssueQueryFiltersInSQL(t *testing.T) {
	st, admin := viStore(t)
	mk := func(title string, in CreateIssueInput) *Issue {
		in.ProjectRef, in.Title = "DEV", title
		iss, err := st.CreateIssue(admin.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		return iss
	}
	mk("blocker", CreateIssueInput{LabelIDs: []string{"bug", "feature"}})
	mk("waiting", CreateIssueInput{LabelIDs: []string{"bug"}, ParentRef: "DEV-1"})
	mk("done", CreateIssueInput{StateID: "Done"})
	mk("dropped", CreateIssueInput{StateID: "Canceled"})
	if _, _, err := st.LinkIssues(admin.ID, "DEV-1", "blocks", "DEV-2"); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		"is:blocked":                  "waiting",
		"is:unblocked is:open":        "blocker",
		"has:parent":                  "waiting",
		"no:parent is:open":           "blocker",
		"label:bug label:feature":     "blocker",
		"label:BUG":                   "blocker,waiting",
		"is:closed":                   "done,dropped",
		"is:open":                     "blocker,waiting",
		"label:bug is:open no:parent": "blocker",
	} {
		f, err := ParseIssueQuery(q)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		res, err := st.ListIssues(f)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var got []string
		for _, it := range res {
			got = append(got, it.Title)
		}
		sort.Strings(got)
		w := strings.Split(want, ",")
		sort.Strings(w)
		if strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%q: got %v, want %v", q, got, w)
		}
	}
	// Only the five comparisons reach SQL; anything else is refused.
	n := 1
	if _, err := st.ListIssues(IssueFilter{Priority: &n, PriorityOp: "= 1 OR 1=1 --"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("injected PriorityOp: %v", err)
	}
	if _, err := st.SearchIssues("assignee:me", 10, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("assignee:me without a user: %v", err)
	}
}
