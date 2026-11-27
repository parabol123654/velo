package cli

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/web"
)

type qaCycleCLI struct {
	st  *db.Store
	url string
	tok string
	pid string
}

func newQACycleCLI(t *testing.T) *qaCycleCLI {
	t.Helper()
	dir := t.TempDir()
	st, err := db.Open(filepath.Join(dir, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("admin@velo.dev", "Admin", "secret123")
	tok, _, _ := st.CreateAPIToken(admin.ID, "cli", db.TokenScope{})
	p, _ := st.CreateProject("DEV", "Dev", "")
	st.CreateProject("OPS", "Ops", "")
	if _, err := st.CreateCycle(p.ID, "Sprint 1", "Ship search", "2026-12-01", "2026-12-14", "upcoming"); err != nil {
		t.Fatal(err)
	}
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, dir, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &qaCycleCLI{st: st, url: ts.URL, tok: tok, pid: p.ID}
}

func (e *qaCycleCLI) run(t *testing.T, tok string, args ...string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		return Run(append([]string{"cycle", "update", "--url", e.url, "--token", tok}, args...))
	})
}

func (e *qaCycleCLI) cycle(t *testing.T, name string) db.Cycle {
	t.Helper()
	cycles, _ := e.st.ListCycles(e.pid)
	for _, c := range cycles {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no cycle %q in %+v", name, cycles)
	return db.Cycle{}
}

func TestQACycleUpdateFlags(t *testing.T) {
	e := newQACycleCLI(t)

	// Flags after the name, flags given twice (last wins), --flag=value form.
	if _, err := e.run(t, e.tok, "Sprint 1", "--project", "DEV", "--status", "active", "--status=completed"); err != nil {
		t.Fatalf("flags after name: %v", err)
	}
	if c := e.cycle(t, "Sprint 1"); c.Status != "completed" || c.Description != "Ship search" || c.StartDate != "2026-12-01" || c.EndDate != "2026-12-14" {
		t.Fatalf("twice-given flag: %+v", c)
	}
	// --desc "" clears only the description; given twice, the last (empty) wins.
	if _, err := e.run(t, e.tok, "--project", "DEV", "--desc", "temp", "Sprint 1", "--desc", ""); err != nil {
		t.Fatal(err)
	}
	if c := e.cycle(t, "Sprint 1"); c.Description != "" || c.Status != "completed" || c.StartDate != "2026-12-01" {
		t.Fatalf("--desc \"\": %+v", c)
	}
	if _, err := e.run(t, e.tok, "--project", "DEV", "Sprint 1", "--desc="); err != nil {
		t.Fatalf("--desc=: %v", err)
	}
	// --start "" clears; --name renames, then the new name finds it.
	out, err := e.run(t, e.tok, "--project", "DEV", "--start", "", "--name", "Sprint One", "SPRINT 1")
	if err != nil || !strings.Contains(out, "Sprint One [completed] - → 2026-12-14") {
		t.Fatalf("rename+clear start: %q %v", out, err)
	}
	if _, err := e.run(t, e.tok, "--project", "DEV", "sprint one", "--status", "active"); err != nil {
		t.Fatalf("find by new name: %v", err)
	}
	if _, err := e.run(t, e.tok, "--project", "DEV", "Sprint 1", "--status", "active"); err == nil {
		t.Errorf("old name still found")
	}
	// A name that starts with "-" works after "--".
	e.st.CreateCycle(e.pid, "-x", "", "", "", "upcoming")
	if _, err := e.run(t, e.tok, "--project", "DEV", "--status", "completed", "--", "-x"); err != nil {
		t.Fatalf("dash name: %v", err)
	}
	if c := e.cycle(t, "-x"); c.Status != "completed" {
		t.Fatalf("dash name: %+v", c)
	}
}

func TestQACycleUpdateErrors(t *testing.T) {
	e := newQACycleCLI(t)
	e.st.CreateCycle(e.pid, "Dup", "", "", "", "active")
	e.st.CreateCycle(e.pid, "DUP", "", "", "", "active")
	sprint := e.cycle(t, "Sprint 1")
	ops, _ := e.st.GetProject("OPS")
	users, _ := e.st.ListUsers()
	ro, _, _ := e.st.CreateAPIToken(users[0].ID, "ro", db.TokenScope{Access: "read"})
	opsTok, _, _ := e.st.CreateAPIToken(users[0].ID, "ops", db.TokenScope{Projects: []string{ops.ID}})

	for _, tc := range []struct {
		tok  string
		args []string
		want string
	}{
		{e.tok, []string{"--project", "DEV", "dup", "--status", "completed"}, "use the cycle ID"},
		{e.tok, []string{"--project", "DEV", "Sprint 1", "Extra", "--status", "active"}, "unexpected argument"},
		{e.tok, []string{"--project", "DEV", "Sprint 1", "--name", ""}, "name"},
		{e.tok, []string{"--project", "DEV", "Sprint 1", "--name", "   "}, "name"},
		{e.tok, []string{"--project", "DEV", "Sprint 1", "--bogus", "x"}, "bogus"},
		{e.tok, []string{"--project", "DEV", "--status", "active"}, "usage"},
		{e.tok, []string{"--project", "OPS", "Sprint 1", "--status", "active"}, "no cycle"},
		{e.tok, []string{"--project", "OPS", sprint.ID, "--status", "active"}, "no cycle"},
		{e.tok, []string{"--project", "NOPE", "Sprint 1", "--status", "active"}, ""},
		{ro, []string{"--project", "DEV", "Sprint 1", "--status", "active"}, ""},
		{opsTok, []string{"--project", "DEV", "Sprint 1", "--status", "active"}, ""},
	} {
		_, err := e.run(t, tc.tok, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
	if c := e.cycle(t, "Sprint 1"); c.Status != "upcoming" || c.Description != "Ship search" {
		t.Errorf("a failed command changed the cycle: %+v", c)
	}
	for _, name := range []string{"Dup", "DUP"} {
		if c := e.cycle(t, name); c.Status != "active" {
			t.Errorf("ambiguous update changed %q: %+v", name, c)
		}
	}
	// By ID one duplicate can be changed.
	dup := e.cycle(t, "DUP")
	if _, err := e.run(t, e.tok, "--project", "DEV", dup.ID, "--status", "completed"); err != nil {
		t.Fatal(err)
	}
	if e.cycle(t, "DUP").Status != "completed" || e.cycle(t, "Dup").Status != "active" {
		t.Errorf("update by ID hit the wrong duplicate")
	}
}
