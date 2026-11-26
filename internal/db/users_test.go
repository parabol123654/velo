package db

import (
	"errors"
	"testing"
	"time"
)

func TestUserManagement(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateUserDirect("dev@velo.local", "Dev", "devpass1", "member")
	if err != nil {
		t.Fatal(err)
	}

	// Change own password: wrong current fails; success keeps only the given session.
	keep, _, _ := st.CreateSession(dev.ID, time.Hour)
	other, _, _ := st.CreateSession(dev.ID, time.Hour)
	if err := st.ChangePassword(dev.ID, "nope", "newpass1", keep); !errors.Is(err, ErrValidation) {
		t.Fatalf("wrong current password: got %v", err)
	}
	if err := st.ChangePassword(dev.ID, "devpass1", "newpass1", keep); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := st.GetUserBySession(keep); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := st.GetUserBySession(other); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("other session should be revoked, got %v", err)
	}
	if _, err := st.AuthenticatePassword("dev@velo.local", "newpass1"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}

	// Admin reset revokes every session.
	if err := st.ResetPassword(dev.ID, "reset123"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUserBySession(keep); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reset should revoke sessions, got %v", err)
	}

	// Disable blocks password, session and API token; enable restores token.
	sess, _, _ := st.CreateSession(dev.ID, time.Hour)
	tok, _, _ := st.CreateAPIToken(dev.ID, "cli", TokenScope{})
	yes, no := true, false
	u, err := st.UpdateUser(admin.ID, dev.ID, UpdateUserInput{Disabled: &yes})
	if err != nil || u.DisabledAt == nil {
		t.Fatalf("disable: %v %+v", err, u)
	}
	if _, err := st.AuthenticatePassword("dev@velo.local", "reset123"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled login: %v", err)
	}
	if _, err := st.GetUserBySession(sess); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled session: %v", err)
	}
	if _, _, err := st.GetUserByAPIToken(tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled token: %v", err)
	}
	if _, err := st.UpdateUser(admin.ID, dev.ID, UpdateUserInput{Disabled: &no}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetUserByAPIToken(tok); err != nil {
		t.Fatalf("re-enabled token: %v", err)
	}

	// Guards: no self change, last admin stays.
	member := "member"
	if _, err := st.UpdateUser(admin.ID, admin.ID, UpdateUserInput{Role: &member}); !errors.Is(err, ErrValidation) {
		t.Fatalf("self demote: %v", err)
	}
	admin2, _ := st.CreateUserDirect("a2@velo.local", "A2", "secret123", "admin")
	if _, err := st.UpdateUser(admin2.ID, admin.ID, UpdateUserInput{Disabled: &yes}); err != nil {
		t.Fatalf("disable one of two admins: %v", err)
	}
	if _, err := st.UpdateUser(admin.ID, admin2.ID, UpdateUserInput{Role: &member}); !errors.Is(err, ErrValidation) {
		t.Fatalf("demote last active admin: %v", err)
	}
	if err := st.DeleteUser(admin.ID, admin2.ID); !errors.Is(err, ErrValidation) {
		t.Fatalf("delete last active admin: %v", err)
	}

	// Delete: allowed without history, refused with history.
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(dev.ID, CreateIssueInput{ProjectRef: "DEV", Title: "by dev"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(admin2.ID, dev.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete user with history: %v", err)
	}
	fresh, _ := st.CreateUserDirect("new@velo.local", "New", "secret123", "member")
	if err := st.DeleteUser(admin2.ID, fresh.ID); err != nil {
		t.Fatalf("delete fresh user: %v", err)
	}
	if err := st.DeleteUser(admin2.ID, fresh.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestMigrateAddsDisabledAt(t *testing.T) {
	st := newTestStore(t)
	// Simulate a v0.1 database: drop the column, then re-run migrate.
	if _, err := st.db.Exec(`ALTER TABLE users DROP COLUMN disabled_at`); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatalf("migrate v0.1 db: %v", err)
	}
	if _, err := st.ListUsers(); err != nil {
		t.Fatalf("ListUsers after migrate: %v", err)
	}
}
