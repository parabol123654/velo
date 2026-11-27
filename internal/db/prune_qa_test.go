package db

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// QA for #4 (velo uploads prune). Every helper works in t.TempDir() only.

type pruneFixture struct {
	t     *testing.T
	st    *Store
	dir   string
	admin *User
	issue *Issue
}

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	st := newTestStore(t)
	admin, err := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "host"})
	if err != nil {
		t.Fatal(err)
	}
	return &pruneFixture{t: t, st: st, dir: t.TempDir(), admin: admin, issue: iss}
}

func (f *pruneFixture) writeFile(name string, age time.Duration) {
	f.t.Helper()
	p := filepath.Join(f.dir, name)
	if err := os.WriteFile(p, []byte("content-"+name), 0o644); err != nil {
		f.t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	if err := os.Chtimes(p, ts, ts); err != nil {
		f.t.Fatal(err)
	}
}

// insertRow writes an attachment row with a chosen id, issue and age.
func (f *pruneFixture) insertRow(id, issueID, storage string, age time.Duration) {
	f.t.Helper()
	var iss any
	if issueID != "" {
		iss = issueID
	}
	if _, err := f.st.db.Exec(`INSERT INTO attachments (id, issue_id, uploader_id, filename, content_type, size, storage_name, created_at)
		VALUES (?, ?, ?, ?, 'image/png', 5, ?, ?)`, id, iss, f.admin.ID, storage, storage, time.Now().Add(-age).UTC().Truncate(time.Second)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *pruneFixture) setDescription(issueID, text string) {
	f.t.Helper()
	if _, err := f.st.db.Exec(`UPDATE issues SET description = ? WHERE id = ?`, text, issueID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *pruneFixture) rowExists(id string) bool {
	f.t.Helper()
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = ?`, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n == 1
}

func (f *pruneFixture) fileExists(name string) bool {
	_, err := os.Lstat(filepath.Join(f.dir, name))
	return err == nil
}

func (f *pruneFixture) prune(apply bool) *PruneReport {
	f.t.Helper()
	rep, err := f.st.PruneUploads(f.dir, apply)
	if err != nil {
		f.t.Fatalf("PruneUploads(apply=%v): %v", apply, err)
	}
	return rep
}

func reportNames(r *PruneReport) []string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Name+"|"+f.Reason)
	}
	sort.Strings(out)
	return out
}

func wantReport(t *testing.T, r *PruneReport, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := reportNames(r)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("report = %v, want %v", got, want)
	}
}

const day = 24 * time.Hour

// A description that links the attachment with another filename after the
// id still counts as a use: the server serves it by id.
func TestQAPruneKeepsLinkWithOtherFilename(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("a.png", 2*day)
	att, err := f.st.CreateAttachment(f.admin.ID, "", "a.png", "image/png", "a.png", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, time.Now().Add(-2*day).UTC(), att.ID); err != nil {
		t.Fatal(err)
	}
	f.setDescription(f.issue.ID, "![renamed](/api/attachments/"+att.ID+"/totally-different%20name.jpg)")
	wantReport(t, f.prune(true))
	if !f.rowExists(att.ID) || !f.fileExists("a.png") {
		t.Fatal("linked attachment was deleted")
	}
	// An absolute URL in a comment also counts.
	f.setDescription(f.issue.ID, "")
	if _, err := f.st.db.Exec(`INSERT INTO comments (id, issue_id, author_id, body, via, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`,
		newID(), f.issue.ID, f.admin.ID, "https://velo.example.com/api/attachments/"+att.ID+"/x", nowUTC(), nowUTC()); err != nil {
		t.Fatal(err)
	}
	wantReport(t, f.prune(true))
}

// An id that is a prefix of another must not be kept alive by a link to the
// longer id, and the other way round.
func TestQAPruneIDPrefix(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("short.png", 2*day)
	f.writeFile("long.png", 2*day)
	f.insertRow("abc", "", "short.png", 2*day)
	f.insertRow("abcdef", "", "long.png", 2*day)

	f.setDescription(f.issue.ID, "/api/attachments/abcdef/long.png")
	wantReport(t, f.prune(false), "short.png|unused")

	f.setDescription(f.issue.ID, "/api/attachments/abc/short.png")
	wantReport(t, f.prune(false), "long.png|unused")

	// A link that merely contains the id as a suffix of a longer id.
	f.setDescription(f.issue.ID, "/api/attachments/00abc/short.png")
	wantReport(t, f.prune(false), "long.png|unused", "short.png|unused")
}

// LIKE treats _ and % as wildcards and is case-insensitive for ASCII. Ids
// are lowercase hex today, so this only documents behaviour on crafted rows:
// a wildcard can only keep a row too long, never delete a used one.
func TestQAPruneLikeWildcardIsConservative(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("w.png", 2*day)
	f.insertRow("a_c", "", "w.png", 2*day)
	f.setDescription(f.issue.ID, "/api/attachments/abc/x")
	rep := f.prune(false)
	if len(rep.Files) != 0 {
		t.Logf("note: wildcard id not kept by look-alike link: %v", reportNames(rep))
	}
	if !f.rowExists("a_c") {
		t.Fatal("dry run deleted a row")
	}
}

// Rows whose file is already gone are reported with 0 bytes and deleted
// without error.
func TestQAPruneRowWithMissingFile(t *testing.T) {
	f := newPruneFixture(t)
	f.insertRow("dead1", "", "gone.png", 2*day)
	rep := f.prune(false)
	wantReport(t, rep, "gone.png|unused")
	if rep.Bytes != 0 || rep.Files[0].Bytes != 0 {
		t.Fatalf("bytes = %d/%d, want 0", rep.Bytes, rep.Files[0].Bytes)
	}
	f.prune(true)
	if f.rowExists("dead1") {
		t.Fatal("row with missing file not deleted")
	}
	wantReport(t, f.prune(true))
}

// Symlinks and directories with no row are never touched, and neither are
// the files a symlink points to.
func TestQAPruneSkipsSymlinksAndDirs(t *testing.T) {
	f := newPruneFixture(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * day)
	_ = os.Chtimes(target, old, old)
	if err := os.Symlink(target, filepath.Join(f.dir, "link.png")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, "full"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "full", "inner.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"empty", "full", "full/inner.png"} {
		_ = os.Chtimes(filepath.Join(f.dir, n), old, old)
	}

	wantReport(t, f.prune(true))
	for _, n := range []string{"link.png", "dirlink", "empty", "full", "full/inner.png"} {
		if !f.fileExists(n) {
			t.Fatalf("%s removed", n)
		}
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep me" {
		t.Fatalf("symlink target changed: %q %v", b, err)
	}
}

// An unused row whose storage name is a symlink: only the link goes, never
// what it points to.
func TestQAPruneUnusedRowSymlinkRemovesOnlyLink(t *testing.T) {
	f := newPruneFixture(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.dir, "s.png")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f.insertRow("sym1", "", "s.png", 2*day)
	f.prune(true)
	if f.fileExists("s.png") {
		t.Fatal("link left")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep me" {
		t.Fatalf("symlink target changed: %q %v", b, err)
	}
}

// uploads/ missing: dry run and apply both succeed; unused rows still go.
func TestQAPruneMissingUploadsDir(t *testing.T) {
	f := newPruneFixture(t)
	missing := filepath.Join(f.dir, "does-not-exist")
	f.insertRow("r1", "", "x.png", 2*day)
	rep, err := f.st.PruneUploads(missing, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	wantReport(t, rep, "x.png|unused")
	if _, err := f.st.PruneUploads(missing, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if f.rowExists("r1") {
		t.Fatal("row kept")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("prune created the uploads directory")
	}
}

// uploadsDir that is a file, not a directory, is an error and deletes nothing.
func TestQAPruneUploadsDirIsFile(t *testing.T) {
	f := newPruneFixture(t)
	notDir := filepath.Join(f.dir, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.insertRow("r1", "", "x.png", 2*day)
	if _, err := f.st.PruneUploads(notDir, true); err == nil {
		t.Fatal("expected an error for a non-directory uploads path")
	}
	if !f.rowExists("r1") {
		t.Fatal("row deleted although prune failed")
	}
}

// Database made through the real flow: attachment created with an issue,
// then the issue deleted the way older versions did it.
func TestQAPruneOldStyleIssueDeletion(t *testing.T) {
	f := newPruneFixture(t)
	other, err := f.st.CreateIssue(f.admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) *Attachment {
		f.writeFile(name, 2*day)
		a, err := f.st.CreateAttachment(f.admin.ID, other.ID, name, "image/png", name, 5)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.db.Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, time.Now().Add(-2*day).UTC(), a.ID); err != nil {
			t.Fatal(err)
		}
		return a
	}
	gone := mk("gone.png")
	quoted := mk("quoted.png")
	f.setDescription(other.ID, "![x]("+gone.URL+") ![y]("+quoted.URL+")")
	f.setDescription(f.issue.ID, "copied from victim: "+quoted.URL)

	// Old way 1: the row is unlinked by hand.
	if _, err := f.st.db.Exec(`UPDATE attachments SET issue_id = NULL WHERE issue_id = ?`, other.ID); err != nil {
		t.Fatal(err)
	}
	// Victim's own description still links both, so nothing is unused yet.
	wantReport(t, f.prune(false))

	// Old way 2: the issue row itself is deleted (FK sets issue_id NULL).
	if _, err := f.st.db.Exec(`DELETE FROM issues WHERE id = ?`, other.ID); err != nil {
		t.Fatal(err)
	}
	wantReport(t, f.prune(false), "gone.png|unused")
	f.prune(true)
	if f.rowExists(gone.ID) || f.fileExists("gone.png") {
		t.Fatal("unused upload left")
	}
	if !f.rowExists(quoted.ID) || !f.fileExists("quoted.png") {
		t.Fatal("upload quoted by a surviving issue was deleted")
	}

	// Current deletion path leaves nothing for prune to find.
	keep, err := f.st.CreateIssue(f.admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "new"})
	if err != nil {
		t.Fatal(err)
	}
	f.writeFile("new.png", 2*day)
	if _, err := f.st.CreateAttachment(f.admin.ID, keep.ID, "new.png", "image/png", "new.png", 5); err != nil {
		t.Fatal(err)
	}
	names, err := f.st.DeleteIssue(keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		_ = os.Remove(filepath.Join(f.dir, n))
	}
	wantReport(t, f.prune(false))
}

// The 24h cutoff, for rows and files, under several local time zones.
func TestQAPruneCutoffAcrossTimeZones(t *testing.T) {
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	for _, zone := range []string{"UTC", "Asia/Ho_Chi_Minh", "America/Los_Angeles", "Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		t.Run(zone, func(t *testing.T) {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Skipf("no tzdata for %s: %v", zone, err)
			}
			time.Local = loc
			f := newPruneFixture(t)
			// Rows written through CreateAttachment (UTC) then aged.
			age := func(name string, d time.Duration) string {
				a, err := f.st.CreateAttachment(f.admin.ID, "", name, "image/png", name, 5)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.db.Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, nowUTC().Add(-d), a.ID); err != nil {
					t.Fatal(err)
				}
				return a.ID
			}
			age("row_23h50.png", day-10*time.Minute)
			age("row_24h10.png", day+10*time.Minute)
			age("row_23h.png", day-time.Hour)
			age("row_25h.png", day+time.Hour)
			f.writeFile("file_23h50.png", day-10*time.Minute)
			f.writeFile("file_24h10.png", day+10*time.Minute)
			wantReport(t, f.prune(false), "row_24h10.png|unused", "row_25h.png|unused", "file_24h10.png|no row")
		})
	}
}

// The cutoff is compared as text, so it only works while every created_at
// is written in UTC. Every version of CreateAttachment writes nowUTC(); this
// pins that down even when the server runs in a non-UTC zone.
func TestQAPruneCreatedAtStoredInUTC(t *testing.T) {
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("UTC-11", -11*3600)
	f := newPruneFixture(t)
	a, err := f.st.CreateAttachment(f.admin.ID, "", "z.png", "image/png", "z.png", 5)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.st.db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM attachments WHERE id = ?`, a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cutoff string
	if err := f.st.db.QueryRow(`SELECT CAST(? AS TEXT)`, time.Now().UTC()).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	// Same layout and zone suffix as the cutoff PruneUploads binds.
	if len(raw) < 19 || len(cutoff) < 19 || raw[10] != cutoff[10] || !strings.HasSuffix(raw, cutoff[len(cutoff)-6:]) {
		t.Fatalf("created_at %q and cutoff %q are not comparable as text", raw, cutoff)
	}
}

// Running prune twice: the second run finds nothing and fails on nothing.
func TestQAPruneTwice(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("orphan.png", 2*day)
	f.writeFile("u.png", 2*day)
	f.insertRow("u1", "", "u.png", 2*day)
	if r := f.prune(true); len(r.Files) != 2 {
		t.Fatalf("first run: %v", reportNames(r))
	}
	wantReport(t, f.prune(true))
	wantReport(t, f.prune(false))
}

func snapshot(t *testing.T, st *Store, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, _ := os.Lstat(p)
		fmt.Fprintf(&b, "%s %v %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		if fi.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			sum := sha256.Sum256(data)
			b.WriteString(" " + hex.EncodeToString(sum[:]))
		}
		b.WriteString("\n")
		return nil
	})
	for _, q := range []string{
		`SELECT id, COALESCE(issue_id,''), uploader_id, filename, content_type, size, storage_name, created_at FROM attachments ORDER BY id`,
		`SELECT id, description FROM issues ORDER BY id`,
		`SELECT id, body FROM comments ORDER BY id`,
	} {
		rows, err := st.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", vals)
		}
		rows.Close()
	}
	return b.String()
}

// A dry run leaves every file (content, mode, mtime) and every row as it was.
func TestQAPruneDryRunChangesNothing(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("orphan.png", 2*day)
	f.writeFile("fresh.png", time.Minute)
	f.writeFile("u.png", 2*day)
	f.insertRow("u1", "", "u.png", 2*day)
	f.insertRow("missing", "", "nofile.png", 2*day)
	f.writeFile("linked.png", 2*day)
	f.insertRow("l1", f.issue.ID, "linked.png", 2*day)
	_ = os.Mkdir(filepath.Join(f.dir, "sub"), 0o755)

	before := snapshot(t, f.st, f.dir)
	for i := 0; i < 3; i++ {
		if r := f.prune(false); len(r.Files) != 3 {
			t.Fatalf("dry run %d: %v", i, reportNames(r))
		}
	}
	if after := snapshot(t, f.st, f.dir); after != before {
		t.Fatalf("dry run changed state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Report sizes match the files on disk; Bytes is their sum.
func TestQAPruneReportBytes(t *testing.T) {
	f := newPruneFixture(t)
	f.writeFile("orphan.png", 2*day) // "content-orphan.png" = 18 bytes
	f.writeFile("u.png", 2*day)      // "content-u.png" = 13 bytes
	f.insertRow("u1", "", "u.png", 2*day)
	r := f.prune(false)
	var sum int64
	for _, pf := range r.Files {
		fi, err := os.Stat(filepath.Join(f.dir, pf.Name))
		if err != nil || fi.Size() != pf.Bytes {
			t.Fatalf("%s: report %d bytes, disk %v %v", pf.Name, pf.Bytes, fi, err)
		}
		sum += pf.Bytes
	}
	if r.Bytes != sum || sum != 31 {
		t.Fatalf("Bytes = %d, sum %d, want 31", r.Bytes, sum)
	}
}
