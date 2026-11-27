package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/mcp"
	"github.com/parabol123654/velo/internal/version"
)

// Known gap 11: `velo version` and MCP initialize report the one version set
// at build time.
// It changes the package-level version.Version, so it must not run in parallel.
func TestVersionAndMCPAgree(t *testing.T) {
	saved := version.Version
	version.Version = "v9.8.7-test"
	t.Cleanup(func() { version.Version = saved })

	out, err := captureStdout(t, func() error { return Run([]string{"version"}) })
	if err != nil || strings.TrimSpace(out) != "velo v9.8.7-test" {
		t.Fatalf("velo version printed %q (%v)", out, err)
	}

	var buf bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}` + "\n")
	if err := mcp.ServeStdio(in, &buf, client.New("http://127.0.0.1:0", "")); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("initialize: %v\n%s", err, buf.String())
	}
	if resp.Result.ServerInfo.Version != "v9.8.7-test" {
		t.Fatalf("MCP serverInfo.version = %q, want the build version", resp.Result.ServerInfo.Version)
	}
}
