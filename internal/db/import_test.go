package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func importFixture(t *testing.T) (*Store, *User, *Project) {
	t.Helper()
	st := newTestStore(t)
	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := st.CreateProject("DEV", "Dev", "")
	if err != nil {
		t.Fatal(err)
	}
	return st, admin, proj
}

func countRows(t *testing.T, st *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sampleBatch() *ImportBatch {
	opened := time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)
	return &ImportBatch{
		Source: "github",
		Cycles: []ImportCycle{{Name: "v1.0", EndDate: "2024-06-30", Status: "completed"}},
		Issues: []ImportIssue{
			{ExternalID: "o/r#1", URL: "https://github.com/o/r/issues/1", Title: "Parent", Author: "@alice",
				State: "Done", Labels: []string{"bug", "Lỗi"}, Cycle: "v1.0", CreatedAt: opened,
				Comments: []ImportComment{{ExternalID: "c1", Author: "@bob", Body: "First!", CreatedAt: opened.Add(time.Hour)}}},
			{ExternalID: "o/r#2", Title: "Child", Parent: "o/r#1", StateCategory: "canceled", Priority: 3,
				Labels: []string{"LỖI"}, Assignee: "ADMIN@velo.local"},
			{ExternalID: "o/r#3", Title: "Unknown assignee", Assignee: "@carol", Cycle: "Sprint 9"},
		},
		Links: []ImportLink{{Source: "o/r#1", Kind: "blocked_by", Target: "o/r#3"}},
	}
}

// A batch creates issues with new identifiers, the source key in a footer,
// states by name or category, labels and cycles it needs (Unicode case
// folded), parents, comments with their original author and date, and links.
func TestImportIssuesCreates(t *testing.T) {
	st, admin, proj := importFixture(t)
	res, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Created != 3 || res.Skipped != 0 || res.Comments != 1 || res.Links != 1 {
		t.Fatalf("unexpected counts: %+v", res)
	}
	if strings.Join(res.LabelsCreated, ",") != "Lỗi" || strings.Join(res.CyclesCreated, ",") != "v1.0,Sprint 9" {
		t.Fatalf("created labels %v, cycles %v", res.LabelsCreated, res.CyclesCreated)
	}
	if len(res.Unassigned) != 1 || res.Unassigned[0] != "o/r#3: @carol" {
		t.Fatalf("unassigned: %v", res.Unassigned)
	}
	want := map[string]string{"o/r#1": "DEV-1", "o/r#2": "DEV-2", "o/r#3": "DEV-3"}
	for _, it := range res.Issues {
		if want[it.ExternalID] != it.Identifier || !it.Created {
			t.Fatalf("issue %+v, want %s", it, want[it.ExternalID])
		}
	}

	p, _ := st.GetIssue("DEV-1")
	if p.StateName != "Done" || p.CycleName != "v1.0" || len(p.Labels) != 2 || !p.CreatedAt.Equal(time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("DEV-1: %+v", p)
	}
	if !strings.Contains(p.Description, "Imported from [o/r#1](https://github.com/o/r/issues/1), opened by @alice") {
		t.Fatalf("DEV-1 footer: %q", p.Description)
	}
	c, _ := st.GetIssue("DEV-2")
	if c.ParentIdentifier != "DEV-1" || c.StateName != "Canceled" || c.Priority != 3 || c.AssigneeName != "Admin" {
		t.Fatalf("DEV-2: %+v", c)
	}
	if len(c.Labels) != 1 || c.Labels[0].Name != "Lỗi" {
		t.Fatalf("DEV-2 labels: %+v (LỖI must reuse Lỗi)", c.Labels)
	}
	u, _ := st.GetIssue("DEV-3")
	if u.AssigneeID != "" || u.StateName != "Todo" || !strings.Contains(u.Description, "assigned to @carol") {
		t.Fatalf("DEV-3: %+v", u)
	}
	if p.BlockedByCount != 1 { // DEV-3, still open, blocks DEV-1
		t.Fatalf("DEV-1 blocked_by_count: %d", p.BlockedByCount)
	}
	comments, _ := st.ListComments(p.ID)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "@bob") || !strings.Contains(comments[0].Body, "First!") ||
		!comments[0].CreatedAt.Equal(time.Date(2024, 3, 2, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("comments: %+v", comments)
	}
	var end, status string
	if err := st.db.QueryRow(`SELECT end_date, status FROM cycles WHERE project_id = ? AND name = 'v1.0'`, proj.ID).Scan(&end, &status); err != nil || end != "2024-06-30" || status != "completed" {
		t.Fatalf("cycle v1.0: %q %q %v", end, status, err)
	}
}

