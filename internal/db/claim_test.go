package db

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestNextIssueOrderAndFilters(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	st.CreateProject("DEV", "Dev", "")
	mk := func(title string, prio int, labels ...string) {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: title, Priority: prio, LabelIDs: labels}); err != nil {
			t.Fatal(err)
		}
	}
	mk("no priority", 0)        // DEV-1
	mk("medium", 2)             // DEV-2
	mk("urgent but blocked", 4) // DEV-3
	mk("high", 3, "bug")        // DEV-4
	mk("blocker", 1)            // DEV-5
	st.LinkIssues(admin.ID, "DEV-5", "blocks", "DEV-3")

	next := func(f IssueFilter) string {
		t.Helper()
		iss, err := st.NextIssue(f)
		if err != nil {
			t.Fatal(err)
		}
		if iss == nil {
			return ""
		}
		return iss.Identifier
	}
	if got := next(IssueFilter{}); got != "DEV-4" {
		t.Fatalf("next = %s, want DEV-4 (highest unblocked)", got)
	}
	if got := next(IssueFilter{LabelID: "feature"}); got != "" {
		t.Fatalf("label filter = %s, want none", got)
	}
	// Claimed issues leave the queue; "no priority" comes after low.
	for _, want := range []string{"DEV-4", "DEV-2", "DEV-5", "DEV-3", "DEV-1", ""} {
		got := next(IssueFilter{ProjectRef: "DEV"})
		if got != want {
			t.Fatalf("next = %q, want %q", got, want)
		}
		if got == "" {
			break
		}
		iss, err := st.ClaimIssue(admin.ID, got)
		if err != nil {
			t.Fatalf("claim %s: %v", got, err)
		}
		if iss.StateName != "In Progress" || iss.AssigneeID != admin.ID {
			t.Fatalf("claimed %s: state %s assignee %s", got, iss.StateName, iss.AssigneeName)
		}
		if got == "DEV-5" { // finishing the blocker frees DEV-3
			done := "Done"
			st.UpdateIssue(admin.ID, "DEV-5", UpdateIssueInput{StateID: &done})
		}
	}
}

func TestClaimIssueRules(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	other, _ := st.CreateUserDirect("other@velo.local", "Other", "secret123", "member")
	st.CreateProject("DEV", "Dev", "")
	for i := 0; i < 3; i++ {
		st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "x"})
	}
	if _, err := st.ClaimIssue(admin.ID, "DEV-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimIssue(admin.ID, "DEV-1"); err != nil {
		t.Fatalf("re-claiming your own issue: %v", err)
	}
	if _, err := st.ClaimIssue(other.ID, "DEV-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim held issue: %v", err)
	}
	done := "Done"
	st.UpdateIssue(admin.ID, "DEV-2", UpdateIssueInput{StateID: &done})
	if _, err := st.ClaimIssue(other.ID, "DEV-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim finished issue: %v", err)
	}

	// Many claimants at once: exactly one wins. Users are created first and a
	// shared start signal releases all claims together.
	var ids []string
	for i := 0; i < 10; i++ {
		u, err := st.CreateUserDirect(fmt.Sprintf("agent%d@velo.local", i), "Agent", "secret123", "member")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	start := make(chan struct{})
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			if _, err := st.ClaimIssue(id, "DEV-3"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected claim error: %v", err)
			}
		}(id)
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims succeeded, want exactly 1", wins)
	}
}
