package cli

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/web"
)

func importServer(t *testing.T) (*db.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := db.Open(filepath.Join(dir, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("admin@velo.dev", "Admin", "secret123")
	tok, _, _ := st.CreateAPIToken(admin.ID, "cli", db.TokenScope{})
	if _, err := st.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, dir, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return st, ts.URL, tok
}

func writeBatch(t *testing.T, b *db.ImportBatch) string {
	t.Helper()
	raw, _ := json.Marshal(b)
	path := filepath.Join(t.TempDir(), "batch.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// #16: velo import file sends a batch through the API: a dry run writes
// nothing, a rerun skips what is there, and problems come back as errors.
func TestImportFileCommand(t *testing.T) {
	st, url, tok := importServer(t)
	path := writeBatch(t, &db.ImportBatch{Source: "file", Issues: []db.ImportIssue{
		{ExternalID: "A-1", Title: "Parent", Labels: []string{"Lỗi"}},
		{ExternalID: "A-2", Title: "Child", Parent: "A-1", Assignee: "nobody@x.dev"},
	}})
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"import", "file"}, append(args, "--url", url, "--token", tok)...))
		})
	}
	out, err := run(path, "--project", "dev", "--dry-run")
	if err != nil || !strings.Contains(out, "Would import 2 issues") || !strings.Contains(out, "New labels: Lỗi") {
		t.Fatalf("dry run: %q %v", out, err)
	}
	if issues, _ := st.ListIssues(db.IssueFilter{ProjectRef: "DEV"}); len(issues) != 0 {
		t.Fatalf("dry run wrote %d issues", len(issues))
	}
	out, err = run("--project", "DEV", path)
	if err != nil || !strings.Contains(out, "Imported 2 issues (0 already imported)") || !strings.Contains(out, "A-2: nobody@x.dev") {
		t.Fatalf("import: %q %v", out, err)
	}
	out, err = run(path, "--project", "DEV")
	if err != nil || !strings.Contains(out, "Imported 0 issues (2 already imported)") {
		t.Fatalf("rerun: %q %v", out, err)
	}
	if iss, _ := st.GetIssue("DEV-2"); iss.ParentIdentifier != "DEV-1" {
		t.Fatalf("parent: %+v", iss)
	}

	bad := writeBatch(t, &db.ImportBatch{Source: "file", Issues: []db.ImportIssue{{ExternalID: "B-1", Title: "x", State: "Nope"}}})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{path}, "--project is required"},
		{[]string{"--project", "DEV"}, "usage"},
		{[]string{bad, "--project", "DEV"}, `state "Nope" is not a state of DEV`},
		{[]string{path, "--project", "NOPE"}, "404"},
		{[]string{filepath.Join(t.TempDir(), "missing.json"), "--project", "DEV"}, "no such file"},
	} {
		if _, err := run(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
	if err := Run([]string{"import", "trello"}); err == nil || !strings.Contains(err.Error(), `unknown import source "trello"`) {
		t.Fatalf("unknown source: %v", err)
	}
}

// A batch bigger than one request is sent in parts, parents before their
// children and links last, so references across parts resolve.
func TestImportSplitsBigBatches(t *testing.T) {
	st, url, tok := importServer(t)
	n := importPartIssues + 200
	b := &db.ImportBatch{Source: "file"}
	for i := 1; i <= n; i++ {
		it := db.ImportIssue{ExternalID: fmt.Sprintf("X-%d", i), Title: fmt.Sprintf("Issue %d", i)}
		if i == 1 {
			it.Parent = fmt.Sprintf("X-%d", n) // parent comes last in the source
		}
		b.Issues = append(b.Issues, it)
	}
	b.Links = []db.ImportLink{{Source: "X-2", Kind: "blocks", Target: fmt.Sprintf("X-%d", n-1)}}

	parts, err := splitImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || len(parts[0].Links) != 0 || len(parts[1].Links) != 1 {
		t.Fatalf("parts: %d, links %d/%d", len(parts), len(parts[0].Links), len(parts[len(parts)-1].Links))
	}
	if parts[0].Issues[0].ExternalID != fmt.Sprintf("X-%d", n) || parts[0].Issues[1].ExternalID != "X-1" {
		t.Fatalf("parent not first: %s, %s", parts[0].Issues[0].ExternalID, parts[0].Issues[1].ExternalID)
	}

	path := writeBatch(t, b)
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"import", "file", path, "--project", "DEV", "--url", url, "--token", tok}, args...))
		})
	}
	if out, err := run("--dry-run"); err != nil || !strings.Contains(out, fmt.Sprintf("Would import %d issues", n)) {
		t.Fatalf("dry run in parts: %q %v", out, err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, fmt.Sprintf("Imported %d issues", n)) || !strings.Contains(out, "1 links") {
		t.Fatalf("import in parts: %q %v", out, err)
	}
	child, _ := st.GetIssue("DEV-2") // X-1 is sent second, after its parent
	if child.Title != "Issue 1" || child.ParentIdentifier != "DEV-1" {
		t.Fatalf("parent across the batch: %+v", child)
	}
}