// Running the same import again creates nothing; a later run adds only the
// issues and comments it has not seen, and leaves edited issues alone.
func TestImportIssuesRerun(t *testing.T) {
	st, admin, _ := importFixture(t)
	if _, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false); err != nil {
		t.Fatal(err)
	}
	title := "Edited in Velo"
	if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if res.Created != 0 || res.Skipped != 3 || res.Comments != 0 || res.Links != 0 {
		t.Fatalf("rerun counts: %+v", res)
	}
	b := sampleBatch()
	b.Issues[0].Comments = append(b.Issues[0].Comments, ImportComment{ExternalID: "c2", Body: "Later"})
	b.Issues = append(b.Issues, ImportIssue{ExternalID: "o/r#4", Title: "New", Parent: "o/r#1"})
	res, err = st.ImportIssues(admin.ID, "DEV", b, false)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if res.Created != 1 || res.Skipped != 3 || res.Comments != 1 {
		t.Fatalf("third run counts: %+v", res)
	}
	if iss, _ := st.GetIssue("DEV-1"); iss.Title != title {
		t.Fatalf("rerun overwrote an edit: %q", iss.Title)
	}
	if iss, _ := st.GetIssue("DEV-4"); iss.ParentIdentifier != "DEV-1" {
		t.Fatalf("DEV-4 parent from an earlier run: %+v", iss)
	}
	// The same external IDs from another source are other issues.
	b2 := sampleBatch()
	b2.Source = "jira"
	if res, err := st.ImportIssues(admin.ID, "DEV", b2, false); err != nil || res.Created != 3 {
		t.Fatalf("other source: %+v %v", res, err)
	}
}

// A dry run reports what would happen and writes nothing.
func TestImportIssuesDryRun(t *testing.T) {
	st, admin, proj := importFixture(t)
	res, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Created != 3 || res.Comments != 1 || len(res.LabelsCreated) != 1 {
		t.Fatalf("dry run result: %+v", res)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM issues WHERE project_id = ?`,
		`SELECT COUNT(*) FROM import_refs WHERE project_id = ?`,
		`SELECT COUNT(*) FROM cycles WHERE project_id = ?`,
	} {
		if n := countRows(t, st, q, proj.ID); n != 0 {
			t.Fatalf("dry run wrote rows: %s = %d", q, n)
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM labels WHERE project_id = ?`, proj.ID); n != 4 {
		t.Fatalf("dry run created labels: %d", n)
	}
	if p, _ := st.GetProject("DEV"); p.IssueSeq != 0 {
		t.Fatalf("dry run advanced the issue sequence to %d", p.IssueSeq)
	}
}

