package db

import (
	"errors"
	"strings"
	"testing"
)

// A bundle exported from a project with its own workflow states imports
// those states, and its issues land in them.
func TestImportCreatesBundleStates(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		States: []WorkflowState{
			{Name: "Todo", Category: "unstarted", Position: 1000},
			{Name: "QA", Category: "started", Color: "#123456", Position: 3500},
			{Name: "Đang làm", Category: "started", Position: 3600},
		},
		Issues: []ExportIssue{
			{Identifier: "OLD-1", Title: "in QA", StateName: "QA"},
			{Identifier: "OLD-2", Title: "Vietnamese, other case", StateName: "ĐANG LÀM"},
			{Identifier: "OLD-3", Title: "default state", StateName: "done"},
		},
	}
	proj, err := st.ImportProjectJSON(admin.ID, b)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	states, err := st.ListWorkflowStates(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]WorkflowState{}
	for _, s := range states {
		byName[s.Name] = s
	}
	if qa, ok := byName["QA"]; !ok || qa.Category != "started" || qa.Color != "#123456" || qa.Position != 3500 {
		t.Fatalf("QA state not imported as in the bundle: %+v", byName["QA"])
	}
	if len(states) != 8 { // six defaults plus QA and Đang làm; Todo is not doubled
		t.Fatalf("want 8 states, got %d: %+v", len(states), states)
	}
	for ident, want := range map[string]string{"IMP-1": "QA", "IMP-2": "Đang làm", "IMP-3": "Done"} {
		iss, err := st.GetIssue(ident)
		if err != nil {
			t.Fatal(err)
		}
		if iss.StateName != want {
			t.Fatalf("%s: want state %q, got %q", ident, want, iss.StateName)
		}
	}
}

// An issue whose state is neither in the bundle nor a default state, or a
// bundle state with an unknown category, fails the import before anything
// is created, instead of leaving a half-imported project.
func TestImportRejectsUnknownStates(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	for _, tc := range []struct {
		states []WorkflowState
		issues []ExportIssue
		want   string
	}{
		{nil, []ExportIssue{{Identifier: "OLD-1", Title: "ok"}, {Identifier: "OLD-2", Title: "bad", StateName: "QA"}}, `OLD-2: "QA"`},
		{[]WorkflowState{{Name: "QA", Category: "review"}}, []ExportIssue{{Identifier: "OLD-1", Title: "a"}}, `state "QA" has an unknown category "review"`},
		{[]WorkflowState{{Name: " ", Category: "started"}}, []ExportIssue{{Identifier: "OLD-1", Title: "a"}}, "state with no name"},
	} {
		b := &ExportBundle{Project: Project{Key: "IMP", Name: "Imported"}, States: tc.states, Issues: tc.issues}
		_, err := st.ImportProjectJSON(admin.ID, b)
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want validation error containing %q, got %v", tc.want, err)
		}
		if _, err := st.GetProject("IMP"); err == nil {
			t.Fatalf("%q: refused import created the project", tc.want)
		}
	}
}

// Labels match like states: an issue's "LỖI" finds the bundle's "lỗi",
// where CreateIssue's own SQLite NOCASE match folds ASCII only.
func TestImportMatchesLabelsIgnoringUnicodeCase(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		Labels:  []Label{{Name: "lỗi", Color: "#000000"}, {Name: "LỖI", Color: "#ffffff"}},
		Issues:  []ExportIssue{{Identifier: "OLD-1", Title: "a", Labels: []string{"LỖI", "BUG"}}},
	}
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatalf("import: %v", err)
	}
	iss, err := st.GetIssue("IMP-1")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, l := range iss.Labels {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "bug,lỗi" {
		t.Fatalf("labels: %v", names)
	}
}
