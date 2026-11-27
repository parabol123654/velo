package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAuthAndInviteLifecycle(t *testing.T) {
	st := newTestStore(t)

	has, err := st.HasUsers()
	if err != nil || has {
		t.Fatalf("expected HasUsers=false on fresh DB, got %v, %v", has, err)
	}

	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if admin.Role != "admin" {
		t.Fatalf("expected admin role, got %s", admin.Role)
	}

	// Second CreateFirstAdmin must fail with ErrConflict
	if _, err := st.CreateFirstAdmin("other@velo.local", "Other", "secret123"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on second CreateFirstAdmin, got %v", err)
	}

	// Password login + session + API token
	logged, err := st.AuthenticatePassword("ADMIN@velo.local", "secret123")
	if err != nil || logged.ID != admin.ID {
		t.Fatalf("AuthenticatePassword failed: %v", err)
	}
	if _, err := st.AuthenticatePassword("admin@velo.local", "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized on bad password, got %v", err)
	}

	sessTok, _, err := st.CreateSession(admin.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	uSess, err := st.GetUserBySession(sessTok)
	if err != nil || uSess.ID != admin.ID {
		t.Fatalf("GetUserBySession: %v", err)
	}

	rawAPI, meta, err := st.CreateAPIToken(admin.ID, "Antigravity MCP", TokenScope{})
	if err != nil || !strings.HasPrefix(rawAPI, "velo_") || meta.Name != "Antigravity MCP" {
		t.Fatalf("CreateAPIToken failed: %v", err)
	}
	uTok, _, err := st.GetUserByAPIToken(rawAPI)
	if err != nil || uTok.ID != admin.ID {
		t.Fatalf("GetUserByAPIToken failed: %v", err)
	}

	// Invite member flow
	inv, err := st.CreateInvite(admin.ID, "member", time.Hour)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	member, err := st.AcceptInvite(inv.Code, "dev@velo.local", "Dev One", "devpass1")
	if err != nil || member.Role != "member" {
		t.Fatalf("AcceptInvite: %v", err)
	}
	// Reusing single-use invite must fail
	if _, err := st.AcceptInvite(inv.Code, "dev2@velo.local", "Dev Two", "devpass2"); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation on reused invite, got %v", err)
	}
}

