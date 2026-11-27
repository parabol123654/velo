package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// QA tests for POST /api/projects/{ref}/import at the store level (#16).

// errIsClean says an import error is one the API turns into a 4xx, not a 500.
func errIsClean(err error) bool {
	return err == nil || errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict)
}

// An issue whose external ID starts with "comment:" shares the key space of
// comments in import_refs. Bad input must be a 400 or work, never a raw
// constraint error (500).
func TestQAImportIssueIDLooksLikeCommentKey(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := &ImportBatch{Source: "github", Issues: []ImportIssue{
		{ExternalID: "comment:1", Title: "An issue whose key looks like a comment key"},
		{ExternalID: "o/r#2", Title: "Has a comment", Comments: []ImportComment{{ExternalID: "1", Body: "hello"}}},
	}}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if !errIsClean(err) {
		t.Fatalf("issue external_id %q next to comment %q gave a raw error (500): %v", "comment:1", "1", err)
	}
	if err == nil && (res.Comments != 1 || countRows(t, st, `SELECT COUNT(*) FROM comments`) != 1) {
		t.Fatalf("comment 1 was silently dropped because issue comment:1 took its import key: %+v", res)
	}
}

// A parent or link that names an imported comment ("comment:<id>") must be a
// 400 "not in this import", not a raw sql.ErrNoRows (500).
func TestQAImportReferenceToCommentKey(t *testing.T) {
	st, admin, _ := importFixture(t)
	first := &ImportBatch{Source: "github", Issues: []ImportIssue{
		{ExternalID: "o/r#1", Title: "One", Comments: []ImportComment{{ExternalID: "c1", Body: "hi"}}},
	}}
	if _, err := st.ImportIssues(admin.ID, "DEV", first, false); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]*ImportBatch{
		"parent": {Source: "github", Issues: []ImportIssue{{ExternalID: "o/r#2", Title: "Two", Parent: "comment:c1"}}},
		"link": {Source: "github", Issues: []ImportIssue{{ExternalID: "o/r#2", Title: "Two"}},
			Links: []ImportLink{{Source: "o/r#2", Kind: "relates", Target: "comment:c1"}}},
		"issue id": {Source: "github", Issues: []ImportIssue{{ExternalID: "comment:c1", Title: "Three"}}},
	} {
		_, err := st.ImportIssues(admin.ID, "DEV", b, true)
		if !errIsClean(err) {
			t.Errorf("%s naming comment:c1 gave a raw error (500): %v", name, err)
		}
	}
}

// SPEC: a blocking loop is a 400 naming it and nothing is written. Two links
// in one batch, A blocks B and B blocks A, are a loop.
func TestQAImportTwoLinkBlockingLoopInBatch(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := &ImportBatch{Source: "jira", Issues: []ImportIssue{{ExternalID: "A", Title: "A"}, {ExternalID: "B", Title: "B"}},
		Links: []ImportLink{{Source: "A", Kind: "blocks", Target: "B"}, {Source: "B", Kind: "blocks", Target: "A"}}}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("A blocks B + B blocks A: want a 400 blocking loop, got res=%+v err=%v", res, err)
	}
}

// Same loop, but the first half was imported by an earlier run.
func TestQAImportBlockingLoopWithEarlierRun(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := &ImportBatch{Source: "jira", Issues: []ImportIssue{{ExternalID: "A", Title: "A"}, {ExternalID: "B", Title: "B"}},
		Links: []ImportLink{{Source: "A", Kind: "blocks", Target: "B"}}}
	if _, err := st.ImportIssues(admin.ID, "DEV", b, false); err != nil {
		t.Fatal(err)
	}
	b.Links = []ImportLink{{Source: "B", Kind: "blocks", Target: "A"}}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("B blocks A after A blocks B: want a 400 blocking loop, got res=%+v err=%v", res, err)
	}
}

