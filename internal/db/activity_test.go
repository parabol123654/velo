package db

import (
	"fmt"
	"strings"
	"testing"
)

// activityLog returns an issue's activities after the creation entry, as
// "field: old -> new" lines.
func activityLog(t *testing.T, st *Store, ref string) []string {
	t.Helper()
	iss, err := st.GetIssue(ref)
	if err != nil {
		t.Fatal(err)
	}
	acts, err := st.ListActivities(iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range acts {
		if a.Action != "created" {
			out = append(out, fmt.Sprintf("%s: %s -> %s", a.Field, a.OldValue, a.NewValue))
		}
	}
	return out
}

func TestEveryIssueChangeLogsOneActivity(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"parent", "child", "other parent"} {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: title, Description: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	str := func(s string) *string { return &s }
	labels := func(l ...string) *[]string { return &l }
	steps := []UpdateIssueInput{
		{Description: str("changed")},
		{Description: str("changed")}, // no-op
		{LabelIDs: labels("feature", "BUG")},
		{LabelIDs: labels("bug", "feature")}, // same set, other order: no-op
		{LabelIDs: labels("chore")},
		{ParentID: str("DEV-1")},
		{ParentID: str("dev-1")}, // no-op
		{ParentID: str("DEV-3")},
		{ParentID: str("")},
		{ParentID: str("")}, // no-op
	}
	for i, p := range steps {
		if _, err := st.UpdateIssue(admin.ID, "DEV-2", p); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := []string{
		"description:  -> ", // field only: the text is not copied into history
		"labels:  -> bug, feature",
		"labels: bug, feature -> chore",
		"parent:  -> DEV-1",
		"parent: DEV-1 -> DEV-3",
		"parent: DEV-3 -> ",
	}
	if got := activityLog(t, st, "DEV-2"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("activities:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLinkAndUnlinkAreLoggedOnBothIssues(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"a", "b"} {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-2", "blocked_by", "DEV-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-1", "relates", "DEV-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UnlinkIssues(admin.ID, "DEV-1", "DEV-2"); err != nil {
		t.Fatal(err)
	}
	// Each issue sees each link from its own side; an unlink names every
	// link it removed, with its kind (order of removal is by kind).
	for ref, want := range map[string][]string{
		"DEV-1": {"link:  -> blocks DEV-2", "link:  -> relates DEV-2", "link: blocks DEV-2 -> ", "link: relates DEV-2 -> "},
		"DEV-2": {"link:  -> blocked_by DEV-1", "link:  -> relates DEV-1", "link: blocked_by DEV-1 -> ", "link: relates DEV-1 -> "},
	} {
		if got := activityLog(t, st, ref); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s activities:\n%s\nwant:\n%s", ref, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}
