package db

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func qaStateNames(t *testing.T, st *Store, projectID string) map[string]WorkflowState {
	t.Helper()
	states, err := st.ListWorkflowStates(projectID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]WorkflowState{}
	for _, s := range states {
		out[s.Name] = s
	}
	return out
}

// A project with its own states, exported and imported under another key,
// keeps every issue in a state of the same name and category.
func TestQAImportStatesRoundTrip(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	src, err := st.CreateProject("SRC", "Source", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ name, cat string }{{"QA", "started"}, {"Đang làm", "started"}, {"Won't do", "canceled"}, {"Shipped", "completed"}} {
		if _, err := st.CreateWorkflowState(src.ID, s.name, s.cat, "#abcdef", 0); err != nil {
			t.Fatal(err)
		}
	}
	for i, state := range []string{"QA", "Đang làm", "Won't do", "Shipped", "Todo", "Done"} {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "SRC", Title: fmt.Sprintf("issue %d", i), StateID: state}); err != nil {
			t.Fatalf("create in %s: %v", state, err)
		}
	}
	b, err := st.ExportProjectJSON("SRC")
	if err != nil {
		t.Fatal(err)
	}
	b.Project.Key, b.Project.Name = "DST", "Dest"
	proj, err := st.ImportProjectJSON(admin.ID, b)
	if err != nil {
		t.Fatalf("import of an exported project: %v", err)
	}
	if n := len(qaStateNames(t, st, proj.ID)); n != 10 {
		t.Fatalf("want 10 states (6 defaults + 4 custom), got %d", n)
	}
	imported, err := st.ListIssues(IssueFilter{ProjectRef: proj.ID})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Issue{}
	for _, iss := range imported {
		byTitle[iss.Title] = iss
	}
	for _, old := range b.Issues {
		iss := byTitle[old.Title]
		if iss.StateName != old.StateName || iss.StateCategory != old.StateCat {
			t.Errorf("%s: was %s/%s, imported as %s/%s", old.Identifier, old.StateName, old.StateCat, iss.StateName, iss.StateCategory)
		}
	}
}

// A project whose default state "In Review" was moved to the completed
// category: after export and import, its closed issues must stay closed.
// The import ignores the bundle's category for names the defaults already
// have, so the issue reopens (category "started").
func TestQAImportKeepsCategoryOfDefaultNamedState(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	src, err := st.CreateProject("SRC", "Source", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range qaStateNames(t, st, src.ID) {
		if s.Name == "In Review" {
			if _, err := st.UpdateWorkflowState(s.ID, "", "completed", "", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "SRC", Title: "closed", StateID: "In Review"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.ExportProjectJSON("SRC")
	if err != nil {
		t.Fatal(err)
	}
	if b.Issues[0].StateCat != "completed" {
		t.Fatalf("setup: exported category %q", b.Issues[0].StateCat)
	}
	b.Project.Key = "DST"
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatal(err)
	}
	iss, err := st.GetIssue("DST-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.StateCategory != "completed" {
		t.Fatalf("closed issue reopened by round trip: state %q has category %q, the bundle said %q", iss.StateName, iss.StateCategory, "completed")
	}
}

// Case variants: duplicate bundle states differing only by case make one
// state; default names in another case are not doubled; whitespace around
// names is trimmed; empty state means the default state.
func TestQAImportStateCaseAndSpaces(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		States: []WorkflowState{
			{Name: "  QA  ", Category: "started"},
			{Name: "qa", Category: "completed"},
			{Name: "DONE", Category: "completed"},
			{Name: "ỨNG DỤNG", Category: "backlog"},
		},
		Issues: []ExportIssue{
			{Identifier: "OLD-1", Title: "a", StateName: " qa "},
			{Identifier: "OLD-2", Title: "b", StateName: "Qa"},
			{Identifier: "OLD-3", Title: "c", StateName: "done"},
			{Identifier: "OLD-4", Title: "d", StateName: "ứng dụng"},
			{Identifier: "OLD-5", Title: "e", StateName: "   "},
			{Identifier: "OLD-6", Title: "f"},
		},
	}
	proj, err := st.ImportProjectJSON(admin.ID, b)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	names := qaStateNames(t, st, proj.ID)
	if len(names) != 8 {
		t.Fatalf("want 8 states, got %v", names)
	}
	if qa, ok := names["QA"]; !ok || qa.Category != "started" {
		t.Fatalf("want trimmed QA/started, got %+v", names)
	}
	for ident, want := range map[string]string{"IMP-1": "QA", "IMP-2": "QA", "IMP-3": "Done", "IMP-4": "ỨNG DỤNG", "IMP-5": "Todo", "IMP-6": "Todo"} {
		iss, err := st.GetIssue(ident)
		if err != nil {
			t.Fatal(err)
		}
		if iss.StateName != want {
			t.Errorf("%s: want %q, got %q", ident, want, iss.StateName)
		}
	}
}

// The SPEC and the commit promise Unicode case-insensitive matching. Greek
// sigma has two lowercase forms (σ, final ς); strings.EqualFold treats
// "ΤΈΛΟΣ" and "Τέλος" as equal, strings.ToLower does not.
func TestQAImportStateUnicodeFoldSigma(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if !strings.EqualFold("ΤΈΛΟΣ", "Τέλος") {
		t.Fatal("setup: EqualFold disagrees")
	}
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		States:  []WorkflowState{{Name: "Τέλος", Category: "completed"}},
		Issues:  []ExportIssue{{Identifier: "OLD-1", Title: "a", StateName: "ΤΈΛΟΣ"}},
	}
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatalf("names equal ignoring case were refused: %v", err)
	}
	iss, err := st.GetIssue("IMP-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.StateName != "Τέλος" {
		t.Fatalf("want Τέλος, got %q", iss.StateName)
	}
}

