package importer

import (
	"os"
	"strings"
	"testing"
	"time"
)

// #16: a Linear CSV export becomes a batch: the state from the Completed,
// Canceled and Started dates or the status name, Linear's priorities,
// comma-separated labels, cycles with their dates, parents, and assignees
// by email or --map-user.
func TestLinearBatch(t *testing.T) {
	f, err := os.Open("testdata/linear/export.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := &LinearCSV{Workspace: "acme", Users: map[string]string{"nguyễn văn a": "a@velo.dev"}}
	b, warnings, err := l.Batch(f)
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "linear" || len(b.Issues) != 5 {
		t.Fatalf("source %q, %d issues", b.Source, len(b.Issues))
	}
	one, two, three, four, five := b.Issues[0], b.Issues[1], b.Issues[2], b.Issues[3], b.Issues[4]
	if one.ExternalID != "ENG-1" || one.URL != "https://linear.app/acme/issue/ENG-1" || one.Title != "Login fails on Safari" ||
		one.Description != "Steps:\n- open Safari" || one.State != "Done" || one.StateCategory != "completed" || one.Priority != 3 ||
		strings.Join(one.Labels, ",") != "Bug,Lỗi" || one.Cycle != "Sprint 4" || one.Assignee != "bob@acme.dev" || one.Author != "alice@acme.dev" ||
		!one.CreatedAt.Equal(time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ENG-1: %+v", one)
	}
	if two.StateCategory != "started" || two.Assignee != "a@velo.dev" || two.Priority != 2 {
		t.Fatalf("ENG-2: %+v", two)
	}
	if three.StateCategory != "canceled" || three.Priority != 0 {
		t.Fatalf("ENG-3: %+v", three)
	}
	if four.Parent != "ENG-1" || four.Priority != 4 || four.StateCategory != "unstarted" || four.Cycle != "Cycle 5" ||
		!four.CreatedAt.Equal(time.Date(2024, 3, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ENG-4: %+v", four)
	}
	if five.StateCategory != "backlog" || five.Parent != "" || !five.CreatedAt.Equal(time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ENG-5: %+v", five)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ENG-5: parent OPS-9") {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(b.Cycles) != 2 || b.Cycles[0].Name != "Sprint 4" || b.Cycles[0].StartDate != "2024-03-01" || b.Cycles[0].EndDate != "2024-03-14" || b.Cycles[0].Status != "completed" {
		t.Fatalf("cycles: %+v", b.Cycles)
	}
	if _, _, err := (&LinearCSV{}).Batch(strings.NewReader("Title,Status\nx,Todo\n")); err == nil || !strings.Contains(err.Error(), `no "ID" column`) {
		t.Fatalf("missing ID column: %v", err)
	}
}

// Review findings: numeric priorities (Linear's API scale, 0 none to 4 low)
// map onto Velo's; unknown ones and unreadable dates are reported; cycles of
// different teams that share a name stay apart.
func TestLinearReviewFindings(t *testing.T) {
	csv := "ID,Team,Title,Status,Priority,Created,Cycle Name\n" +
		"ENG-1,Engineering,a,Todo,1,someday,Cycle 5\n" +
		"OPS-1,Ops,b,Todo,4,2024-03-02T10:00:00.000Z,Cycle 5\n" +
		"OPS-2,Ops,c,Todo,P2,2024-03-02T10:00:00.000Z,\n"
	b, warnings, err := (&LinearCSV{}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if b.Issues[0].Priority != 4 || b.Issues[1].Priority != 1 || b.Issues[2].Priority != 0 {
		t.Fatalf("priorities: %d %d %d", b.Issues[0].Priority, b.Issues[1].Priority, b.Issues[2].Priority)
	}
	if b.Issues[0].Cycle != "Engineering / Cycle 5" || b.Issues[1].Cycle != "Ops / Cycle 5" || len(b.Cycles) != 2 {
		t.Fatalf("cycles: %q %q %+v", b.Issues[0].Cycle, b.Issues[1].Cycle, b.Cycles)
	}
	all := strings.Join(warnings, "\n")
	for _, want := range []string{`Created`, `"someday"`, `unknown priorities: "P2"`} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings lack %q:\n%s", want, all)
		}
	}
}
