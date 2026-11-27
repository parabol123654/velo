package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

func qaWriteCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func qaRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error { return Run(args) })
}

func qaCount(t *testing.T, st *db.Store) int {
	t.Helper()
	issues, err := st.ListIssues(db.IssueFilter{ProjectRef: "DEV"})
	if err != nil {
		t.Fatal(err)
	}
	return len(issues)
}

// A key that appears twice in a CSV is a repeated external ID, which SPEC
// says is refused with nothing written. The CLI must not silently keep one
// of the two rows (and drop the other's comments).
func TestQAImportDuplicateKeysAreRefused(t *testing.T) {
	st, url, tok := importServer(t)
	jira := qaWriteCSV(t, "Summary,Issue key,Comment\nFirst,WEB-1,\nSecond,WEB-1,02/Mar/24 10:00 AM;u;only on the second row\n")
	linear := qaWriteCSV(t, "ID,Title\nENG-1,First\nENG-1,Second\n")
	for _, args := range [][]string{
		{"import", "jira", jira},
		{"import", "linear", linear},
	} {
		out, err := qaRun(t, append(args, "--project", "DEV", "--url", url, "--token", tok)...)
		if err == nil || !strings.Contains(err.Error(), "appears twice") {
			t.Errorf("%s: want an error naming the repeated key, got %v (output %q)", args[1], err, out)
		}
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("%d issues written from files with repeated keys", n)
	}
}

// --state-map names a Velo state. When the project has no such state the
// import must say so, not silently put the issues in another state (which a
// rerun cannot fix, as it skips them).
func TestQAImportJiraStateMapToUnknownState(t *testing.T) {
	st, url, tok := importServer(t)
	path := qaWriteCSV(t, "Summary,Issue key,Status,Status Category\nA,WEB-1,QA,In Progress\n")
	out, err := qaRun(t, "import", "jira", path, "--project", "DEV", "--state-map", "QA=Nosuch State", "--url", url, "--token", tok)
	if err == nil || !strings.Contains(err.Error(), "Nosuch State") {
		iss, _ := st.GetIssue("DEV-1")
		state := ""
		if iss != nil {
			state = iss.StateName
		}
		t.Fatalf("want an error naming the unknown state, got %v (output %q, DEV-1 in %q)", err, out, state)
	}
}

// Parent loops and blocking loops in a file are refused by the server with
// nothing written; the CLI neither hangs nor half-imports.
func TestQAImportLoopsAreRefused(t *testing.T) {
	st, url, tok := importServer(t)
	for _, tc := range []struct{ kind, csv, want string }{
		{"jira", "Summary,Issue key,Parent\nA,WEB-1,WEB-3\nB,WEB-2,WEB-1\nC,WEB-3,WEB-2\n", "loop"},
		{"jira", "Summary,Issue key,Parent\nA,WEB-1,WEB-1\n", "loop"},
		{"jira", "Summary,Issue key,Outward issue link (Blocks)\nA,WEB-1,WEB-2\nB,WEB-2,WEB-3\nC,WEB-3,WEB-1\n", "blocking loop"},
		{"linear", "ID,Title,Parent issue\nENG-1,a,ENG-2\nENG-2,b,ENG-1\n", "loop"},
	} {
		path := qaWriteCSV(t, tc.csv)
		for _, dry := range []bool{true, false} {
			args := []string{"import", tc.kind, path, "--project", "DEV", "--url", url, "--token", tok}
			if dry {
				args = append(args, "--dry-run")
			}
			if _, err := qaRun(t, args...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s %q dry=%v: want %q, got %v", tc.kind, tc.csv, dry, tc.want, err)
			}
		}
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("%d issues written", n)
	}
}

// Two Jira issues that block each other are a blocking loop, which SPEC
// says is a 400 with nothing written. The import must not silently keep one
// direction and report success.
func TestQAImportJiraMutualBlocksAreALoop(t *testing.T) {
	st, url, tok := importServer(t)
	path := qaWriteCSV(t, "Summary,Issue key,Outward issue link (Blocks)\nA,WEB-1,WEB-2\nB,WEB-2,WEB-1\n")
	out, err := qaRun(t, "import", "jira", path, "--project", "DEV", "--url", url, "--token", tok)
	if err == nil || !strings.Contains(err.Error(), "blocking loop") {
		one, _ := st.GetIssue("DEV-1")
		two, _ := st.GetIssue("DEV-2")
		t.Fatalf("want a blocking-loop error, got %v (output %q; DEV-1 blocked by %d, DEV-2 blocked by %d)", err, out, one.BlockedByCount, two.BlockedByCount)
	}
}