// Any problem refuses the whole batch with a 400 naming it, and nothing is
// written, not even the issues before the bad one.
func TestImportIssuesRejects(t *testing.T) {
	st, admin, proj := importFixture(t)
	if _, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "github", Issues: []ImportIssue{
		{ExternalID: "x#1", Title: "a"}, {ExternalID: "x#2", Title: "b"},
	}, Links: []ImportLink{{Source: "x#1", Kind: "blocks", Target: "x#2"}}}, false); err != nil {
		t.Fatal(err)
	}
	ok := func() ImportIssue { return ImportIssue{ExternalID: "o#1", Title: "fine"} }
	for _, tc := range []struct {
		name string
		b    ImportBatch
		want string
	}{
		{"no source", ImportBatch{Issues: []ImportIssue{ok()}}, "source is required"},
		{"no issues", ImportBatch{Source: "s"}, "no issues"},
		{"no external id", ImportBatch{Source: "s", Issues: []ImportIssue{ok(), {Title: "t"}}}, "issue 2 has no external_id"},
		{"repeated id", ImportBatch{Source: "s", Issues: []ImportIssue{ok(), ok()}}, "o#1 appears twice"},
		{"empty title", ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "o#1", Title: "  "}}}, "o#1: title is required"},
		{"priority", ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "o#1", Title: "t", Priority: 5}}}, "o#1: priority must be between 0 and 4"},
		{"state", ImportBatch{Source: "s", Issues: []ImportIssue{ok(), {ExternalID: "o#2", Title: "t", State: "QA"}}}, `o#2: state "QA" is not a state of DEV`},
		{"category", ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "o#1", Title: "t", StateCategory: "review"}}}, `o#1: unknown state category "review"`},
		{"parent", ImportBatch{Source: "s", Issues: []ImportIssue{ok(), {ExternalID: "o#2", Title: "t", Parent: "o#9"}}}, "o#2: parent o#9 is not in this import"},
		{"parent loop", ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "o#1", Title: "a", Parent: "o#2"}, {ExternalID: "o#2", Title: "b", Parent: "o#1"}}}, "parents form a loop"},
		{"comment body", ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "o#1", Title: "t", Comments: []ImportComment{{ExternalID: "c", Body: " "}}}}}, "o#1: comment c has no body"},
		{"link issue", ImportBatch{Source: "s", Issues: []ImportIssue{ok()}, Links: []ImportLink{{Source: "o#1", Kind: "blocks", Target: "o#7"}}}, "link o#1 blocks o#7: o#7 is not in this import"},
		{"link kind", ImportBatch{Source: "s", Issues: []ImportIssue{ok(), {ExternalID: "o#2", Title: "t"}}, Links: []ImportLink{{Source: "o#1", Kind: "follows", Target: "o#2"}}}, "unknown kind"},
		{"blocking loop", ImportBatch{Source: "github", Issues: []ImportIssue{{ExternalID: "x#3", Title: "c"}},
			Links: []ImportLink{{Source: "x#2", Kind: "blocks", Target: "x#3"}, {Source: "x#3", Kind: "blocks", Target: "x#1"}}}, "DEV-1 → DEV-2 → DEV-3"},
	} {
		before := countRows(t, st, `SELECT COUNT(*) FROM issues WHERE project_id = ?`, proj.ID)
		_, err := st.ImportIssues(admin.ID, "DEV", &tc.b, false)
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want validation error containing %q, got %v", tc.name, tc.want, err)
		}
		if after := countRows(t, st, `SELECT COUNT(*) FROM issues WHERE project_id = ?`, proj.ID); after != before {
			t.Fatalf("%s: refused batch wrote %d issues", tc.name, after-before)
		}
	}
	if _, err := st.ImportIssues(admin.ID, "NOPE", &ImportBatch{Source: "s", Issues: []ImportIssue{ok()}}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

// Import refs go with their issue: deleting an imported issue lets a later
// run import it again instead of skipping it forever.
func TestImportRefsFollowDeletes(t *testing.T) {
	st, admin, _ := importFixture(t)
	if _, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteIssue("DEV-3"); err != nil {
		t.Fatal(err)
	}
	b := sampleBatch()
	b.Links = nil
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if err != nil || res.Created != 1 || res.Issues[2].Identifier != "DEV-4" {
		t.Fatalf("re-import after delete: %+v %v", res, err)
	}
}
