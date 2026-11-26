package db

import (
	"errors"
	"fmt"
	"path/filepath"
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

	rawAPI, meta, err := st.CreateAPIToken(admin.ID, "Antigravity MCP")
	if err != nil || !strings.HasPrefix(rawAPI, "velo_") || meta.Name != "Antigravity MCP" {
		t.Fatalf("CreateAPIToken failed: %v", err)
	}
	uTok, err := st.GetUserByAPIToken(rawAPI)
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
	hits, err := st.SearchIssues("Envoy routing", 20)
	if err != nil || len(hits) != total {
		t.Fatalf("FTS5 search 'Envoy routing' expected %d hits, got %d (%v)", total, len(hits), err)
	}
	idHits, err := st.SearchIssues("DEV-1", 5)
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
	bundle, err := st.ExportProjectJSON("DEV")
	if err != nil {
		t.Fatalf("ExportProjectJSON: %v", err)
	}
	bundle.Project.Key = "IMP"
	bundle.Project.Name = "Imported Platform"
	impProj, err := st.ImportProjectJSON(admin.ID, bundle)
	if err != nil || impProj.IssueCount != total+1 {
		t.Fatalf("ImportProjectJSON failed: %v, %+v", err, impProj)
	}
}
