package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// QA: list_issues and search_issues with the query syntax through ServeStdio:
// valid queries give exact results, invalid ones give isError with the
// server's message, and a combination of a separate argument and the same
// field in query is an error.
func TestQAMCPQuerySyntaxStdio(t *testing.T) {
	api, st := newMCPTestAPI(t)
	me := cyclesOwner(t, st)
	for _, in := range []db.CreateIssueInput{
		{ProjectRef: "VELO", Title: "đăng nhập lỗi", AssigneeID: me, LabelIDs: []string{"bug"}, Priority: 4}, // VELO-1
		{ProjectRef: "VELO", Title: "child", ParentRef: "VELO-1", StateID: "Done", Priority: 1},              // VELO-2
		{ProjectRef: "VELO", Title: "loose end", Priority: 2},                                                // VELO-3
	} {
		if _, err := st.CreateIssue(me, in); err != nil {
			t.Fatal(err)
		}
	}
	type call struct {
		tool  string
		args  map[string]any
		ok    bool
		want  []string // identifiers (or error text) that must appear
		avoid []string
	}
	calls := []call{
		{"list_issues", map[string]any{"query": "assignee:me is:open"}, true, []string{"VELO-1"}, []string{"VELO-2", "VELO-3"}},
		{"list_issues", map[string]any{"query": "has:parent"}, true, []string{"VELO-2"}, []string{"VELO-1", "VELO-3"}},
		{"list_issues", map[string]any{"query": "no:parent priority:<=medium"}, true, []string{"VELO-3"}, []string{"VELO-1", "VELO-2"}},
		{"list_issues", map[string]any{"project": "VELO", "query": "dang nhap label:BUG"}, true, []string{"VELO-1"}, []string{"VELO-2", "VELO-3"}},
		{"list_issues", map[string]any{"query": "IS:CLOSED"}, true, []string{"VELO-2"}, []string{"VELO-1", "VELO-3"}},
		{"search_issues", map[string]any{"query": "VELO-1 is:open"}, true, []string{"VELO-1"}, []string{"VELO-2", "VELO-3"}},
		{"search_issues", map[string]any{"query": "assignee:me\tpriority:>=4"}, true, []string{"VELO-1"}, []string{"VELO-3"}},
		{"search_issues", map[string]any{"query": "assignee:none is:open"}, true, []string{"VELO-3"}, []string{"VELO-1", "VELO-2"}},
		{"list_issues", map[string]any{"query": "asignee:me"}, false, []string{`unknown filter`, "asignee:"}, nil},
		{"list_issues", map[string]any{"query": "priority:>=5"}, false, []string{"priority"}, nil},
		{"list_issues", map[string]any{"query": "priority:>>3"}, false, []string{"priority"}, nil},
		{"list_issues", map[string]any{"query": `state:"In Progress`}, false, []string{"unclosed quote"}, nil},
		{"list_issues", map[string]any{"query": "is:open is:closed"}, false, []string{"cannot both hold"}, nil},
		{"list_issues", map[string]any{"parent": "root", "query": "has:parent"}, false, []string{"both as a parameter"}, nil},
		{"list_issues", map[string]any{"priority": 4, "query": "priority:4"}, false, []string{"both as a parameter"}, nil},
		{"list_issues", map[string]any{"unblocked": true, "query": "is:blocked"}, false, []string{"cannot both hold"}, nil},
		{"list_issues", map[string]any{"assignee": "me", "query": "assignee:none"}, false, []string{"both as a parameter"}, nil},
		{"search_issues", map[string]any{"query": "is:done"}, false, []string{"is: takes"}, nil},
		{"search_issues", map[string]any{"query": `label:""`}, false, []string{"needs a value"}, nil},
		{"search_issues", map[string]any{"query": "fix: login"}, false, []string{"unknown filter"}, nil},
		{"search_issues", map[string]any{"query": "has:label"}, false, []string{"takes parent"}, nil},
	}
	var in strings.Builder
	for i, c := range calls {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call",
			"params": map[string]any{"name": c.tool, "arguments": c.args}})
		in.Write(b)
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	if err := ServeStdio(strings.NewReader(in.String()), &out, api); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(calls) {
		t.Fatalf("want %d responses, got %d:\n%s", len(calls), len(lines), out.String())
	}
	for i, l := range lines {
		c := calls[i]
		var resp struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(l), &resp); err != nil {
			t.Fatalf("%d: %v %s", i, err, l)
		}
		text := ""
		for _, ct := range resp.Result.Content {
			text += ct.Text
		}
		if resp.Result.IsError == c.ok {
			t.Errorf("%s %v: isError=%v, text %s", c.tool, c.args, resp.Result.IsError, text)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(text, `"identifier": "`+w+`"`) && !(!c.ok && strings.Contains(text, w)) {
				t.Errorf("%s %v: missing %q in %s", c.tool, c.args, w, text)
			}
		}
		for _, a := range c.avoid {
			if strings.Contains(text, `"identifier": "`+a+`"`) {
				t.Errorf("%s %v: should not contain %q", c.tool, c.args, a)
			}
		}
	}
}

// QA: a very large query through MCP is answered (not a transport failure)
// and is not reported as an internal server error.
func TestQAMCPHugeQuery(t *testing.T) {
	api, _ := newMCPTestAPI(t, "one")
	for name, q := range map[string]string{
		"3000 words":       strings.Repeat("word ", 3000),
		"1200 label:bug":   strings.Repeat("label:bug ", 1200),
		"emoji and tabs":   strings.Repeat("🐛\t", 2000),
		"NUL inside query": "one\x00 is:open",
	} {
		for _, tool := range []string{"list_issues", "search_issues"} {
			_, err := dispatchTool(api, tool, map[string]any{"query": q})
			if err != nil && (strings.Contains(err.Error(), "500") || strings.Contains(strings.ToLower(err.Error()), "sql")) {
				t.Errorf("%s %s: internal error leaked: %.200v", tool, name, err)
			}
		}
	}
}
