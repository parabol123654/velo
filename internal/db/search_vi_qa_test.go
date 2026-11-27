package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

// QA tests for Vietnamese accent folding in search (gap 8).

// ftsIntegrity runs FTS5's integrity-check, which compares the index with
// the external content (the issues_fts_src view). A mismatch means a
// trigger indexed something the view does not produce.
func ftsIntegrity(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO issues_fts(issues_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Fatalf("fts integrity-check: %v", err)
	}
}

func mustIssue(t *testing.T, st *Store, actor, title, desc string) *Issue {
	t.Helper()
	it, err := st.CreateIssue(actor, CreateIssueInput{ProjectRef: "DEV", Title: title, Description: desc})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return it
}

func TestQASearchHornAndToneLetters(t *testing.T) {
	st, admin := viStore(t)
	mustIssue(t, st, admin.ID, "Người dùng không thể đổi ảnh", "")
	mustIssue(t, st, admin.ID, "ƯU TIÊN CAO: Ổ ĐĨA đầy", "")
	mustIssue(t, st, admin.ID, "Lỗi ở trang chủ", "")
	for q, want := range map[string]string{
		"nguoi dung":    "Người dùng không thể đổi ảnh",
		"khong the":     "Người dùng không thể đổi ảnh",
		"doi anh":       "Người dùng không thể đổi ảnh",
		"uu tien":       "ƯU TIÊN CAO: Ổ ĐĨA đầy",
		"o dia day":     "ƯU TIÊN CAO: Ổ ĐĨA đầy",
		"ĐĨA":           "ƯU TIÊN CAO: Ổ ĐĨA đầy",
		"loi trang chu": "Lỗi ở trang chủ",
		"LOI":           "Lỗi ở trang chủ",
	} {
		got := searchTitles(t, st, q)
		if len(got) != 1 || !got[want] {
			t.Errorf("search %q found %v, want only %q", q, got, want)
		}
	}
	ftsIntegrity(t, st)
}

// NFD: macOS and some IMEs send decomposed text (base letter + combining marks).
func TestQASearchDecomposedInput(t *testing.T) {
	st, admin := viStore(t)
	nfcTitle := "đăng nhập"                        // precomposed
	nfdTitle := "Ca\u0306\u0323p nha\u0302\u0323t" // "Cặp nhật" decomposed
	mustIssue(t, st, admin.ID, nfcTitle, "")
	mustIssue(t, st, admin.ID, nfdTitle, "")

	// Decomposed query finds precomposed text.
	if got := searchTitles(t, st, "đa\u0306ng nha\u0302\u0323p"); !got[nfcTitle] || len(got) != 1 {
		t.Errorf("NFD query: %v", got)
	}
	// Plain query finds decomposed text.
	if got := searchTitles(t, st, "cap nhat"); !got[nfdTitle] || len(got) != 1 {
		t.Errorf("plain query over NFD title: %v", got)
	}
	// Precomposed query finds decomposed text.
	if got := searchTitles(t, st, "cặp nhật"); !got[nfdTitle] || len(got) != 1 {
		t.Errorf("NFC query over NFD title: %v", got)
	}
	ftsIntegrity(t, st)
}

// FTS syntax characters and quotes must never reach MATCH as syntax.
func TestQASearchSyntaxCharactersNeverError(t *testing.T) {
	st, admin := viStore(t)
	mustIssue(t, st, admin.ID, "đăng nhập (OAuth) \"Google\"", "x AND y OR NOT z")
	for _, q := range []string{
		`"đăng`, `đăng"`, `"đăng nhập"`, `đăng*`, `*`, `-`, `-đăng`,
		`(đăng`, `đăng)`, `đăng AND`, `OR`, `NOT`, `NEAR(đăng nhập)`, `title:đăng`,
		`{title}: đăng`, `^đăng`, `đăng + nhập`, `'`, `%`, `_`, `\`, `đ`, `Đ`, `ð`,
		"\u0323", "\u0300\u0301", `đ:đ`, `🙂`, `.`, `đ-đ`,
	} {
		if _, err := st.SearchIssues(q, 10, nil); err != nil {
			t.Errorf("search %q: %v", q, err)
		}
	}
	// Quoted and parenthesised words still find the issue.
	for _, q := range []string{`"dang nhap"`, `(oauth)`, `"google"`, `dang*`, `NOT`} {
		if got := searchTitles(t, st, q); len(got) != 1 {
			t.Errorf("search %q found %v", q, got)
		}
	}
}

// A query of only quotes (no FTS terms left) or with a NUL byte returns no
// hits, not an error. Both used to fail with "SQL logic error".
func TestQASearchQuoteOnlyQueryReturnsNoError(t *testing.T) {
	st, admin := viStore(t)
	mustIssue(t, st, admin.ID, "đăng nhập", "")
	for _, q := range []string{`"`, `""`, `" "`, "đăng\x00nhập"} {
		if _, err := st.SearchIssues(q, 10, nil); err != nil {
			t.Errorf("search %q: %v", q, err)
		}
	}
}

