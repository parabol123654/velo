package db

import (
	"errors"
	"testing"
)

func TestIssueLinks(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	st.CreateProject("DEV", "Dev", "")
	st.CreateProject("OPS", "Ops", "")
	for _, p := range []string{"DEV", "DEV", "DEV", "OPS"} {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: p, Title: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	blocked := func(ref string) int {
		t.Helper()
		iss, err := st.GetIssue(ref)
		if err != nil {
			t.Fatal(err)
		}
		return iss.BlockedByCount
	}

	// DEV-2 blocked_by DEV-1 is stored as DEV-1 blocks DEV-2.
	if _, _, err := st.LinkIssues(admin.ID, "DEV-2", "blocked_by", "DEV-1"); err != nil {
		t.Fatal(err)
	}
	if blocked("DEV-2") != 1 || blocked("DEV-1") != 0 {
		t.Fatalf("blocked counts: DEV-1=%d DEV-2=%d", blocked("DEV-1"), blocked("DEV-2"))
	}
	one, _ := st.GetIssue("DEV-1")
	two, _ := st.GetIssue("DEV-2")
	if len(one.Links) != 1 || one.Links[0].Kind != "blocks" || one.Links[0].Issue.Identifier != "DEV-2" {
		t.Fatalf("DEV-1 links: %+v", one.Links)
	}
	if len(two.Links) != 1 || two.Links[0].Kind != "blocked_by" {
		t.Fatalf("DEV-2 links: %+v", two.Links)
	}

	// The unblocked filter skips DEV-2 until DEV-1 is done.
	open, _ := st.ListIssues(IssueFilter{ProjectRef: "DEV", Unblocked: true})
	for _, it := range open {
		if it.Identifier == "DEV-2" {
			t.Fatal("blocked issue listed as unblocked")
		}
	}
	done := "Done"
	if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{StateID: &done}); err != nil {
		t.Fatal(err)
	}
	if blocked("DEV-2") != 0 {
		t.Fatal("finished blocker still counts")
	}

	// Rejected links.
	for _, c := range []struct {
		ref, kind, target string
		want              error
	}{
		{"DEV-2", "blocks", "DEV-1", ErrConflict},    // reverse of an existing blocks
		{"DEV-1", "blocks", "DEV-2", ErrConflict},    // duplicate
		{"DEV-1", "blocks", "DEV-1", ErrValidation},  // self
		{"DEV-1", "relates", "OPS-1", ErrValidation}, // other project
		{"DEV-1", "follows", "DEV-3", ErrValidation}, // unknown kind
	} {
		if _, _, err := st.LinkIssues(admin.ID, c.ref, c.kind, c.target); !errors.Is(err, c.want) {
			t.Fatalf("%s %s %s: got %v, want %v", c.ref, c.kind, c.target, err, c.want)
		}
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-1", "relates", "DEV-3"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-3", "relates", "DEV-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("relates is symmetric, reverse should conflict: %v", err)
	}

	// Unlink works from either side; deleting an issue drops its links.
	if _, _, err := st.UnlinkIssues(admin.ID, "DEV-3", "DEV-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UnlinkIssues(admin.ID, "DEV-3", "DEV-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlink twice: %v", err)
	}
	if err := st.DeleteIssue("DEV-1"); err != nil {
		t.Fatal(err)
	}
	if two, _ := st.GetIssue("DEV-2"); len(two.Links) != 0 {
		t.Fatalf("links survived delete: %+v", two.Links)
	}
	acts, _ := st.ListActivities(two.ID)
	if last := acts[len(acts)-1]; last.Field != "link" || last.NewValue != "blocked_by DEV-1" {
		t.Fatalf("link activity: %+v", last)
	}
}
