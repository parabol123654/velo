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
