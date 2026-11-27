package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #16: velo import linear reads a CSV export and imports it; a rerun adds nothing.
func TestImportLinearCommand(t *testing.T) {
	st, url, tok := importServer(t)
	csv := "ID,Title,Description,Status,Priority,Creator,Assignee,Labels,Created,Started,Completed,Canceled,Parent issue\n" +
		"ENG-1,Đăng nhập lỗi,Steps,In Progress,Urgent,alice,admin@velo.dev,\"Bug, Lỗi\",2024-03-02T10:00:00.000Z,2024-03-03T10:00:00.000Z,,,\n" +
		"ENG-2,Child,,Todo,Low,alice,,,2024-03-02T10:00:00.000Z,,,,ENG-1\n"
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"import", "linear", path, "--project", "DEV", "--url", url, "--token", tok}, args...))
		})
	}
	out, err := run("--linear-workspace", "acme")
	if err != nil || !strings.Contains(out, "Imported 2 issues") || !strings.Contains(out, "New labels: Lỗi") {
		t.Fatalf("import: %q %v", out, err)
	}
	one, _ := st.GetIssue("DEV-1")
	if one.StateName != "In Progress" || one.Priority != 4 || one.AssigneeName != "Admin" || !strings.Contains(one.Description, "https://linear.app/acme/issue/ENG-1") {
		t.Fatalf("DEV-1: %+v", one)
	}
	if two, _ := st.GetIssue("DEV-2"); two.ParentIdentifier != "DEV-1" || two.StateName != "Todo" {
		t.Fatalf("DEV-2: %+v", two)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Imported 0 issues (2 already imported)") {
		t.Fatalf("rerun: %q %v", out, err)
	}
}
