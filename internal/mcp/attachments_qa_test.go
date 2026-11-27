package mcp

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/web"
)

// delete_issue over MCP goes through the HTTP API, so it removes the issue's
// uploads and files like the web UI does.
func TestQAMCPDeleteIssueRemovesUploads(t *testing.T) {
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, _ := st.CreateFirstAdmin("agent@velo.dev", "Agent", "secret123")
	rawTok, _, _ := st.CreateAPIToken(admin.ID, "MCP", db.TokenScope{})
	st.CreateProject("VELO", "Velo", "")
	iss, err := st.CreateIssue(admin.ID, db.CreateIssueInput{ProjectRef: "VELO", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	distFS, _ := web.DistFS()
	srv, err := server.New(st, tmp, distFS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	file := filepath.Join(tmp, "uploads", "abc_shot.png")
	if err := os.WriteFile(file, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	att, err := st.CreateAttachment(admin.ID, iss.ID, "shot.png", "image/png", "abc_shot.png", 3)
	if err != nil {
		t.Fatal(err)
	}
	api := client.New(ts.URL, rawTok)
	if _, err := dispatchTool(api, "delete_issue", map[string]any{"ref": "VELO-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("MCP delete_issue left the upload file")
	}
	if _, err := st.GetAttachment(att.ID); err == nil {
		t.Fatal("MCP delete_issue left the attachment row")
	}
	if _, err := dispatchTool(api, "delete_issue", map[string]any{"ref": "VELO-1"}); err == nil {
		t.Fatal("deleting a missing issue should fail")
	}
}
