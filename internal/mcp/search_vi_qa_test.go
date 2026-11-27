package mcp

import (
	"bytes"
	"strings"
	"testing"
)

// QA: search_issues through ServeStdio folds Vietnamese accents.
func TestQAMCPSearchVietnamese(t *testing.T) {
	api, _ := newMCPTestAPI(t, "Đăng nhập bằng Google", "Cập nhật hồ sơ người dùng")
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"dang nhap"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"NGUOI DUNG"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"\"ho so\" (cap)"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_issues","arguments":{"query":"velo-2"}}}`,
	}
	var out bytes.Buffer
	if err := ServeStdio(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, api); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 responses, got %s", out.String())
	}
	want := []string{"VELO-1", "VELO-2", "VELO-2", "VELO-2"}
	other := []string{"VELO-2", "VELO-1", "VELO-1", "VELO-1"}
	for i, l := range lines {
		if strings.Contains(l, `"isError":true`) || !strings.Contains(l, want[i]) || strings.Contains(l, other[i]) {
			t.Errorf("request %d: %s", i+1, l)
		}
	}

	// Direct dispatch with a query of only FTS syntax must not error.
	if _, err := dispatchTool(api, "search_issues", map[string]any{"query": "* - ( ) : ^"}); err != nil {
		t.Errorf("syntax-only query: %v", err)
	}
}