// A three-issue loop closed through a link made in Velo is caught.
func TestQAImportBlockingLoopThroughVeloLink(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := &ImportBatch{Source: "jira", Issues: []ImportIssue{{ExternalID: "A", Title: "A"}, {ExternalID: "B", Title: "B"}, {ExternalID: "C", Title: "C"}}}
	if _, err := st.ImportIssues(admin.ID, "DEV", b, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-1", "blocks", "DEV-2"); err != nil { // A blocks B
		t.Fatal(err)
	}
	b.Links = []ImportLink{{Source: "B", Kind: "blocks", Target: "C"}, {Source: "C", Kind: "blocks", Target: "A"}}
	_, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("A→B (Velo), B→C, C→A: want a 400 loop, got %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issue_links`); n != 1 {
		t.Fatalf("failed import wrote links: %d", n)
	}
}

// Parent loops and a self-parent are 400s; a parent from an earlier run works.
func TestQAImportParentsAcrossRuns(t *testing.T) {
	st, admin, _ := importFixture(t)
	for name, b := range map[string]*ImportBatch{
		"self":  {Source: "s", Issues: []ImportIssue{{ExternalID: "A", Title: "A", Parent: "A"}}},
		"two":   {Source: "s", Issues: []ImportIssue{{ExternalID: "A", Title: "A", Parent: "B"}, {ExternalID: "B", Title: "B", Parent: "A"}}},
		"three": {Source: "s", Issues: []ImportIssue{{ExternalID: "A", Title: "A", Parent: "C"}, {ExternalID: "B", Title: "B", Parent: "A"}, {ExternalID: "C", Title: "C", Parent: "B"}}},
	} {
		if _, err := st.ImportIssues(admin.ID, "DEV", b, false); !errors.Is(err, ErrValidation) {
			t.Errorf("%s parent loop: want 400, got %v", name, err)
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issues`); n != 0 {
		t.Fatalf("refused imports wrote %d issues", n)
	}
	if _, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "P", Title: "P"}}}, false); err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "s", Issues: []ImportIssue{
		{ExternalID: "C", Title: "C", Parent: "P"}, {ExternalID: "D", Title: "D", Parent: "C"}}}, false)
	if err != nil || res.Created != 2 {
		t.Fatalf("children of an earlier run: %+v %v", res, err)
	}
	c, _ := st.GetIssue("DEV-2")
	d, _ := st.GetIssue("DEV-3")
	if c.ParentIdentifier != "DEV-1" || d.ParentIdentifier != "DEV-2" {
		t.Fatalf("parents: C→%q D→%q", c.ParentIdentifier, d.ParentIdentifier)
	}
	// Another source does not see source "s".
	if _, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "other", Issues: []ImportIssue{{ExternalID: "X", Title: "X", Parent: "P"}}}, false); !errors.Is(err, ErrValidation) {
		t.Fatalf("parent from another source: want 400, got %v", err)
	}
}

// A partial rerun: some issues are old, some new; new comments on old issues
// are added; links between old and new issues are made once.
func TestQAImportPartialRerun(t *testing.T) {
	st, admin, _ := importFixture(t)
	b1 := &ImportBatch{Source: "gh", Issues: []ImportIssue{
		{ExternalID: "1", Title: "One", Comments: []ImportComment{{ExternalID: "c1", Body: "first"}}},
		{ExternalID: "2", Title: "Two"},
	}}
	if _, err := st.ImportIssues(admin.ID, "DEV", b1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Title: ptr("Edited in Velo")}); err != nil {
		t.Fatal(err)
	}
	b2 := &ImportBatch{Source: "gh", Issues: []ImportIssue{
		{ExternalID: "1", Title: "One (changed upstream)", Comments: []ImportComment{{ExternalID: "c1", Body: "first"}, {ExternalID: "c2", Body: "second"}}},
		{ExternalID: "3", Title: "Three", Parent: "2"},
	}, Links: []ImportLink{{Source: "3", Kind: "blocked_by", Target: "1"}, {Source: "2", Kind: "relates", Target: "3"}}}
	for i := 0; i < 2; i++ {
		res, err := st.ImportIssues(admin.ID, "DEV", b2, false)
		if err != nil {
			t.Fatal(err)
		}
		wantCreated, wantComments, wantLinks := 1, 1, 2
		if i == 1 {
			wantCreated, wantComments, wantLinks = 0, 0, 0
		}
		if res.Created != wantCreated || res.Comments != wantComments || res.Links != wantLinks {
			t.Fatalf("run %d: %+v", i+2, res)
		}
	}
	one, _ := st.GetIssue("DEV-1")
	if one.Title != "Edited in Velo" {
		t.Fatalf("rerun overwrote a Velo edit: %q", one.Title)
	}
	three, _ := st.GetIssue("DEV-3")
	if three.ParentIdentifier != "DEV-2" || len(three.Links) != 2 {
		t.Fatalf("DEV-3: parent %q links %+v", three.ParentIdentifier, three.Links)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM comments`); n != 2 {
		t.Fatalf("comments: %d, want 2", n)
	}
}

func ptr[T any](v T) *T { return &v }

// Deleting imported issues and comments, then rerunning, brings them back
// (their import_refs rows go with them) without duplicating the rest.
func TestQAImportDeleteThenRerun(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := &ImportBatch{Source: "gh", Issues: []ImportIssue{
		{ExternalID: "1", Title: "Parent", Comments: []ImportComment{{ExternalID: "c1", Body: "a"}, {ExternalID: "c2", Body: "b"}}},
		{ExternalID: "2", Title: "Child", Parent: "1"},
	}, Links: []ImportLink{{Source: "1", Kind: "blocks", Target: "2"}}}
	if _, err := st.ImportIssues(admin.ID, "DEV", b, false); err != nil {
		t.Fatal(err)
	}
	cs, _ := st.ListComments(mustGet(t, st, "DEV-1").ID)
	if _, err := st.DeleteComment(admin, cs[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM import_refs WHERE comment_id IS NOT NULL`); n != 1 {
		t.Fatalf("comment ref not dropped: %d", n)
	}
	if _, err := st.DeleteIssue("DEV-1"); err != nil { // the parent, with its other comment and the link
		t.Fatal(err)
	}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Skipped != 1 || res.Comments != 2 || res.Links != 1 {
		t.Fatalf("rerun after deletes: %+v", res)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM import_refs`); n != 4 {
		t.Fatalf("import_refs rows: %d, want 4", n)
	}
}

func mustGet(t *testing.T, st *Store, ref string) *Issue {
	t.Helper()
	iss, err := st.GetIssue(ref)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

// Vietnamese names in labels, states and cycles match existing ones ignoring
// case; the batch does not make a second label for another case.
func TestQAImportVietnameseNames(t *testing.T) {
	st, admin, proj := importFixture(t)
	if _, err := st.CreateLabel(proj.ID, "Lỗi nghiêm trọng", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateWorkflowState(proj.ID, "Đang xử lý", "started", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCycle(proj.ID, "Đợt một", "", "", "", "active"); err != nil {
		t.Fatal(err)
	}
	b := &ImportBatch{Source: "s", Issues: []ImportIssue{
		{ExternalID: "1", Title: "Tiêu đề", State: "ĐANG XỬ LÝ", Labels: []string{"LỖI NGHIÊM TRỌNG", "Giao diện", "GIAO DIỆN"}, Cycle: "ĐỢT MỘT"},
		{ExternalID: "2", Title: "Hai", Labels: []string{"giao diện"}, Cycle: "Đợt hai"},
	}}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.LabelsCreated, "|") != "Giao diện" || strings.Join(res.CyclesCreated, "|") != "Đợt hai" {
		t.Fatalf("created labels %q cycles %q", res.LabelsCreated, res.CyclesCreated)
	}
	one := mustGet(t, st, "DEV-1")
	if one.StateName != "Đang xử lý" || one.CycleName != "Đợt một" || len(one.Labels) != 2 {
		t.Fatalf("DEV-1: state %q cycle %q labels %+v", one.StateName, one.CycleName, one.Labels)
	}
	// Search finds an imported Vietnamese title without diacritics.
	found, err := st.SearchIssues("tieu de", 10, nil)
	if err != nil || len(found) != 1 {
		t.Fatalf("search imported issue: %v %+v", err, found)
	}
}

// The dry run report is the real run's report, field for field, also when
// labels, cycles, unassigned people, comments, parents and links are involved
// and on a rerun.
func TestQAImportDryRunSameReport(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := sampleBatch()
	b.Issues = append(b.Issues, ImportIssue{ExternalID: "o/r#4", Title: "Four", Labels: []string{"new-one"}, Assignee: "ghost@x.dev",
		Comments: []ImportComment{{ExternalID: "c4", Body: "**bold**"}}})
	b.Links = append(b.Links, ImportLink{Source: "o/r#4", Kind: "relates", Target: "o/r#2"})
	for run := 0; run < 2; run++ {
		dry, err := st.ImportIssues(admin.ID, "DEV", b, true)
		if err != nil {
			t.Fatal(err)
		}
		real, err := st.ImportIssues(admin.ID, "DEV", b, false)
		if err != nil {
			t.Fatal(err)
		}
		dry.DryRun = false
		dj, _ := json.Marshal(dry)
		rj, _ := json.Marshal(real)
		if string(dj) != string(rj) {
			t.Fatalf("run %d: dry run report differs\ndry:  %s\nreal: %s", run+1, dj, rj)
		}
		b.Issues = append(b.Issues, ImportIssue{ExternalID: "o/r#5", Title: "Five", Parent: "o/r#4"})
	}
}

// Comments keep Markdown, the original instant (converted to UTC) and name
// the author; descriptions keep Markdown above the footer.
func TestQAImportCommentsMarkdownAndTimeZones(t *testing.T) {
	st, admin, _ := importFixture(t)
	hcm := time.FixedZone("ICT", 7*3600)
	at := time.Date(2024, 1, 1, 6, 30, 0, 0, hcm) // 2023-12-31T23:30Z
	md := "## Bước tái hiện\n\n```go\nfmt.Println(\"x\")\n```\n\n- [ ] một\n- [x] hai\n\n| a | b |\n|---|---|\n| 1 | 2 |"
	b := &ImportBatch{Source: "gh", Issues: []ImportIssue{{ExternalID: "1", Title: "T", Description: md, CreatedAt: at,
		Comments: []ImportComment{
			{ExternalID: "c1", Author: "@nguyễn", Body: md, CreatedAt: at},
			{ExternalID: "c2", Body: "no author, no date"},
		}}}}
	if _, err := st.ImportIssues(admin.ID, "DEV", b, false); err != nil {
		t.Fatal(err)
	}
	iss := mustGet(t, st, "DEV-1")
	if !iss.CreatedAt.Equal(at) || iss.Description != md+"\n\n> Imported from 1" {
		t.Fatalf("issue created %v description %q", iss.CreatedAt, iss.Description)
	}
	cs, _ := st.ListComments(iss.ID)
	if len(cs) != 2 {
		t.Fatalf("comments: %+v", cs)
	}
	var withAuthor *Comment
	for i := range cs {
		if strings.Contains(cs[i].Body, "@nguyễn") {
			withAuthor = &cs[i]
		}
	}
	if withAuthor == nil || !withAuthor.CreatedAt.Equal(at) || !strings.HasSuffix(withAuthor.Body, md) {
		t.Fatalf("comment: %+v", withAuthor)
	}
	// The header shows the day the comment was written where it was written.
	if first := strings.SplitN(withAuthor.Body, "\n", 2)[0]; first != "**@nguyễn** · "+at.Format("2006-01-02") {
		t.Fatalf("comment header %q", first)
	}
}

// Assignees: an email in any case finds the user; a disabled user, an
// unknown email and "me" leave the issue unassigned and named in the report.
func TestQAImportAssignees(t *testing.T) {
	st, admin, _ := importFixture(t)
	ann, err := st.CreateUserDirect("ann@velo.local", "Ann", "secret123", "member")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := st.CreateUserDirect("gone@velo.local", "Gone", "secret123", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(admin.ID, gone.ID, UpdateUserInput{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	b := &ImportBatch{Source: "s", Issues: []ImportIssue{
		{ExternalID: "1", Title: "ann", Assignee: " ANN@Velo.Local "},
		{ExternalID: "2", Title: "disabled", Assignee: "gone@velo.local"},
		{ExternalID: "3", Title: "unknown", Assignee: "who@else.dev"},
	}}
	res, err := st.ImportIssues(admin.ID, "DEV", b, false)
	if err != nil {
		t.Fatal(err)
	}
	if mustGet(t, st, "DEV-1").AssigneeID != ann.ID {
		t.Fatal("ann not assigned")
	}
	two := mustGet(t, st, "DEV-2")
	if two.AssigneeID != "" || !strings.Contains(two.Description, "assigned to gone@velo.local") {
		t.Fatalf("disabled assignee: %q %q", two.AssigneeID, two.Description)
	}
	if strings.Join(res.Unassigned, "|") != "2: gone@velo.local|3: who@else.dev" {
		t.Fatalf("unassigned: %q", res.Unassigned)
	}
}

// With no unstarted state, an issue without a state lands where CreateIssue
// would put it (the importer's code says "as CreateIssue does"), and asking
// for the unstarted category by name is a 400.
func TestQAImportNoUnstartedState(t *testing.T) {
	st, admin, proj := importFixture(t)
	states, _ := st.ListWorkflowStates(proj.ID)
	for _, s := range states {
		switch s.Category {
		case "unstarted":
			if _, err := st.UpdateWorkflowState(s.ID, "", "started", "", nil); err != nil {
				t.Fatal(err)
			}
		case "backlog":
			last := 99000.0
			if _, err := st.UpdateWorkflowState(s.ID, "", "", "", &last); err != nil {
				t.Fatal(err)
			}
		}
	}
	made, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "by hand"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "x", StateCategory: "unstarted"}}}, false); !errors.Is(err, ErrValidation) {
		t.Fatalf("state_category unstarted with none: want 400, got %v", err)
	}
	if _, err := st.ImportIssues(admin.ID, "DEV", &ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "imported"}}}, false); err != nil {
		t.Fatal(err)
	}
	imp := mustGet(t, st, "DEV-2")
	if imp.StateName != made.StateName {
		t.Fatalf("no unstarted state: CreateIssue picks %q, import picks %q", made.StateName, imp.StateName)
	}
}

