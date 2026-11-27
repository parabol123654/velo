package db

import (
	"errors"
	"strings"
	"testing"
)

// An issue label that the bundle does not define fails the import before
// anything is created, instead of leaving a half-imported project.
func TestImportRejectsUndefinedIssueLabels(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	b := &ExportBundle{
		Project: Project{Key: "IMP", Name: "Imported"},
		Labels:  []Label{{Name: "infra", Color: "#000000"}},
		Issues: []ExportIssue{
			{Identifier: "OLD-1", Title: "ok", Labels: []string{"INFRA", "bug"}},
			{Identifier: "OLD-2", Title: "bad", Labels: []string{"typo"}},
		},
	}
	_, err := st.ImportProjectJSON(admin.ID, b)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), `OLD-2: "typo"`) {
		t.Fatalf("want validation error naming OLD-2's label, got %v", err)
	}
	if _, err := st.GetProject("IMP"); err == nil {
		t.Fatal("refused import created the project")
	}
	b.Issues = b.Issues[:1]
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatalf("import with defined and default labels: %v", err)
	}
}

// A link that LinkIssues would refuse (unknown issue or kind, self-link,
// repeat, blocking loop) fails the import before anything is created.
func TestImportRejectsBadLinks(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	issues := []ExportIssue{{Identifier: "OLD-1", Title: "a"}, {Identifier: "OLD-2", Title: "b"}, {Identifier: "OLD-3", Title: "c"}}
	link := func(src, kind, dst string) ExportLink { return ExportLink{Source: src, Kind: kind, Target: dst} }
	for _, tc := range []struct {
		links []ExportLink
		want  string
	}{
		{[]ExportLink{link("OLD-1", "blocks", "OLD-9")}, "OLD-1 blocks OLD-9 names an issue"},
		{[]ExportLink{link("OLD-1", "follows", "OLD-2")}, "OLD-1 follows OLD-2 has an unknown kind"},
		{[]ExportLink{link("OLD-1", "blocks", "OLD-1")}, "OLD-1 blocks OLD-1 links an issue to itself"},
		{[]ExportLink{link("OLD-1", "relates", "OLD-2"), link("OLD-2", "relates", "OLD-1")}, "OLD-2 relates OLD-1 repeats"},
		{[]ExportLink{link("OLD-1", "blocks", "OLD-2"), link("OLD-2", "Blocked_By", "OLD-1")}, "OLD-2 Blocked_By OLD-1 repeats"},
		{[]ExportLink{link("OLD-1", "blocks", "OLD-2"), link("OLD-2", "blocks", "OLD-1")}, "OLD-2 blocks OLD-1 repeats"},
		{[]ExportLink{link("OLD-1", "blocks", "OLD-2"), link("OLD-2", "blocks", "OLD-3"), link("OLD-1", "blocked_by", "OLD-3")}, "OLD-1 → OLD-2 → OLD-3 → OLD-1"},
	} {
		b := &ExportBundle{Project: Project{Key: "IMP", Name: "Imported"}, Issues: issues, Links: tc.links}
		if _, err := st.ImportProjectJSON(admin.ID, b); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: want validation error containing %q, got %v", tc.links, tc.want, err)
		}
		if _, err := st.GetProject("IMP"); err == nil {
			t.Fatalf("%+v: refused import created the project", tc.links)
		}
	}
	// Kinds are matched like the API matches them: case and spaces do not matter.
	b := &ExportBundle{Project: Project{Key: "IMP", Name: "Imported"}, Issues: issues,
		Links: []ExportLink{link("OLD-1", " Blocks", "OLD-2"), link("OLD-3", "relates", "OLD-1")}}
	if _, err := st.ImportProjectJSON(admin.ID, b); err != nil {
		t.Fatalf("valid links: %v", err)
	}
}