// Every bad issue is named, and nothing is created; states are checked even
// when a bundle state's name collides with a default (its category must be
// valid); a missing category is refused; a default state needs no bundle
// entry (old bundles without "states" import).
func TestQAImportStateValidation(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	for _, tc := range []struct {
		states []WorkflowState
		issues []ExportIssue
		want   []string
	}{
		{nil, []ExportIssue{{Identifier: "OLD-1", Title: "a", StateName: "X"}, {Identifier: "OLD-2", Title: "b", StateName: "Y\"z"}}, []string{`OLD-1: "X"`, `OLD-2: "Y\"z"`}},
		{[]WorkflowState{{Name: "Done", Category: "finished"}}, []ExportIssue{{Identifier: "OLD-1", Title: "a"}}, []string{`unknown category "finished"`}},
		{[]WorkflowState{{Name: "QA"}}, []ExportIssue{{Identifier: "OLD-1", Title: "a"}}, []string{`unknown category ""`}},
		{[]WorkflowState{{Name: "QA", Category: "Started"}}, []ExportIssue{{Identifier: "OLD-1", Title: "a"}}, []string{`unknown category "Started"`}},
		{[]WorkflowState{{Name: "\t\n", Category: "started"}}, nil, []string{"no name"}},
	} {
		b := &ExportBundle{Project: Project{Key: "IMP", Name: "Imported"}, States: tc.states, Issues: tc.issues}
		_, err := st.ImportProjectJSON(admin.ID, b)
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("%+v: want validation error, got %v", tc, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error %q does not contain %q", err, w)
			}
		}
		if _, err := st.GetProject("IMP"); err == nil {
			t.Fatalf("%v: refused import created the project", tc.want)
		}
	}
	// Bundle without states, issues in default states only.
	b := &ExportBundle{Project: Project{Key: "OLD", Name: "Old"}, Issues: []ExportIssue{
		{Identifier: "X-1", Title: "a", StateName: "In Progress"}, {Identifier: "X-2", Title: "b", StateName: "canceled"},
	}}
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatalf("bundle without states: %v", err)
	}
	if iss, _ := st.GetIssue("OLD-2"); iss == nil || iss.StateName != "Canceled" {
		t.Fatalf("OLD-2: %+v", iss)
	}
}

// Bundle positions are kept, and each issue lands in its own state.
func TestQAImportStatePositions(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		States:  []WorkflowState{{Name: "A", Category: "started", Position: 100}, {Name: "B", Category: "completed", Position: 200}},
		Issues:  []ExportIssue{{Identifier: "OLD-1", Title: "a", StateName: "A"}, {Identifier: "OLD-2", Title: "b", StateName: "B"}},
	}
	proj, err := st.ImportProjectJSON(admin.ID, b)
	if err != nil {
		t.Fatal(err)
	}
	names := qaStateNames(t, st, proj.ID)
	if names["A"].Position != 100 || names["B"].Position != 200 {
		t.Fatalf("positions not kept: %+v", names)
	}
	for ident, want := range map[string]string{"IMP-1": "A", "IMP-2": "B"} {
		if iss, _ := st.GetIssue(ident); iss == nil || iss.StateName != want {
			t.Errorf("%s: want %s, got %+v", ident, want, iss)
		}
	}
}

// Parallel imports: the same key gives one project with one set of states;
// different keys keep their states apart.
func TestQAImportStatesParallel(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	bundle := func(key string) *ExportBundle {
		return &ExportBundle{
			Project: Project{Key: key, Name: key},
			States:  []WorkflowState{{Name: "QA-" + key, Category: "started"}},
			Issues:  []ExportIssue{{Identifier: "OLD-1", Title: "a", StateName: "QA-" + key}},
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "SAME"
			if i%2 == 1 {
				key = fmt.Sprintf("K%d", i)
			}
			_, errs[i] = st.ImportProjectJSON(admin.ID, bundle(key))
		}(i)
	}
	wg.Wait()
	ok := 0
	for i, err := range errs {
		if i%2 == 1 && err != nil {
			t.Errorf("import K%d: %v", i, err)
		}
		if i%2 == 0 && err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("same key: %d imports succeeded, want 1 (%v)", ok, errs)
	}
	for _, key := range []string{"SAME", "K1", "K3", "K5", "K7"} {
		p, err := st.GetProject(key)
		if err != nil {
			t.Fatal(err)
		}
		names := qaStateNames(t, st, p.ID)
		if len(names) != 7 {
			t.Errorf("%s: want 7 states, got %v", key, names)
		}
		if _, ok := names["QA-"+key]; !ok {
			t.Errorf("%s: missing its own state: %v", key, names)
		}
		if iss, _ := st.GetIssue(key + "-1"); iss == nil || iss.StateName != "QA-"+key {
			t.Errorf("%s-1: %+v", key, iss)
		}
	}
}
