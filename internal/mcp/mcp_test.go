package mcp

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velo-dev/velo/internal/client"
	"github.com/velo-dev/velo/internal/db"
	"github.com/velo-dev/velo/internal/server"
	"github.com/velo-dev/velo/web"
)

func TestMCPStdioAgentWorkflow(t *testing.T) {
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	admin, err := st.CreateFirstAdmin("agent@velo.dev", "AI Pair", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	rawTok, _, err := st.CreateAPIToken(admin.ID, "MCP Test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject("VELO", "Velo Tracker", "Fast local tracker"); err != nil {
		t.Fatal(err)
	}

	distFS, _ := web.DistFS()
	srv, err := server.New(st, tmp, distFS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	api := client.New(ts.URL, rawTok)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_issue","arguments":{"project":"VELO","title":"Support MCP stdio server","description":"Allow AI agents to manage tickets locally","priority":4,"labels":["feature"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"update_issue","arguments":{"ref":"VELO-1","state":"In Progress"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add_comment","arguments":{"ref":"VELO-1","body":"Implemented and verified via go test."}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"MCP stdio"}}}`,
	}

	var out bytes.Buffer
	if err := ServeStdio(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, api); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// 6 responses expected because notifications/initialized has no id and must NOT produce a response!
	if len(lines) != 6 {
		t.Fatalf("expected 6 JSON-RPC responses (notification ignored), got %d:\n%s", len(lines), out.String())
	}

	if !strings.Contains(lines[2], "VELO-1") {
		t.Fatalf("create_issue response missing VELO-1: %s", lines[2])
	}
	if !strings.Contains(lines[3], "In Progress") {
		t.Fatalf("update_issue response missing In Progress: %s", lines[3])
	}
	if !strings.Contains(lines[5], "VELO-1") {
		t.Fatalf("search_issues response missing VELO-1: %s", lines[5])
	}

	var checkResp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	for idx, l := range lines {
		if err := json.Unmarshal([]byte(l), &checkResp); err != nil {
			t.Fatalf("line %d invalid JSON: %v", idx, err)
		}
		if checkResp.Result.IsError {
			t.Fatalf("line %d returned tool error: %s", idx, l)
		}
	}
}
