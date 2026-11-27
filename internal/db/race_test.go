package db

import (
	"errors"
	"sync"
	"testing"
)

// Known gap 1: updates must read the current row inside their transaction,
// so concurrent changes to one issue never undo each other.

func raceStore(t *testing.T) (*Store, *User) {
	t.Helper()
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	return st, admin
}

func TestConcurrentPatchesKeepEachLastValue(t *testing.T) {
	st, admin := raceStore(t)
	states := []string{"Backlog", "Todo", "In Progress", "In Review", "Done"}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s := states[i%len(states)]
			if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{StateID: &s}); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			p := i % 5
			if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Priority: &p}); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	iss, _ := st.GetIssue("DEV-1")
	if iss.StateName != states[49%len(states)] || iss.Priority != 49%5 {
		t.Fatalf("final state %q priority %d; want %q and %d (one patch undid the other)",
			iss.StateName, iss.Priority, states[49%len(states)], 49%5)
	}
}

func TestCheckItemAndDescriptionPatchAreSerializable(t *testing.T) {
	st, admin := raceStore(t)
	const d0 = "- [ ] a\n- [ ] b"
	const dp = "- [ ] a\n- [ ] b\n- [ ] c"
	// Same field, so last write wins; but each write must see the other's
	// result, so the outcome is one of the two serial orders.
	valid := map[string]bool{
		dp:                          true, // check, then patch
		"- [x] a\n- [ ] b\n- [ ] c": true, // patch, then check
	}
	for round := 0; round < 100; round++ {
		reset := d0
		st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Description: &reset})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := st.CheckItem(admin.ID, "DEV-1", "a", 0, true); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			d := dp
			if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Description: &d}); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if iss, _ := st.GetIssue("DEV-1"); !valid[iss.Description] {
			t.Fatalf("round %d: description %q is not a serial outcome (a write was based on stale data)", round, iss.Description)
		}
	}
}

func TestTitlePatchKeepsConcurrentTick(t *testing.T) {
	st, admin := raceStore(t)
	for round := 0; round < 100; round++ {
		reset, title := "- [ ] a", "x"
		st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Description: &reset, Title: &title})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			st.CheckItem(admin.ID, "DEV-1", "a", 0, true)
		}()
		go func() {
			defer wg.Done()
			renamed := "renamed"
			st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Title: &renamed})
		}()
		wg.Wait()
		if iss, _ := st.GetIssue("DEV-1"); iss.Description != "- [x] a" || iss.Title != "renamed" {
			t.Fatalf("round %d: title %q description %q; a rename undid the tick or the reverse", round, iss.Title, iss.Description)
		}
	}
}

func TestClaimLosesToConcurrentManualAssignment(t *testing.T) {
	st, admin := raceStore(t)
	agent, _ := st.CreateUserDirect("agent@velo.local", "Agent", "secret123", "member")
	person, _ := st.CreateUserDirect("person@velo.local", "Person", "secret123", "member")
	conflicts := 0
	for round := 0; round < 200; round++ {
		none, todo := "", "Todo"
		st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{AssigneeID: &none, StateID: &todo})
		var wg sync.WaitGroup
		var claimErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, claimErr = st.ClaimIssue(agent.ID, "DEV-1")
		}()
		go func() {
			defer wg.Done()
			p := person.ID
			if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{AssigneeID: &p}); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		// Serial orders: claim then assign (claim ok, person wins) or assign
		// then claim (409). Either way the manual assignment stands.
		iss, _ := st.GetIssue("DEV-1")
		if iss.AssigneeID != person.ID {
			t.Fatalf("round %d: claim (err=%v) overwrote a concurrent manual assignment; assignee is %s", round, claimErr, iss.AssigneeName)
		}
		if claimErr != nil {
			if !errors.Is(claimErr, ErrConflict) {
				t.Fatalf("round %d: claim error %v, want ErrConflict", round, claimErr)
			}
			conflicts++
		}
	}
	t.Logf("%d of 200 claims came second and got a conflict", conflicts)
}