// Bad input is a 400 naming the problem, and nothing is written.
func TestQAImportBadInput(t *testing.T) {
	st, admin, _ := importFixture(t)
	long := strings.Repeat("ộ", 201)
	many := make([]ImportIssue, 5001)
	for i := range many {
		many[i] = ImportIssue{ExternalID: fmt.Sprint(i), Title: "t"}
	}
	base := countRows(t, st, `SELECT COUNT(*) FROM issues`) + countRows(t, st, `SELECT COUNT(*) FROM labels`) + countRows(t, st, `SELECT COUNT(*) FROM import_refs`) // default labels
	for name, b := range map[string]*ImportBatch{
		"nil issues":        {Source: "s"},
		"blank source":      {Source: "  ", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}},
		"long source":       {Source: long, Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}},
		"long external id":  {Source: "s", Issues: []ImportIssue{{ExternalID: long, Title: "t"}}},
		"padded duplicate":  {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}, {ExternalID: " 1 ", Title: "t"}}},
		"priority 5":        {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Priority: 5}}},
		"priority -1":       {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Priority: -1}}},
		"blank title":       {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: " \t"}}},
		"unknown state":     {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", State: "Nope"}}},
		"bad category":      {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", StateCategory: "Done"}}},
		"blank label":       {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Labels: []string{" "}}}},
		"comment no id":     {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Comments: []ImportComment{{Body: "x"}}}}},
		"comment no body":   {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Comments: []ImportComment{{ExternalID: "c", Body: " "}}}}},
		"comment twice":     {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Comments: []ImportComment{{ExternalID: "c", Body: "x"}}}, {ExternalID: "2", Title: "t", Comments: []ImportComment{{ExternalID: "c", Body: "y"}}}}},
		"unknown parent":    {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t", Parent: "zz"}}},
		"link unknown kind": {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}, {ExternalID: "2", Title: "t"}}, Links: []ImportLink{{Source: "1", Kind: "parent", Target: "2"}}},
		"link to itself":    {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}, Links: []ImportLink{{Source: "1", Kind: "relates", Target: " 1"}}},
		"link unknown end":  {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}, Links: []ImportLink{{Source: "1", Kind: "relates", Target: "9"}}},
		"link empty end":    {Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}, Links: []ImportLink{{Source: "1", Kind: "relates", Target: ""}}},
		"cycle no name":     {Source: "s", Cycles: []ImportCycle{{Name: " "}}, Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}},
		"cycle bad status":  {Source: "s", Cycles: []ImportCycle{{Name: "c", Status: "done"}}, Issues: []ImportIssue{{ExternalID: "1", Title: "t", Cycle: "c"}}},
		"5001 issues":       {Source: "s", Issues: many},
	} {
		if _, err := st.ImportIssues(admin.ID, "DEV", b, false); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want a 400, got %v", name, err)
		}
		if n := countRows(t, st, `SELECT COUNT(*) FROM issues`) + countRows(t, st, `SELECT COUNT(*) FROM labels`) + countRows(t, st, `SELECT COUNT(*) FROM import_refs`) - base; n != 0 {
			t.Errorf("refused import %q wrote %d rows", name, n)
		}
	}
	if _, err := st.ImportIssues(admin.ID, "NOPE", &ImportBatch{Source: "s", Issues: []ImportIssue{{ExternalID: "1", Title: "t"}}}, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: want 404, got %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issues`) + countRows(t, st, `SELECT COUNT(*) FROM labels`) + countRows(t, st, `SELECT COUNT(*) FROM import_refs`) - base; n != 0 {
		t.Fatalf("refused imports wrote %d rows", n)
	}
}

// Concurrent imports of the same batch make each issue and comment once.
func TestQAImportConcurrentSameBatch(t *testing.T) {
	st, admin, _ := importFixture(t)
	b := sampleBatch()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.ImportIssues(admin.ID, "DEV", b, false); err != nil {
				errs <- err
			}
		}()
	}
	// Mutations made through the normal API at the same time.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "by hand"}); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent import: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issues`); n != 3+5 {
		t.Fatalf("issues: %d, want 8", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM comments`); n != 1 {
		t.Fatalf("comments: %d, want 1", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issue_links`); n != 1 {
		t.Fatalf("links: %d, want 1", n)
	}
}

