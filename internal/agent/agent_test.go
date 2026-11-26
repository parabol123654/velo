package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/web"
)

// TestHelperProcess is the fake coding agent: the tests run this test binary
// as the command, choosing its behaviour with VELO_AGENT_HELPER.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("VELO_AGENT_HELPER")
	if mode == "" {
		return
	}
	stdin, _ := io.ReadAll(os.Stdin)
	first := strings.SplitN(string(stdin), "\n", 2)[0]
	fmt.Printf("ISSUE=%s ARGS=%q PROMPT=%s\n", os.Getenv("VELO_ISSUE"), os.Args[len(os.Args)-1], first)
	switch mode {
	case "fail":
		os.Exit(3)
	case "sleep":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func helperCmd(t *testing.T, mode string) []string {
	t.Setenv("VELO_AGENT_HELPER", mode)
	return []string{os.Args[0], "-test.run=TestHelperProcess", "--", "{issue}"}
}

type env struct {
	api   *client.Client
	st    *db.Store
	admin *db.User
	url   string
}

func newEnv(t *testing.T) env {
	t.Helper()
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("dev@velo.dev", "Dev", "secret123")
	tok, _, _ := st.CreateAPIToken(admin.ID, "Claude Code", db.TokenScope{Projects: []string{}})
	p, _ := st.CreateProject("DEV", "Dev", "")
	st.CreateLabel(p.ID, "agent", "#7c3aed")
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, tmp, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return env{api: client.New(ts.URL, tok), st: st, admin: admin, url: ts.URL}
}

func (e env) issue(t *testing.T, title string, prio int, labels ...string) {
	t.Helper()
	if _, err := e.api.CreateIssue(db.CreateIssueInput{ProjectRef: "DEV", Title: title, Priority: prio, LabelIDs: labels}); err != nil {
		t.Fatal(err)
	}
}

func (e env) lastComment(t *testing.T, ref string) db.Comment {
	t.Helper()
	iss, _ := e.st.GetIssue(ref)
	cms, _ := e.st.ListComments(iss.ID)
	if len(cms) == 0 {
		t.Fatalf("%s has no comment", ref)
	}
	return cms[len(cms)-1]
}

func (e env) cfg(cmd []string) Config {
	return Config{Label: "agent", Cmd: cmd, Timeout: 30 * time.Second, Poll: time.Hour, Once: true}
}

func TestRunOnceSuccess(t *testing.T) {
	e := newEnv(t)
	e.issue(t, "Urgent but not for the agent", 4)
	e.issue(t, "Agent task", 1, "agent")

	var log bytes.Buffer
	if err := Run(context.Background(), e.api, e.cfg(helperCmd(t, "ok")), &log); err != nil {
		t.Fatal(err)
	}
	iss, _ := e.st.GetIssue("DEV-2")
	if iss.AssigneeID != e.admin.ID || iss.StateName != "In Progress" {
		t.Fatalf("DEV-2 not claimed: %s / %s", iss.AssigneeName, iss.StateName)
	}
	cm := e.lastComment(t, "DEV-2")
	for _, want := range []string{"exit code 0", `ISSUE=DEV-2 ARGS="DEV-2" PROMPT=Work on DEV-2`} {
		if !strings.Contains(cm.Body, want) {
			t.Fatalf("comment lacks %q:\n%s", want, cm.Body)
		}
	}
	if cm.Via != "Claude Code" {
		t.Fatalf("comment via = %q", cm.Via)
	}
	if one, _ := e.st.GetIssue("DEV-1"); one.AssigneeID != "" {
		t.Fatal("unlabeled issue was taken")
	}
}

func TestRunFailureTimeoutAndNoRetry(t *testing.T) {
	e := newEnv(t)
	e.issue(t, "Will fail", 0, "agent")
	var log bytes.Buffer
	Run(context.Background(), e.api, e.cfg(helperCmd(t, "fail")), &log)
	if cm := e.lastComment(t, "DEV-1"); !strings.Contains(cm.Body, "exit code 3") || !strings.Contains(cm.Body, "stays assigned") {
		t.Fatalf("failure comment:\n%s", cm.Body)
	}
	// The failed issue stays assigned, so the next run finds nothing.
	log.Reset()
	Run(context.Background(), e.api, e.cfg(helperCmd(t, "ok")), &log)
	if !strings.Contains(log.String(), "no issue is waiting") {
		t.Fatalf("failed issue was picked again:\n%s", log.String())
	}

	e.issue(t, "Will hang", 0, "agent")
	cfg := e.cfg(helperCmd(t, "sleep"))
	cfg.Timeout = 500 * time.Millisecond
	start := time.Now()
	Run(context.Background(), e.api, cfg, &log)
	if time.Since(start) > 8*time.Second {
		t.Fatal("timeout did not stop the command")
	}
	if cm := e.lastComment(t, "DEV-2"); !strings.Contains(cm.Body, "timed out") {
		t.Fatalf("timeout comment:\n%s", cm.Body)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.issue(t, "Agent task", 0, "agent")
	cfg := e.cfg([]string{"claude", "-p", "{prompt}"})
	cfg.DryRun = true
	var log bytes.Buffer
	if err := Run(context.Background(), e.api, cfg, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "would claim DEV-1") {
		t.Fatalf("dry run output:\n%s", log.String())
	}
	if iss, _ := e.st.GetIssue("DEV-1"); iss.AssigneeID != "" || iss.StateName != "Todo" {
		t.Fatal("dry run changed the issue")
	}
}

func TestWakesOnEvent(t *testing.T) {
	e := newEnv(t)
	cfg := e.cfg(helperCmd(t, "ok"))
	cfg.Once = false // keep running; poll interval is an hour, so only events can wake it
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, e.api, cfg, io.Discard)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	time.Sleep(300 * time.Millisecond) // idle, subscribed
	e.issue(t, "Arrives later", 0, "agent")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		iss, _ := e.st.GetIssue("DEV-1")
		if cms, _ := e.st.ListComments(iss.ID); len(cms) > 0 && strings.Contains(cms[0].Body, "exit code 0") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("agent did not pick up the new issue from the event stream")
}

func TestSplitCommand(t *testing.T) {
	cases := map[string][]string{
		`claude -p {prompt}`:             {"claude", "-p", "{prompt}"},
		`claude -p "{prompt}" --verbose`: {"claude", "-p", "{prompt}", "--verbose"},
		`run 'a b' "c \"d\"" e\ f`:       {"run", "a b", `c "d"`, "e f"},
		`  spaced   out  `:               {"spaced", "out"},
		`empty "" arg`:                   {"empty", "", "arg"},
		`no $(shell) | pipes; here`:      {"no", "$(shell)", "|", "pipes;", "here"},
	}
	for in, want := range cases {
		got, err := SplitCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitCommand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := SplitCommand(`claude "unterminated`); err == nil {
		t.Error("unterminated quote accepted")
	}
}

func TestBuildArgv(t *testing.T) {
	prompt := `do it; rm -rf / "quoted" $(x)`
	argv, stdin := buildArgv([]string{"claude", "-p", "{prompt}", "--name={issue}"}, prompt, "DEV-7")
	if stdin || !reflect.DeepEqual(argv, []string{"claude", "-p", prompt, "--name=DEV-7"}) {
		t.Fatalf("argv = %q stdin=%v", argv, stdin)
	}
	if _, stdin := buildArgv([]string{"agent-script"}, prompt, "DEV-7"); !stdin {
		t.Fatal("prompt should go to stdin when no argument takes it")
	}
}
