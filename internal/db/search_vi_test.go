package db

import (
	"testing"
)

// searchTitles returns the titles a search finds.
func searchTitles(t *testing.T, st *Store, q string) map[string]bool {
	t.Helper()
	hits, err := st.SearchIssues(q, 50, nil)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	out := map[string]bool{}
	for _, h := range hits {
		out[h.Title] = true
	}
	return out
}

func viStore(t *testing.T) (*Store, *User) {
	t.Helper()
	st := newTestStore(t)
	admin, _ := st.CreateFirstAdmin("admin@velo.local", "Admin", "secret123")
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	return st, admin
}

func TestVietnameseSearchFoldsAccents(t *testing.T) {
	st, admin := viStore(t)
	for _, title := range []string{"đăng nhập bằng Google", "ĐĂNG NHẬP lỗi", "Cập nhật hồ sơ", "Unrelated"} {
		if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "In description", Description: "Màn hình Đăng ký"}); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"dang nhap": {"đăng nhập bằng Google", "ĐĂNG NHẬP lỗi"},
		"đăng nhập": {"đăng nhập bằng Google", "ĐĂNG NHẬP lỗi"},
		"DANG":      {"đăng nhập bằng Google", "ĐĂNG NHẬP lỗi", "In description"},
		"cap nhat":  {"Cập nhật hồ sơ"},
		"câp":       {"Cập nhật hồ sơ"},
		"ho so":     {"Cập nhật hồ sơ"},
		"dang ky":   {"In description"},
	} {
		got := searchTitles(t, st, q)
		if len(got) != len(want) {
			t.Errorf("search %q found %v, want %v", q, got, want)
			continue
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("search %q found %v, want %v", q, got, want)
			}
		}
	}

	// The index follows edits and deletes.
	title := "Đổi mật khẩu"
	if _, err := st.UpdateIssue(admin.ID, "DEV-4", UpdateIssueInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if got := searchTitles(t, st, "doi mat khau"); !got[title] || len(got) != 1 {
		t.Errorf("after edit: %v", got)
	}
	if err := st.DeleteIssue("DEV-1"); err != nil {
		t.Fatal(err)
	}
	// The 'delete' values the triggers send match what was indexed.
	ftsIntegrity(t, st)
	if got := searchTitles(t, st, "dang nhap"); got["đăng nhập bằng Google"] || !got["ĐĂNG NHẬP lỗi"] {
		t.Errorf("after delete: %v", got)
	}
}

// The search index as the previous release created it.
const oldFTSSQL = `
DROP TRIGGER IF EXISTS issues_ai;
DROP TRIGGER IF EXISTS issues_ad;
DROP TRIGGER IF EXISTS issues_au;
DROP TABLE IF EXISTS issues_fts;
DROP VIEW IF EXISTS issues_fts_src;
CREATE VIRTUAL TABLE issues_fts USING fts5(identifier, title, description, content='issues', content_rowid='rowid');
CREATE TRIGGER issues_ai AFTER INSERT ON issues BEGIN
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier, new.title, new.description);
END;
CREATE TRIGGER issues_ad AFTER DELETE ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier, old.title, old.description);
END;
CREATE TRIGGER issues_au AFTER UPDATE OF identifier, title, description ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier, old.title, old.description);
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier, new.title, new.description);
END;
INSERT INTO issues_fts(issues_fts) VALUES ('rebuild');
`

func TestOldSearchIndexIsRebuilt(t *testing.T) {
	st, admin := viStore(t)
	if _, err := st.db.Exec(oldFTSSQL); err != nil {
		t.Fatal(err)
	}
	// Written while the old index is in place.
	if _, err := st.CreateIssue(admin.ID, CreateIssueInput{ProjectRef: "DEV", Title: "Đăng nhập chậm"}); err != nil {
		t.Fatal(err)
	}
	if got := searchTitles(t, st, "dang nhap"); len(got) != 0 {
		t.Fatalf("old index already folds: %v", got)
	}
	if err := st.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got := searchTitles(t, st, "dang nhap"); !got["Đăng nhập chậm"] {
		t.Fatalf("after migrate: %v", got)
	}
	// Running it again changes nothing.
	if err := st.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := searchTitles(t, st, "dang nhap"); len(got) != 1 {
		t.Fatalf("after second migrate: %v", got)
	}
	ftsIntegrity(t, st)
}