// A database made before import_refs existed opens and imports.
func TestQAImportOnPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE import_refs`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	res, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false)
	if err != nil || res.Created != 3 {
		t.Fatalf("import on migrated db: %+v %v", res, err)
	}
}

// Deleting the project drops its import_refs, so a project with the same key
// imports everything again; an export of an imported project round-trips.
func TestQAImportProjectDeleteAndExport(t *testing.T) {
	st, admin, _ := importFixture(t)
	if _, err := st.ImportIssues(admin.ID, "DEV", sampleBatch(), false); err != nil {
		t.Fatal(err)
	}
	bundle, err := st.ExportProjectJSON("DEV")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteProject("DEV"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM import_refs`); n != 0 {
		t.Fatalf("import_refs left after project delete: %d", n)
	}
	raw, _ := json.Marshal(bundle)
	var back ExportBundle
	_ = json.Unmarshal(raw, &back)
	if _, err := st.ImportProjectJSON(admin.ID, &back); err != nil {
		t.Fatalf("bundle import of an imported project: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM issue_links`); n != 1 {
		t.Fatalf("links after round trip: %d", n)
	}
}

// One request holds the only database connection. A batch at the 5000 issue
// limit whose blocks links form a chain listed last-first should still finish
// in reasonable time, so other users are not locked out for long.
func TestQAImportBlockChainPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	st, admin, _ := importFixture(t)
	const n = 5000
	b := &ImportBatch{Source: "s"}
	for i := 0; i < n; i++ {
		b.Issues = append(b.Issues, ImportIssue{ExternalID: fmt.Sprint(i), Title: "t"})
	}
	for i := n - 2; i >= 0; i-- {
		b.Links = append(b.Links, ImportLink{Source: fmt.Sprint(i), Kind: "blocks", Target: fmt.Sprint(i + 1)})
	}
	start := time.Now()
	if _, err := st.ImportIssues(admin.ID, "DEV", b, true); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("%d issues with a %d-link blocks chain listed last-first: %s", n, n-1, took)
	if took > 30*time.Second {
		t.Fatalf("import held the database for %s", took)
	}
}
