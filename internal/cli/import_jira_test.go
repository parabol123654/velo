package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #16: velo import jira reads a CSV export and imports it, mapping users and
// statuses; a rerun adds nothing.
func TestImportJiraCommand(t *testing.T) {
	st, url, tok := importServer(t)
	if _, err := st.CreateUserDirect("a@velo.dev", "An", "secret123", "member"); err != nil {
		t.Fatal(err)
	}
	csv := "Summary,Issue key,Issue id,Status,Status Category,Priority,Assignee,Created,Comment,Parent,Outward issue link (Blocks)\n" +
		"Đăng nhập lỗi,WEB-1,1,QA,In Progress,High,Nguyễn Văn A,02/Mar/24 10:00 AM,\"02/Mar/24 11:00 AM;x;Repro\",,WEB-2\n" +
		"Child,WEB-2,2,To Do,To Do,Low,,02/Mar/24 10:00 AM,,1,\n"
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"import", "jira", path, "--url", url, "--token", tok}, args...))
		})
	}
	out, err := run("--project", "DEV", "--map-user", "nguyễn văn a=a@velo.dev", "--state-map", "qa=In Review", "--jira-url", "https://acme.atlassian.net")
	if err != nil || !strings.Contains(out, "Imported 2 issues") || !strings.Contains(out, "1 comments, 1 links") {
		t.Fatalf("import: %q %v", out, err)
	}
	one, _ := st.GetIssue("DEV-1")
	if one.StateName != "In Review" || one.Priority != 3 || one.AssigneeName != "An" || !strings.Contains(one.Description, "[WEB-1](https://acme.atlassian.net/browse/WEB-1)") {
		t.Fatalf("DEV-1: %+v", one)
	}
	if two, _ := st.GetIssue("DEV-2"); two.ParentIdentifier != "DEV-1" || two.BlockedByCount != 1 {
		t.Fatalf("DEV-2: %+v", two)
	}
	if out, err := run("--project", "DEV"); err != nil || !strings.Contains(out, "Imported 0 issues (2 already imported), 0 comments, 0 links") {
		t.Fatalf("rerun: %q %v", out, err)
	}
	if _, err := run("--project", "DEV", "--state-map", "QA"); err == nil || !strings.Contains(err.Error(), "STATUS=STATE") {
		t.Fatalf("bad --state-map: %v", err)
	}
}