func TestQASearchIdentifierAndCase(t *testing.T) {
	st, admin := viStore(t)
	for i := 0; i < 12; i++ {
		mustIssue(t, st, admin.ID, fmt.Sprintf("Việc số %d", i+1), "")
	}
	for q, want := range map[string]string{"DEV-1": "DEV-1", "dev-1": "DEV-1", "Dev-12": "DEV-12", "dev 12": "DEV-12"} {
		hits, err := st.SearchIssues(q, 50, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, h := range hits {
			if h.Identifier == want {
				found = true
			}
		}
		if !found {
			t.Errorf("search %q did not find %s", q, want)
		}
	}
	if got := searchTitles(t, st, "VIỆC SỐ"); len(got) != 12 {
		t.Errorf("upper-case query found %d, want 12", len(got))
	}
}

func TestQASearchManyIssuesAndLimit(t *testing.T) {
	st, admin := viStore(t)
	for i := 0; i < 60; i++ {
		mustIssue(t, st, admin.ID, fmt.Sprintf("Đơn hàng %d", i), "giao hàng nhanh")
	}
	hits, err := st.SearchIssues("don hang", 50, nil)
	if err != nil || len(hits) != 50 {
		t.Fatalf("limit 50: %d %v", len(hits), err)
	}
	all, err := st.ListIssues(IssueFilter{Query: "giao hang", Limit: 1000})
	if err != nil || len(all) != 60 {
		t.Fatalf("list all: %d %v", len(all), err)
	}
	ftsIntegrity(t, st)
}

// Updating columns that are not indexed must leave the index consistent.
func TestQASearchNonIndexedUpdates(t *testing.T) {
	st, admin := viStore(t)
	mustIssue(t, st, admin.ID, "Đặt lịch họp", "Phòng đôi")
	prio := 3
	if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Priority: &prio}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE issues SET position = position + 1`); err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, st)
	if got := searchTitles(t, st, "dat lich phong doi"); len(got) != 1 {
		t.Errorf("after non-indexed update: %v", got)
	}
	// Changing only the description, from đ to Đ and back.
	for _, d := range []string{"PHÒNG ĐÔI", "phòng đơn", ""} {
		d := d
		if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Description: &d}); err != nil {
			t.Fatal(err)
		}
		ftsIntegrity(t, st)
	}
	if got := searchTitles(t, st, "phong"); len(got) != 0 {
		t.Errorf("old description still indexed: %v", got)
	}
}

func TestQASearchDeleteProjectCascades(t *testing.T) {
	st, admin := viStore(t)
	if _, err := st.CreateProject("OPS", "Ops", ""); err != nil {
		t.Fatal(err)
	}
	mustIssue(t, st, admin.ID, "Đăng nhập DEV", "")
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "OPS", Title: "Đăng nhập OPS"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteProject("OPS"); err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, st)
	if got := searchTitles(t, st, "dang nhap"); len(got) != 1 || !got["Đăng nhập DEV"] {
		t.Errorf("after project delete: %v", got)
	}
	// A new issue may reuse the rowid of a deleted one.
	if _, err := st.CreateProject("OPS", "Ops", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "OPS", Title: "Khác hẳn"}); err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, st)
	if got := searchTitles(t, st, "dang nhap"); len(got) != 1 {
		t.Errorf("stale row after rowid reuse: %v", got)
	}
}

func TestQASearchBackupThenOpen(t *testing.T) {
	st, admin := viStore(t)
	mustIssue(t, st, admin.ID, "Sao lưu dữ liệu", "Đồng bộ")
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(path); err != nil {
		t.Fatal(err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer b.Close()
	ftsIntegrity(t, b)
	if got := searchTitles(t, b, "sao luu dong bo"); len(got) != 1 {
		t.Errorf("backup search: %v", got)
	}
	// The backup keeps indexing new issues.
	mustIssue(t, b, admin.ID, "Đã khôi phục", "")
	if got := searchTitles(t, b, "da khoi phuc"); len(got) != 1 {
		t.Errorf("backup new issue: %v", got)
	}
	ftsIntegrity(t, b)
}

// A database file made by the previous release, with many issues, opens
// with the new build and is reindexed.
func TestQASearchOldDatabaseFileReopened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(oldFTSSQL); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		mustIssue(t, st, admin.ID, fmt.Sprintf("Đăng nhập lần %d", i), "Mô tả đầy đủ")
	}
	// An edit and a delete under the old triggers.
	title := "Đổi mật khẩu"
	if _, err := st.UpdateIssue(admin.ID, "DEV-1", UpdateIssueInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteIssue("DEV-2"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen old db: %v", err)
	}
	defer st.Close()
	ftsIntegrity(t, st)
	all, err := st.ListIssues(IssueFilter{Query: "dang nhap", Limit: 1000})
	if err != nil || len(all) != 48 {
		t.Fatalf("after reopen: %d %v", len(all), err)
	}
	if got := searchTitles(t, st, "doi mat khau"); len(got) != 1 {
		t.Errorf("edited title: %v", got)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'issues_fts_src' AND type = 'view'`).Scan(&n)
	if n != 1 {
		t.Errorf("view missing after reopen")
	}
	// A third open does not rebuild again and still works.
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, st)
}
