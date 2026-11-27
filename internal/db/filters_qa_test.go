package db

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func qaFilterUsers(t *testing.T) (*Store, *User, *User) {
	t.Helper()
	st := newTestStore(t)
	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUserDirect("other@velo.local", "Other", "secret123", "member")
	if err != nil {
		t.Fatal(err)
	}
	return st, admin, other
}

// QA: name limits. SPEC and the error say "1-80 characters", so 80 Vietnamese
// letters (multi-byte in UTF-8) must be accepted and 81 refused.
func TestQASavedFilterNameLimits(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	for _, tc := range []struct {
		label, name string
		ok          bool
	}{
		{"spaces only", "    ", false},
		{"tabs and newlines only", "\t\n \r", false},
		{"80 ASCII", strings.Repeat("a", 80), true},
		{"81 ASCII", strings.Repeat("b", 81), false},
		{"80 ASCII padded with spaces", "  " + strings.Repeat("c", 80) + "  ", true},
		{"80 Vietnamese runes", strings.Repeat("ố", 80), true},
		{"81 Vietnamese runes", strings.Repeat("ộ", 81), false},
		{"27 Vietnamese runes (81 bytes)", strings.Repeat("ữ", 27), true},
	} {
		_, err := st.CreateSavedFilter(admin.ID, tc.name, "is:open")
		if tc.ok && err != nil {
			t.Errorf("%s (%d bytes, %d runes): want ok, got %v", tc.label, len(tc.name), len([]rune(strings.TrimSpace(tc.name))), err)
		}
		if !tc.ok && !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", tc.label, err)
		}
	}
}

// QA: renaming to a too-long or blank name is refused on update too, and the
// stored row is unchanged.
func TestQASavedFilterUpdateValidation(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	f, err := st.CreateSavedFilter(admin.ID, "Mine", "assignee:me")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 81)} {
		b := bad
		if _, err := st.UpdateSavedFilter(admin.ID, f.ID, SavedFilterInput{Name: &b}); !errors.Is(err, ErrValidation) {
			t.Errorf("rename to %q: %v", bad, err)
		}
	}
	empty := "  "
	if _, err := st.UpdateSavedFilter(admin.ID, f.ID, SavedFilterInput{Query: &empty}); !errors.Is(err, ErrValidation) {
		t.Errorf("blank query: %v", err)
	}
	list, _ := st.ListSavedFilters(admin.ID)
	if len(list) != 1 || list[0].Name != "Mine" || list[0].Query != "assignee:me" {
		t.Fatalf("row changed after refused updates: %+v", list)
	}
	// An empty input changes nothing and succeeds.
	got, err := st.UpdateSavedFilter(admin.ID, f.ID, SavedFilterInput{})
	if err != nil || got.Name != "Mine" || got.Position != f.Position {
		t.Fatalf("empty update: %+v %v", got, err)
	}
	// Unknown id.
	if _, err := st.UpdateSavedFilter(admin.ID, "nope", SavedFilterInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := st.DeleteSavedFilter(admin.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete unknown id: %v", err)
	}
}

// QA: case-insensitive uniqueness on create and rename; renaming a filter to
// its own name in another case works.
func TestQASavedFilterCaseInsensitiveNames(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	a, err := st.CreateSavedFilter(admin.ID, "Open Bugs", "is:open")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateSavedFilter(admin.ID, "Urgent", "priority:urgent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSavedFilter(admin.ID, " OPEN BUGS ", "is:open"); !errors.Is(err, ErrConflict) {
		t.Errorf("create case-duplicate: %v", err)
	}
	same := "OPEN bugs"
	if got, err := st.UpdateSavedFilter(admin.ID, a.ID, SavedFilterInput{Name: &same}); err != nil || got.Name != "OPEN bugs" {
		t.Errorf("rename to own name in another case: %+v %v", got, err)
	}
	clash := "open BUGS"
	if _, err := st.UpdateSavedFilter(admin.ID, b.ID, SavedFilterInput{Name: &clash}); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto another filter's name: %v", err)
	}
	if list, _ := st.ListSavedFilters(admin.ID); list[1].Name != "Urgent" {
		t.Errorf("refused rename changed the row: %+v", list)
	}
}

