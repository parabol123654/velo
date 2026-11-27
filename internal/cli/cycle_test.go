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

// #2: velo cycle update changes only the flags given.
func TestCycleUpdateCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := db.Open(filepath.Join(dir, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("admin@velo.dev", "Admin", "secret123")
	tok, _, _ := st.CreateAPIToken(admin.ID, "cli", db.TokenScope{})
	p, _ := st.CreateProject("DEV", "Dev", "")
	if _, err := st.CreateCycle(p.ID, "Sprint 1", "Ship search", "2026-12-01", "2026-12-14", "upcoming"); err != nil {
		t.Fatal(err)
	}
	distFS, _ := web.DistFS()
	srv, _ := server.New(st, dir, distFS)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"cycle", "update", "--url", ts.URL, "--token", tok}, args...))
		})
	}

	out, err := run("--project", "DEV", "sprint 1", "--status", "active")
	if err != nil || !strings.Contains(out, "Sprint 1 [active] 2026-12-01 → 2026-12-14") {
		t.Fatalf("status only: %q %v", out, err)
	}
	cycles, _ := st.ListCycles(p.ID)
	if cycles[0].Description != "Ship search" {
		t.Fatalf("description lost: %+v", cycles[0])
	}
	// An empty flag clears that field only.
	if out, err := run("--project", "DEV", "Sprint 1", "--end", ""); err != nil || !strings.Contains(out, "2026-12-01 → -") {
		t.Fatalf("clear end date: %q %v", out, err)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--project", "DEV", "Sprint 1"}, "nothing to change"},
		{[]string{"Sprint 1", "--status", "active"}, "usage"},
		{[]string{"--project", "DEV", "Nope", "--status", "active"}, `no cycle "Nope"`},
		{[]string{"--project", "DEV", "Sprint 1", "--status", "done"}, "invalid cycle status"},
	} {
		if _, err := run(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
}
