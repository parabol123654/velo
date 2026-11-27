package db

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// #4: uploads left behind by older versions are found, reported by a dry
// run, and removed only with apply; anything still used is kept.
func TestPruneUploads(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	file := func(name string, aged bool) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		if aged {
			_ = os.Chtimes(path, old, old)
		}
	}
	// A row (with its file) that no issue owns.
	unlinked := func(name string, aged bool) *Attachment {
		file(name, aged)
		att, err := st.CreateAttachment(admin.ID, "", name, "image/png", name, 5)
		if err != nil {
			t.Fatal(err)
		}
		if aged {
			if _, err := st.db.Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, old.UTC(), att.ID); err != nil {
				t.Fatal(err)
			}
		}
		return att
	}

	file("orphan_old.png", true)  // no row: removed
	file("orphan_new.png", false) // no row, but may be an upload in progress: kept
	unlinked("unused_old.png", true)
	inDesc := unlinked("in_desc.png", true)
	inComment := unlinked("in_comment.png", true)
	unlinked("unused_new.png", false) // may be pasted into a form not saved yet
	iss, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	file("linked.png", true)
	if _, err := st.CreateAttachment(admin.ID, iss.ID, "linked.png", "image/png", "linked.png", 5); err != nil {
		t.Fatal(err)
	}
	// Left unlinked as older versions did: quoted by someone other than the uploader.
	if _, err := st.db.Exec(`UPDATE issues SET description = ? WHERE id = ?`, "see "+inDesc.URL, iss.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO comments (id, issue_id, author_id, body, via, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`,
		newID(), iss.ID, admin.ID, "see "+inComment.URL, nowUTC(), nowUTC()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil { // never touched
		t.Fatal(err)
	}

	names := func(r *PruneReport) []string {
		var out []string
		for _, f := range r.Files {
			out = append(out, f.Name)
		}
		sort.Strings(out)
		return out
	}
	want := []string{"orphan_old.png", "unused_old.png"}

	dry, err := st.PruneUploads(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(dry); len(got) != 2 || got[0] != want[0] || got[1] != want[1] || dry.Bytes != 10 {
		t.Fatalf("dry run found %v (%d bytes), want %v (10 bytes)", got, dry.Bytes, want)
	}
	for _, n := range want {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("dry run removed %s", n)
		}
	}

	done, err := st.PruneUploads(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(done); len(got) != 2 {
		t.Fatalf("apply removed %v", got)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	wantLeft := []string{"in_comment.png", "in_desc.png", "linked.png", "orphan_new.png", "subdir", "unused_new.png"}
	if len(left) != len(wantLeft) {
		t.Fatalf("left %v, want %v", left, wantLeft)
	}
	for i := range left {
		if left[i] != wantLeft[i] {
			t.Fatalf("left %v, want %v", left, wantLeft)
		}
	}
	var rows int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&rows)
	if rows != 4 { // in_desc, in_comment, unused_new, linked
		t.Fatalf("%d attachment rows left, want 4", rows)
	}
	again, err := st.PruneUploads(dir, true)
	if err != nil || len(again.Files) != 0 {
		t.Fatalf("second run: %v %v", again, err)
	}
}