// Dry runs write nothing; a real run imports; a rerun adds nothing; a rerun
// of a grown export adds only the new issue, the new comment on an old
// issue, and the new link.
func TestQAImportJiraDryRunRerunAndGrowth(t *testing.T) {
	st, url, tok := importServer(t)
	v1 := "Summary,Issue key,Labels,Comment,Comment,Outward issue link (Blocks)\n" +
		"A,WEB-1,Lỗi,02/Mar/24 10:00 AM;u;first,,\n" +
		"B,WEB-2,LỖI,,,\n"
	v2 := "Summary,Issue key,Labels,Comment,Comment,Outward issue link (Blocks)\n" +
		"A,WEB-1,Lỗi,02/Mar/24 10:00 AM;u;first,03/Mar/24 10:00 AM;u;second,WEB-3\n" +
		"B,WEB-2,LỖI,,,\n" +
		"C,WEB-3,,,,\n"
	path := qaWriteCSV(t, v1)
	run := func(args ...string) string {
		t.Helper()
		out, err := qaRun(t, append([]string{"import", "jira", path, "--project", "dev", "--url", url, "--token", tok}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	if out := run("--dry-run"); !strings.Contains(out, "Would import 2 issues (0 already imported), 1 comments, 0 links") || !strings.Contains(out, "New labels: Lỗi\n") {
		t.Fatalf("dry run: %q", out)
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("dry run wrote %d issues", n)
	}
	if out := run(); !strings.Contains(out, "Imported 2 issues (0 already imported), 1 comments, 0 links") {
		t.Fatalf("import: %q", out)
	}
	if out := run(); !strings.Contains(out, "Imported 0 issues (2 already imported), 0 comments, 0 links") || strings.Contains(out, "New labels") {
		t.Fatalf("rerun: %q", out)
	}
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run("--dry-run"); !strings.Contains(out, "Would import 1 issues (2 already imported), 1 comments, 1 links") {
		t.Fatalf("dry run of the grown export: %q", out)
	}
	if out := run(); !strings.Contains(out, "Imported 1 issues (2 already imported), 1 comments, 1 links") {
		t.Fatalf("grown export: %q", out)
	}
	if out := run(); !strings.Contains(out, "Imported 0 issues (3 already imported), 0 comments, 0 links") {
		t.Fatalf("rerun of the grown export: %q", out)
	}
	if n := qaCount(t, st); n != 3 {
		t.Fatalf("%d issues", n)
	}
	one, _ := st.GetIssue("DEV-1")
	comments, err := st.ListComments(one.ID)
	if err != nil || len(comments) != 2 {
		t.Fatalf("DEV-1 comments: %d %v", len(comments), err)
	}
}

// The same for Linear, with a cycle: the cycle is created once.
func TestQAImportLinearDryRunAndRerun(t *testing.T) {
	st, url, tok := importServer(t)
	path := qaWriteCSV(t, "\xef\xbb\xbfID,Title,Cycle Name,Cycle Start,Cycle End,Parent issue\r\n"+
		"ENG-2,Child,Đợt 1,2024-03-01,2024-03-14,ENG-1\r\n"+
		"ENG-1,Parent,đợt 1,,,\r\n")
	run := func(args ...string) string {
		t.Helper()
		out, err := qaRun(t, append([]string{"import", "linear", "--project", "DEV", path, "--url", url, "--token", tok}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	if out := run("--dry-run"); !strings.Contains(out, "Would import 2 issues") || strings.Count(out, "Đợt 1")+strings.Count(out, "đợt 1") != 1 {
		t.Fatalf("dry run: %q", out)
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("dry run wrote %d issues", n)
	}
	run()
	if out := run(); !strings.Contains(out, "Imported 0 issues (2 already imported)") || strings.Contains(out, "New cycles") {
		t.Fatalf("rerun: %q", out)
	}
	// The CLI sends parents first, so the parent listed second gets DEV-1.
	child, _ := st.GetIssue("DEV-2")
	if child.Title != "Child" || child.ParentIdentifier != "DEV-1" || child.CycleName == "" {
		t.Fatalf("a child listed before its parent: %+v", child)
	}
}

// A Jira export of more issues than one request holds, children listed
// before their parents across the split, imports in parts; the dry run works
// too.
func TestQAImportJiraBigFile(t *testing.T) {
	st, url, tok := importServer(t)
	n := importPartIssues + 150
	var sb strings.Builder
	sb.WriteString("Summary,Issue key,Parent,Outward issue link (Blocks)\n")
	for i := 1; i <= n; i++ {
		parent, blocks := "", ""
		if i <= 10 {
			parent = fmt.Sprintf("BIG-%d", n-i+1) // parent is near the end of the file
		}
		if i == 2 {
			blocks = fmt.Sprintf("BIG-%d", n)
		}
		fmt.Fprintf(&sb, "Issue %d,BIG-%d,%s,%s\n", i, i, parent, blocks)
	}
	path := qaWriteCSV(t, sb.String())
	base := []string{"import", "jira", path, "--project", "DEV", "--url", url, "--token", tok}
	if out, err := qaRun(t, append(base, "--dry-run")...); err != nil || !strings.Contains(out, fmt.Sprintf("Would import %d issues", n)) {
		t.Fatalf("dry run: %q %v", out, err)
	}
	if out, err := qaRun(t, base...); err != nil || !strings.Contains(out, fmt.Sprintf("Imported %d issues", n)) || !strings.Contains(out, "1 links") {
		t.Fatalf("import: %q %v", out, err)
	}
	if out, err := qaRun(t, base...); err != nil || !strings.Contains(out, fmt.Sprintf("Imported 0 issues (%d already imported), 0 comments, 0 links", n)) {
		t.Fatalf("rerun: %q %v", out, err)
	}
	withParent := 0
	all, _ := st.ListIssues(db.IssueFilter{ProjectRef: "DEV"})
	for _, iss := range all {
		if iss.ParentIdentifier != "" {
			withParent++
		}
	}
	if len(all) != n || withParent != 10 {
		t.Fatalf("%d issues, %d with a parent", len(all), withParent)
	}
}

// Bad flags and arguments fail before anything is sent, with a message.
func TestQAImportBadArguments(t *testing.T) {
	st, url, tok := importServer(t)
	good := qaWriteCSV(t, "Summary,Issue key,ID,Title\nA,WEB-1,ENG-1,A\n")
	empty := qaWriteCSV(t, "")
	missing := filepath.Join(t.TempDir(), "missing.csv")
	conn := []string{"--url", url, "--token", tok}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import"}, "usage: velo import"},
		{[]string{"import", "jira"}, "usage"},
		{[]string{"import", "jira", good, good, "--project", "DEV"}, "usage"},
		{[]string{"import", "jira", good, "--project", "DEV", "--bogus"}, "flag provided but not defined"},
		{[]string{"import", "jira", good, "--project", "  "}, "--project is required"},
		{[]string{"import", "jira", missing}, "--project is required"},
		{[]string{"import", "jira", missing, "--project", "DEV"}, "no such file"},
		{[]string{"import", "jira", empty, "--project", "DEV"}, "empty"},
		{[]string{"import", "jira", good, "--project", "DEV", "--state-map", "=Done"}, "STATUS=STATE"},
		{[]string{"import", "jira", good, "--project", "DEV", "--state-map", "QA="}, "STATUS=STATE"},
		{[]string{"import", "jira", good, "--project", "DEV", "--map-user", "x=not-an-email"}, "LOGIN=EMAIL"},
		{[]string{"import", "jira", good, "--project", "NOPE"}, "404"},
		{[]string{"import", "linear", good, "--project", "DEV", "--state-map", "a=b"}, "flag provided but not defined"},
		{[]string{"import", "linear", good, "--project", "DEV", "--map-user", "@=a@b.c"}, "LOGIN=EMAIL"},
		{[]string{"import", "linear", missing, "--project", "DEV"}, "no such file"},
		{[]string{"import", "github", "not-a-repo", "--project", "DEV", "--github-api", "http://127.0.0.1:1"}, "OWNER/NAME"},
		{[]string{"import", "github", "o/r", "--github-api", "http://127.0.0.1:1"}, "--project is required"},
		{[]string{"import", "github", "--project", "DEV"}, "usage"},
		{[]string{"import", "GitHub", "o/r"}, "unknown import source"},
	} {
		if _, err := qaRun(t, append(tc.args, conn...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("%d issues written by bad commands", n)
	}
}

// A read-only token, or one for another project, cannot import.
func TestQAImportTokenScopes(t *testing.T) {
	st, url, _ := importServer(t)
	users, err := st.ListUsers()
	if err != nil || len(users) != 1 {
		t.Fatal(users, err)
	}
	admin := users[0]
	if _, err := st.CreateProject("OPS", "Ops", ""); err != nil {
		t.Fatal(err)
	}
	ro, _, _ := st.CreateAPIToken(admin.ID, "ro", db.TokenScope{Access: "read"})
	ops, _, _ := st.CreateAPIToken(admin.ID, "ops", db.TokenScope{Projects: []string{"OPS"}})
	path := qaWriteCSV(t, "ID,Title\nENG-1,a\n")
	for name, tok := range map[string]string{"read-only": ro, "other project": ops, "bad": "velo_nope"} {
		for _, dry := range []string{"--dry-run=false", "--dry-run"} {
			if _, err := qaRun(t, "import", "linear", path, "--project", "DEV", dry, "--url", url, "--token", tok); err == nil {
				t.Errorf("%s token %s: import allowed", name, dry)
			}
		}
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("%d issues written", n)
	}
}

// GitHub end to end: an error from GitHub writes nothing; an empty
// repository says so; --github-token wins over GITHUB_TOKEN, and GH_TOKEN is
// used when GITHUB_TOKEN is empty; a dry run asks GitHub but writes nothing.
func TestQAImportGitHubEndToEnd(t *testing.T) {
	st, url, tok := importServer(t)
	var mu sync.Mutex
	var auth []string
	mode := "ok"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		m := mode
		mu.Unlock()
		switch {
		case m == "401":
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
		case m == "fail-comments" && r.URL.Path == "/repos/o/r/issues/comments":
			w.WriteHeader(500)
			w.Write([]byte(`{"message":"boom"}`))
		case m == "empty" || r.URL.Path != "/repos/o/r/issues":
			w.Write([]byte(`[]`))
		default:
			w.Write([]byte(`[{"number":1,"title":"Một","state":"open","user":{"login":"a"}},{"number":2,"title":"PR","state":"open","user":{"login":"a"},"pull_request":{}}]`))
		}
	}))
	defer gh.Close()
	set := func(m string) {
		mu.Lock()
		mode, auth = m, nil
		mu.Unlock()
	}
	run := func(args ...string) (string, error) {
		return qaRun(t, append([]string{"import", "github", "o/r", "--project", "DEV", "--github-api", gh.URL + "/", "--url", url, "--token", tok}, args...)...)
	}
	lastAuth := func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth[len(auth)-1]
	}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-cli-token")
	for _, m := range []string{"401", "fail-comments"} {
		set(m)
		if _, err := run(); err == nil {
			t.Fatalf("%s: no error", m)
		}
		if n := qaCount(t, st); n != 0 {
			t.Fatalf("%s: %d issues written", m, n)
		}
	}
	// GH_TOKEN and GITHUB_TOKEN are for api.github.com; this fake API host
	// gets no token from them (review finding: never send a github.com token
	// to another host).
	if a := lastAuth(); a != "" {
		t.Fatalf("environment token sent to a host other than api.github.com: %q", a)
	}

	set("empty")
	if out, err := run(); err != nil || !strings.Contains(out, "Nothing to import") {
		t.Fatalf("empty repo: %q %v", out, err)
	}

	set("ok")
	t.Setenv("GITHUB_TOKEN", "env-token")
	if out, err := run("--github-token", "flag-token", "--dry-run"); err != nil || !strings.Contains(out, "Would import 1 issues") {
		t.Fatalf("dry run: %q %v", out, err)
	}
	if a := lastAuth(); a != "Bearer flag-token" {
		t.Fatalf("--github-token did not win: %q", a)
	}
	if n := qaCount(t, st); n != 0 {
		t.Fatalf("dry run wrote %d issues", n)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Imported 1 issues") {
		t.Fatalf("import: %q %v", out, err)
	}
	if a := lastAuth(); a != "" { // not api.github.com, so not GITHUB_TOKEN
		t.Fatalf("GITHUB_TOKEN sent to a host other than api.github.com: %q", a)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Imported 0 issues (1 already imported)") {
		t.Fatalf("rerun: %q %v", out, err)
	}
}