// QA: "ignoring case" for Vietnamese names. SQLite NOCASE folds ASCII only, so
// "Lỗi mở" and "LỖI MỞ" may both be stored.
func TestQASavedFilterCaseInsensitiveVietnamese(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	if _, err := st.CreateSavedFilter(admin.ID, "Lỗi mở", "is:open"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSavedFilter(admin.ID, "LỖI MỞ", "is:open"); !errors.Is(err, ErrConflict) {
		t.Errorf("Vietnamese name differing only in case: want ErrConflict, got %v", err)
	}
}

// QA: ordering. New filters go last even after reordering to negative or huge
// positions; position ties fall back to creation order, deterministically.
func TestQASavedFilterOrdering(t *testing.T) {
	st, admin, other := qaFilterUsers(t)
	var ids []string
	for _, n := range []string{"a", "b", "c"} {
		f, err := st.CreateSavedFilter(admin.ID, n, "is:open")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	// Another user's positions do not affect this user's.
	if f, _ := st.CreateSavedFilter(other.ID, "o", "is:open"); f.Position != 1000 {
		t.Errorf("other user's first position: %v", f.Position)
	}
	neg := -5.5
	if _, err := st.UpdateSavedFilter(admin.ID, ids[2], SavedFilterInput{Position: &neg}); err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateSavedFilter(admin.ID, "d", "is:open")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListSavedFilters(admin.ID)
	order := func(l []SavedFilter) string {
		var s []string
		for _, f := range l {
			s = append(s, f.Name)
		}
		return strings.Join(s, ",")
	}
	if got := order(list); got != "c,a,b,d" {
		t.Errorf("order after move: %s", got)
	}
	_ = d
	// Ties: give all the same position; order must be stable across reads.
	zero := 0.0
	for _, f := range list {
		if _, err := st.UpdateSavedFilter(admin.ID, f.ID, SavedFilterInput{Position: &zero}); err != nil {
			t.Fatal(err)
		}
	}
	first := order(mustList(t, st, admin.ID))
	for i := 0; i < 5; i++ {
		if got := order(mustList(t, st, admin.ID)); got != first {
			t.Fatalf("tie order not stable: %s vs %s", got, first)
		}
	}
	// Huge position: the next one is still after it.
	huge := 1e300
	if _, err := st.UpdateSavedFilter(admin.ID, ids[0], SavedFilterInput{Position: &huge}); err != nil {
		t.Fatal(err)
	}
	e, err := st.CreateSavedFilter(admin.ID, "e", "is:open")
	if err != nil {
		t.Fatal(err)
	}
	l := mustList(t, st, admin.ID)
	if l[len(l)-1].ID != e.ID {
		t.Errorf("new filter not last after a huge position: %s", order(l))
	}
}

func mustList(t *testing.T, st *Store, uid string) []SavedFilter {
	t.Helper()
	l, err := st.ListSavedFilters(uid)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// QA: a query naming a project is stored as is (filters have no project).
func TestQASavedFilterQueryEdgeCases(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	if _, err := st.CreateSavedFilter(admin.ID, "ops", "project:NOSUCH is:open"); err != nil {
		t.Errorf("project of any key: %v", err)
	}
	if _, err := st.CreateSavedFilter(admin.ID, "vi", `"lỗi: đăng nhập" label:bug`); err != nil {
		t.Errorf("quoted Vietnamese text: %v", err)
	}
	if _, err := st.CreateSavedFilter(admin.ID, "long", strings.Repeat("a", 1001)); !errors.Is(err, ErrValidation) {
		t.Errorf("query over 1000 bytes: %v", err)
	}
	if _, err := st.CreateSavedFilter(admin.ID, "unclosed", `"abc`); !errors.Is(err, ErrValidation) {
		t.Errorf("unclosed quote: %v", err)
	}
	// Unknown user id: the foreign key refuses it, not a silent orphan row.
	if _, err := st.CreateSavedFilter("no-such-user", "x", "is:open"); err == nil {
		t.Errorf("filter created for a user that does not exist")
	}
}

// QA: deleting a user deletes their filters (cascade) and only theirs.
func TestQASavedFilterDeleteUserCascade(t *testing.T) {
	st, admin, other := qaFilterUsers(t)
	third, _ := st.CreateUserDirect("third@velo.local", "Third", "secret123", "member")
	for _, u := range []*User{admin, other, third} {
		for _, n := range []string{"one", "two"} {
			if _, err := st.CreateSavedFilter(u.ID, n, "is:open"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.DeleteUser(admin.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM saved_filters WHERE user_id = ?`, other.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted user's filters left: %d %v", n, err)
	}
	for _, u := range []*User{admin, third} {
		if l := mustList(t, st, u.ID); len(l) != 2 {
			t.Errorf("filters of %s after another user's deletion: %+v", u.Email, l)
		}
	}
}

// QA: a database file created without saved_filters opens with the new build
// through Open (not just migrate on a live handle) and keeps its data.
func TestQASavedFilterMigrationReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.db.Exec(`DROP TABLE saved_filters`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen old db: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateSavedFilter(admin.ID, "x", "is:open"); err != nil {
		t.Fatalf("after reopen: %v", err)
	}
	st.Close()
	// Opening again is idempotent and keeps the filter.
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if l := mustList(t, st2, admin.ID); len(l) != 1 {
		t.Fatalf("filter lost on reopen: %+v", l)
	}
}

// QA: concurrent creates with the same name (in different cases): exactly one
// wins, the rest are ErrConflict, never another error. Concurrent creates
// with distinct names all succeed with distinct positions.
func TestQASavedFilterConcurrentCreates(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "Same"
			if i%2 == 1 {
				name = "SAME"
			}
			_, errs[i] = st.CreateSavedFilter(admin.ID, name, "is:open")
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrConflict):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly 1 success, got %d", ok)
	}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.CreateSavedFilter(admin.ID, "f"+string(rune('a'+i)), "is:open")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("distinct create: %v", err)
		}
	}
	seen := map[float64]bool{}
	for _, f := range mustList(t, st, admin.ID) {
		if seen[f.Position] {
			t.Errorf("duplicate position %v", f.Position)
		}
		seen[f.Position] = true
	}
}

// QA: concurrent renames of two filters to the same new name: one wins.
func TestQASavedFilterConcurrentRenames(t *testing.T) {
	st, admin, _ := qaFilterUsers(t)
	a, _ := st.CreateSavedFilter(admin.ID, "a", "is:open")
	b, _ := st.CreateSavedFilter(admin.ID, "b", "is:open")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{a.ID, b.ID} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			name := "Target"
			_, errs[i] = st.UpdateSavedFilter(admin.ID, id, SavedFilterInput{Name: &name})
		}(i, id)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrConflict) {
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("want 1 rename to win, got %d", ok)
	}
}
