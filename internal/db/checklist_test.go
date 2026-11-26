package db

import (
	"errors"
	"strings"
	"testing"
)

func TestSetChecklistItem(t *testing.T) {
	doc := strings.Join([]string{
		"Acceptance:",
		"- [ ] write tests",
		"  * [x] Write docs",
		"- [ ] write tests for the CLI",
		"```",
		"- [ ] not a task (code)",
		"```",
		"- plain bullet",
	}, "\n")
	line := func(s string, i int) string { return strings.Split(s, "\n")[i] }

	cases := []struct {
		name     string
		text     string
		index    int
		checked  bool
		wantLine int
		want     string
		wantErr  bool
	}{
		{"exact text wins over partial", "Write Tests", 0, true, 1, "- [x] write tests", false},
		{"partial unique", "for the cli", 0, true, 3, "- [x] write tests for the CLI", false},
		{"uncheck keeps indent and bullet", "docs", 0, false, 2, "  * [ ] Write docs", false},
		{"by index", "", 3, true, 3, "- [x] write tests for the CLI", false},
		{"ambiguous partial", "write", 0, true, 0, "", true},
		{"no match", "deploy", 0, true, 0, "", true},
		{"index out of range", "", 4, true, 0, "", true}, // the code-fence line is not an item
		{"nothing given", "", 0, true, 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := SetChecklistItem(doc, c.text, c.index, c.checked)
			if c.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("want validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := line(out, c.wantLine); got != c.want {
				t.Fatalf("line %d = %q, want %q", c.wantLine, got, c.want)
			}
			if line(out, 5) != "- [ ] not a task (code)" {
				t.Fatal("changed a line inside a code block")
			}
		})
	}
	if _, _, err := SetChecklistItem("no tasks here", "x", 0, true); !errors.Is(err, ErrValidation) {
		t.Fatalf("no checklist: %v", err)
	}
}

func TestCheckItemActivity(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	st.CreateProject("DEV", "Dev", "")
	st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "x", Description: "- [ ] migrate\n- [ ] deploy"})
	iss, err := st.Via("Claude Code").CheckItem(admin.ID, "DEV-1", "deploy", 0, true)
	if err != nil || iss.Description != "- [ ] migrate\n- [x] deploy" {
		t.Fatalf("CheckItem: %v %q", err, iss.Description)
	}
	if _, err := st.CheckItem(admin.ID, "DEV-1", "deploy", 0, true); err != nil {
		t.Fatalf("checking again should be a no-op: %v", err)
	}
	acts, _ := st.ListActivities(iss.ID)
	if len(acts) != 2 || acts[1].NewValue != "checked: deploy" || acts[1].Via != "Claude Code" {
		t.Fatalf("activities: %+v", acts)
	}
}
