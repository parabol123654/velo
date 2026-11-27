package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = saved
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// newDataDir makes a temp data directory with one unused upload and one
// orphan file, both two days old.
func newDataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	uploads := filepath.Join(data, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := db.Open(filepath.Join(data, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, n := range []string{"unused.png", "orphan.png"} {
		p := filepath.Join(uploads, n)
		if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	att, err := st.CreateAttachment(admin.ID, "", "unused.png", "image/png", "unused.png", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, old.UTC(), att.ID); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestQAUploadsBadArgs(t *testing.T) {
	data := newDataDir(t)
	for _, args := range [][]string{
		{},
		{"list"},
		{"--data", data},
		{"prune", "extra"},
		{"prune", "--data", data, "extra"},
		{"prune", "--bogus"},
		{"prune", "--data"},
	} {
		_, err := captureStdout(t, func() error { return runUploads(args) })
		if err == nil {
			t.Errorf("runUploads(%q): want an error", args)
		}
	}
	// Through Run, as main sees it (non-nil error means exit 1).
	if _, err := captureStdout(t, func() error { return Run([]string{"uploads"}) }); err == nil || !strings.Contains(err.Error(), "usage: velo uploads prune") {
		t.Errorf("Run uploads: %v", err)
	}
	// Bad args never delete anything.
	for _, n := range []string{"unused.png", "orphan.png"} {
		if _, err := os.Stat(filepath.Join(data, "uploads", n)); err != nil {
			t.Fatalf("%s removed by a rejected command", n)
		}
	}
}

func TestQAUploadsMissingDatabase(t *testing.T) {
	data := filepath.Join(t.TempDir(), "nope")
	_, err := captureStdout(t, func() error { return runUploads([]string{"prune", "--data", data}) })
	if err == nil || !strings.Contains(err.Error(), "no database at") {
		t.Fatalf("err = %v, want 'no database at'", err)
	}
	if _, err := os.Stat(data); err == nil {
		t.Fatal("missing data directory was created")
	}
	// Existing directory, no velo.db: no database gets created either.
	empty := t.TempDir()
	if _, err := captureStdout(t, func() error { return runUploads([]string{"prune", "--data", empty, "--apply"}) }); err == nil {
		t.Fatal("want an error for a directory without velo.db")
	}
	if _, err := os.Stat(filepath.Join(empty, "velo.db")); err == nil {
		t.Fatal("velo.db was created")
	}
}

func TestQAUploadsDryRunThenApply(t *testing.T) {
	data := newDataDir(t)
	uploads := filepath.Join(data, "uploads")

	out, err := captureStdout(t, func() error { return runUploads([]string{"prune", "--data", data}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unused.png", "orphan.png", "no row", "unused", "2 files (10 bytes) would be deleted", "--apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	for _, n := range []string{"unused.png", "orphan.png"} {
		if _, err := os.Stat(filepath.Join(uploads, n)); err != nil {
			t.Fatalf("dry run removed %s", n)
		}
	}

	// Flags may come in either order.
	out, err = captureStdout(t, func() error { return runUploads([]string{"prune", "--apply", "--data", data}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Deleted 2 files (10 bytes).") {
		t.Errorf("apply output:\n%s", out)
	}
	if entries, _ := os.ReadDir(uploads); len(entries) != 0 {
		t.Fatalf("files left after apply: %v", entries)
	}

	out, err = captureStdout(t, func() error { return runUploads([]string{"prune", "--data", data, "--apply"}) })
	if err != nil || strings.TrimSpace(out) != "Nothing to prune." {
		t.Fatalf("second apply: %q %v", out, err)
	}
}

// uploads/ missing next to an existing database is not an error.
func TestQAUploadsMissingUploadsDir(t *testing.T) {
	data := newDataDir(t)
	if err := os.RemoveAll(filepath.Join(data, "uploads")); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return runUploads([]string{"prune", "--data", data, "--apply"}) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "Deleted 1 files (0 bytes).") {
		t.Errorf("output:\n%s", out)
	}
}
