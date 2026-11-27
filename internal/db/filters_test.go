package db

import (
	"errors"
	"testing"
)

// #17: saved filters belong to one user and hold a valid query.
func TestSavedFilters(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	other, _ := st.CreateUserDirect("other@velo.local", "Other", "secret123", "member")

	a, err := st.CreateSavedFilter(admin.ID, " My open bugs ", "assignee:me label:bug is:open")
	if err != nil || a.Name != "My open bugs" || a.Query != "assignee:me label:bug is:open" {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, err := st.CreateSavedFilter(admin.ID, "Urgent", "priority:urgent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSavedFilter(other.ID, "My open bugs", "is:open"); err != nil {
		t.Fatalf("another user may reuse a name: %v", err)
	}

	for _, tc := range []struct {
		name, query string
		want        error
	}{
		{"", "is:open", ErrValidation},
		{"x", "", ErrValidation},
		{"x", "asignee:me", ErrValidation}, // the query must parse
		{"my OPEN bugs", "is:open", ErrConflict},
	} {
		if _, err := st.CreateSavedFilter(admin.ID, tc.name, tc.query); !errors.Is(err, tc.want) {
			t.Errorf("create %q %q: want %v, got %v", tc.name, tc.query, tc.want, err)
		}
	}

	list, err := st.ListSavedFilters(admin.ID)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list in creation order: %+v %v", list, err)
	}

	// Partial update; move b first.
	name, pos := "Urgent only", a.Position-1
	upd, err := st.UpdateSavedFilter(admin.ID, b.ID, SavedFilterInput{Name: &name, Position: &pos})
	if err != nil || upd.Name != "Urgent only" || upd.Query != "priority:urgent" {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if list, _ := st.ListSavedFilters(admin.ID); list[0].ID != b.ID {
		t.Fatalf("position did not reorder: %+v", list)
	}
	bad := "is:done"
	if _, err := st.UpdateSavedFilter(admin.ID, b.ID, SavedFilterInput{Query: &bad}); !errors.Is(err, ErrValidation) {
		t.Fatalf("update with a bad query: %v", err)
	}

	// Another user's filter does not exist for this user.
	if _, err := st.UpdateSavedFilter(other.ID, a.ID, SavedFilterInput{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update someone else's filter: %v", err)
	}
	if err := st.DeleteSavedFilter(other.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete someone else's filter: %v", err)
	}
	if err := st.DeleteSavedFilter(admin.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSavedFilters(admin.ID); len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}

	// Saved filters are not history: a user with only filters can be deleted.
	if err := st.DeleteUser(admin.ID, other.ID); err != nil {
		t.Fatalf("delete a user who has saved filters: %v", err)
	}
}

// A database from before saved filters gets the table on open.
func TestMigrateAddsSavedFilters(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.db.Exec(`DROP TABLE saved_filters`); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateSavedFilter(admin.ID, "x", "is:open"); err != nil {
		t.Fatalf("after migrate: %v", err)
	}
}