func TestConcurrentIssuesFTSAndExportImport(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateFirstAdmin("lead@velo.local", "Tech Lead", "secret123")
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}

	proj, err := st.CreateProject("dev", "Core Platform", "Main repo")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.Key != "DEV" {
		t.Fatalf("expected uppercase key DEV, got %s", proj.Key)
	}

	states, err := st.ListWorkflowStates(proj.ID)
	if err != nil || len(states) != 6 {
		t.Fatalf("expected 6 seeded workflow states, got %d (%v)", len(states), err)
	}

	// Concurrent creation of 15 issues -> must get unique DEV-1..DEV-15 without SQLITE_BUSY failure
	const total = 15
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := st.CreateIssue(admin.ID, CreateIssueInput{
				ProjectRef:  "DEV",
				Title:       fmt.Sprintf("Fix Envoy edge routing bug #%d", n),
				Description: "Reproduce race condition in local proxy listener",
				Priority:    3,
				LabelIDs:    []string{"bug"},
			})
			if err != nil {
				errs <- err
			}
		}(i + 1)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent CreateIssue failed: %v", e)
	}

	all, err := st.ListIssues(IssueFilter{ProjectRef: "DEV"})
	if err != nil || len(all) != total {
		t.Fatalf("expected %d issues, got %d (%v)", total, len(all), err)
	}

	// Sub-issue + state transition + activity log
	sub, err := st.CreateIssue(admin.ID, CreateIssueInput{
		ProjectRef: "DEV",
		ParentRef:  "DEV-1",
		Title:      "Add regression test for listener",
		StateID:    "Done",
	})
	if err != nil {
		t.Fatalf("create sub-issue: %v", err)
	}
	parent, err := st.GetIssue("dev-1")
	if err != nil {
		t.Fatalf("GetIssue case-insensitive: %v", err)
	}
	if parent.SubIssueCount != 1 || parent.SubIssueDoneCount != 1 || len(parent.SubIssues) != 1 || parent.SubIssues[0].ID != sub.ID {
		t.Fatalf("unexpected sub-issue counts on parent: %+v", parent)
	}

	inProg := "In Progress"
	prio := 4
	updated, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{
		StateID:  &inProg,
		Priority: &prio,
	})
	if err != nil || updated.StateName != "In Progress" || updated.Priority != 4 {
		t.Fatalf("UpdateIssue failed: %v, %+v", err, updated)
	}
	acts, err := st.ListActivities(updated.ID)
	if err != nil || len(acts) < 3 { // created + state + priority
		t.Fatalf("expected >=3 activities, got %d (%v)", len(acts), err)
	}

	// FTS5 search including hyphenated identifier & keyword
	hits, err := st.SearchIssues("Envoy routing", 20, nil)
	if err != nil || len(hits) != total {
		t.Fatalf("FTS5 search 'Envoy routing' expected %d hits, got %d (%v)", total, len(hits), err)
	}
	idHits, err := st.SearchIssues("DEV-1", 5, nil)
	if err != nil || len(idHits) == 0 {
		t.Fatalf("FTS5 search 'DEV-1' expected hits, got %d (%v)", len(idHits), err)
	}

	// Comments + Export Markdown/JSON + Re-import under new project key
	if _, err := st.AddComment(admin.ID, "DEV-1", "Verified locally with `go test`"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	md, err := st.ExportProjectMarkdown("DEV")
	if err != nil || !strings.Contains(md, "**DEV-1**") {
		t.Fatalf("ExportProjectMarkdown failed: %v\n%s", err, md)
	}
	// Links survive the round trip, including one stored swapped (blocked_by).
	if _, _, err := st.LinkIssues(admin.ID, "DEV-2", "blocked_by", "DEV-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LinkIssues(admin.ID, "DEV-2", "relates", "DEV-3"); err != nil {
		t.Fatal(err)
	}
	wantLinks := linksByTitle(t, st, "DEV")
	if len(wantLinks) != 4 { // each link seen from both sides
		t.Fatalf("setup: links %v", wantLinks)
	}
	bundle, err := st.ExportProjectJSON("DEV")
	if err != nil {
		t.Fatalf("ExportProjectJSON: %v", err)
	}
	// Through JSON, as a real export file would travel.
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle = &ExportBundle{}
	if err := json.Unmarshal(raw, bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Project.Key = "IMP"
	bundle.Project.Name = "Imported Platform"
	impProj, err := st.ImportProjectJSON(admin.ID, bundle)
	if err != nil || impProj.IssueCount != total+1 {
		t.Fatalf("ImportProjectJSON failed: %v, %+v", err, impProj)
	}
	if got := linksByTitle(t, st, "IMP"); strings.Join(got, "\n") != strings.Join(wantLinks, "\n") {
		t.Fatalf("links after import:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantLinks, "\n"))
	}
}

// linksByTitle lists a project's links as "title kind title", sorted, so two
// projects with the same issues compare equal whatever their numbers are.
func linksByTitle(t *testing.T, st *Store, project string) []string {
	t.Helper()
	issues, err := st.ListIssues(IssueFilter{ProjectRef: project})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range issues {
		full, err := st.GetIssue(it.Identifier)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range full.Links {
			out = append(out, it.Title+" "+l.Kind+" "+l.Issue.Title)
		}
	}
	sort.Strings(out)
	return out
}

func TestBackupTo(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(out); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	if err := st.BackupTo(out); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrite should be refused, got %v", err)
	}
	copyStore, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	if has, err := copyStore.HasUsers(); err != nil || !has {
		t.Fatalf("backup lacks data: %v %v", has, err)
	}
}

func TestTokenScopeStore(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	p, _ := st.CreateProject("DEV", "Dev", "")
	if _, _, err := st.CreateAPIToken(admin.ID, "x", TokenScope{Access: "root"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad access: %v", err)
	}
	raw, meta, err := st.CreateAPIToken(admin.ID, "agent", TokenScope{Access: "read", Projects: []string{"dev"}})
	if err != nil || meta.Access != "read" || len(meta.ProjectIDs) != 1 || meta.ProjectKeys[0] != "DEV" {
		t.Fatalf("scoped token: %+v %v", meta, err)
	}
	// Deleting the project must not turn the token into an unrestricted one.
	if _, _, err := st.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	_, tok, err := st.GetUserByAPIToken(raw)
	if err != nil || !tok.Restricted() || tok.AllowsProject("anything") {
		t.Fatalf("token after project delete: %+v %v", tok, err)
	}
	list, _ := st.ListAPITokens(admin.ID)
	if len(list) != 1 || list[0].ProjectKeys[0] != "(deleted)" {
		t.Fatalf("listed keys: %+v", list)
	}
}
