package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

func ServeStdio(in io.Reader, out io.Writer, api *client.Client) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		// JSON-RPC notifications omit ID and must not receive a response.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		resp := handleRequest(req, api)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleRequest(req rpcRequest, api *client.Client) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "velo-mcp",
					"version": "0.2.0",
				},
			},
		}
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefinitions()}}
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &call); err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid tool call params"},
			}
		}
		text, err := dispatchTool(api, call.Name, call.Arguments)
		if err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: toolResult{
					Content: []toolContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				},
			}
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: toolResult{
				Content: []toolContent{{Type: "text", Text: text}},
			},
		}
	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intPtrArg(args map[string]any, key string) *int {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		i := int(n)
		return &i
	case int:
		return &n
	}
	return nil
}

func strSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func toPrettyJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func dispatchTool(api *client.Client, name string, args map[string]any) (string, error) {
	switch name {
	case "list_projects":
		projects, err := api.ListProjects()
		if err != nil {
			return "", err
		}
		return toPrettyJSON(projects), nil

	case "list_issues":
		limit := 30
		if l := intPtrArg(args, "limit"); l != nil && *l > 0 {
			limit = *l
		}
		issues, err := api.ListIssues(client.IssueQuery{
			Project:  strArg(args, "project"),
			State:    strArg(args, "state"),
			Category: strArg(args, "category"),
			Query:    strArg(args, "query"),
			Assignee: strArg(args, "assignee"),
			Cycle:    strArg(args, "cycle"),
			Label:    strArg(args, "label"),
			Parent:   strArg(args, "parent"),
			Priority: intPtrArg(args, "priority"),
			Limit:    limit,
		})
		if err != nil {
			return "", err
		}
		return toPrettyJSON(issues), nil

	case "get_issue":
		ref := strArg(args, "ref")
		if ref == "" {
			return "", fmt.Errorf("ref is required (e.g. DEV-1)")
		}
		detail, err := api.GetIssueDetail(ref)
		if err != nil {
			return "", err
		}
		return toPrettyJSON(detail), nil

	case "create_issue":
		prio := 0
		if p := intPtrArg(args, "priority"); p != nil {
			prio = *p
		}
		iss, err := api.CreateIssue(db.CreateIssueInput{
			ProjectRef:  strArg(args, "project"),
			ParentRef:   strArg(args, "parent"),
			Title:       strArg(args, "title"),
			Description: strArg(args, "description"),
			StateID:     strArg(args, "state"),
			Priority:    prio,
			AssigneeID:  strArg(args, "assignee"),
			CycleID:     strArg(args, "cycle"),
			LabelIDs:    strSliceArg(args, "labels"),
		})
		if err != nil {
			return "", err
		}
		return toPrettyJSON(iss), nil

	case "update_issue":
		ref := strArg(args, "ref")
		if ref == "" {
			return "", fmt.Errorf("ref is required (e.g. DEV-1)")
		}
		var patch db.UpdateIssueInput
		if _, ok := args["title"]; ok {
			s := strArg(args, "title")
			patch.Title = &s
		}
		if _, ok := args["description"]; ok {
			s := strArg(args, "description")
			patch.Description = &s
		}
		if _, ok := args["state"]; ok {
			s := strArg(args, "state")
			patch.StateID = &s
		}
		if p := intPtrArg(args, "priority"); p != nil {
			patch.Priority = p
		}
		if _, ok := args["parent"]; ok {
			s := strArg(args, "parent")
			patch.ParentID = &s
		}
		if _, ok := args["assignee"]; ok {
			s := strArg(args, "assignee")
			patch.AssigneeID = &s
		}
		if _, ok := args["cycle"]; ok {
			s := strArg(args, "cycle")
			patch.CycleID = &s
		}
		if _, ok := args["labels"]; ok {
			lbs := strSliceArg(args, "labels")
			patch.LabelIDs = &lbs
		}
		iss, err := api.UpdateIssue(ref, patch)
		if err != nil {
			return "", err
		}
		return toPrettyJSON(iss), nil

	case "add_comment":
		cm, err := api.AddComment(strArg(args, "ref"), strArg(args, "body"))
		if err != nil {
			return "", err
		}
		return toPrettyJSON(cm), nil

	case "search_issues":
		limit := 20
		if l := intPtrArg(args, "limit"); l != nil && *l > 0 {
			limit = *l
		}
		hits, err := api.SearchIssues(strArg(args, "query"), limit)
		if err != nil {
			return "", err
		}
		return toPrettyJSON(hits), nil

	case "delete_issue":
		ref := strArg(args, "ref")
		if ref == "" {
			return "", fmt.Errorf("ref is required (e.g. DEV-1)")
		}
		if err := api.DeleteIssue(ref); err != nil {
			return "", err
		}
		return "Deleted " + ref, nil

	case "get_project":
		bundle, err := api.GetProject(strArg(args, "project"))
		if err != nil {
			return "", err
		}
		return toPrettyJSON(bundle), nil

	case "list_users":
		users, err := api.ListUsers()
		if err != nil {
			return "", err
		}
		return toPrettyJSON(users), nil

	case "create_label":
		lb, err := api.CreateLabel(strArg(args, "project"), strArg(args, "name"), strArg(args, "color"))
		if err != nil {
			return "", err
		}
		return toPrettyJSON(lb), nil

	case "create_cycle":
		cy, err := api.CreateCycle(strArg(args, "project"), strArg(args, "name"),
			strArg(args, "start_date"), strArg(args, "end_date"), strArg(args, "status"))
		if err != nil {
			return "", err
		}
		return toPrettyJSON(cy), nil

	case "export_project":
		format := strArg(args, "format")
		if format == "" {
			format = "markdown"
		}
		out, err := api.ExportProject(strArg(args, "project"), format)
		if err != nil {
			return "", err
		}
		return string(out), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_projects",
			"description": "List all Velo projects with keys and open issue counts.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "list_issues",
			"description": "List issues filtered by project key (e.g. DEV), workflow state name, category (backlog|unstarted|started|completed|canceled), priority (0..4), or search query.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":  map[string]any{"type": "string", "description": "Project key such as DEV"},
					"state":    map[string]any{"type": "string", "description": "Workflow state name such as Todo or In Progress"},
					"category": map[string]any{"type": "string", "description": "backlog, unstarted, started, completed, or canceled"},
					"priority": map[string]any{"type": "integer", "description": "0=None, 1=Low, 2=Medium, 3=High, 4=Urgent"},
					"query":    map[string]any{"type": "string", "description": "Optional FTS5 search text"},
					"assignee": map[string]any{"type": "string", "description": "User email, \"me\", or \"none\" for unassigned"},
					"cycle":    map[string]any{"type": "string", "description": "Cycle name, or \"none\" for issues without a cycle"},
					"label":    map[string]any{"type": "string", "description": "Label name"},
					"parent":   map[string]any{"type": "string", "description": "Parent identifier (e.g. DEV-1) to list its sub-issues, or \"root\" for top-level only"},
					"limit":    map[string]any{"type": "integer", "description": "Max issues to return (default 30)"},
				},
			},
		},
		{
			"name":        "get_issue",
			"description": "Get full details, sub-issues, comments, and activity log for an issue identifier (e.g. DEV-12).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref": map[string]any{"type": "string", "description": "Issue identifier (e.g. DEV-12) or ID"},
				},
				"required": []string{"ref"},
			},
		},
		{
			"name":        "create_issue",
			"description": "Create a new issue or sub-issue in a project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":     map[string]any{"type": "string", "description": "Project key (e.g. DEV)"},
					"title":       map[string]any{"type": "string", "description": "Concise issue title"},
					"description": map[string]any{"type": "string", "description": "Markdown description / acceptance criteria"},
					"state":       map[string]any{"type": "string", "description": "Optional initial state name (default Todo)"},
					"priority":    map[string]any{"type": "integer", "description": "0=None, 1=Low, 2=Medium, 3=High, 4=Urgent"},
					"parent":      map[string]any{"type": "string", "description": "Optional parent issue identifier (e.g. DEV-1) to create a sub-issue"},
					"assignee":    map[string]any{"type": "string", "description": "Optional assignee email, or \"me\""},
					"cycle":       map[string]any{"type": "string", "description": "Optional cycle name"},
					"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Label names such as bug, feature"},
				},
				"required": []string{"project", "title"},
			},
		},
		{
			"name":        "update_issue",
			"description": "Update an existing issue's workflow state (e.g. 'In Progress', 'In Review', 'Done'), priority, title, description, assignee, cycle, parent, or labels. Pass an empty string to clear assignee, cycle or parent.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":         map[string]any{"type": "string", "description": "Issue identifier (e.g. DEV-12)"},
					"state":       map[string]any{"type": "string", "description": "Target state name (e.g. In Progress, In Review, Done)"},
					"priority":    map[string]any{"type": "integer", "description": "0..4"},
					"title":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"parent":      map[string]any{"type": "string", "description": "Parent identifier (e.g. DEV-1)"},
					"assignee":    map[string]any{"type": "string", "description": "User email or \"me\""},
					"cycle":       map[string]any{"type": "string", "description": "Cycle name"},
					"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Full list of label names (replaces current labels)"},
				},
				"required": []string{"ref"},
			},
		},
		{
			"name":        "add_comment",
			"description": "Add a Markdown comment to an issue (e.g. implementation summary, root cause, commit hash).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":  map[string]any{"type": "string", "description": "Issue identifier (e.g. DEV-12)"},
					"body": map[string]any{"type": "string", "description": "Markdown comment body"},
				},
				"required": []string{"ref", "body"},
			},
		},
		{
			"name":        "search_issues",
			"description": "Fast SQLite FTS5 full-text search across issue identifiers, titles, and descriptions.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Keywords or identifier"},
					"limit": map[string]any{"type": "integer", "description": "Max results (default 20)"},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "delete_issue",
			"description": "Permanently delete an issue with its comments and activity. Sub-issues are kept and become top-level.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"ref": map[string]any{"type": "string", "description": "Issue identifier (e.g. DEV-12)"}},
				"required":   []string{"ref"},
			},
		},
		{
			"name":        "get_project",
			"description": "Get a project with its workflow states, labels and cycles (names usable in other tools).",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"project": map[string]any{"type": "string", "description": "Project key (e.g. DEV)"}},
				"required":   []string{"project"},
			},
		},
		{
			"name":        "list_users",
			"description": "List workspace users (emails usable as assignee).",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "create_label",
			"description": "Create a label in a project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project key (e.g. DEV)"},
					"name":    map[string]any{"type": "string"},
					"color":   map[string]any{"type": "string", "description": "Optional hex color such as #ef4444"},
				},
				"required": []string{"project", "name"},
			},
		},
		{
			"name":        "create_cycle",
			"description": "Create a cycle (sprint) in a project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":    map[string]any{"type": "string", "description": "Project key (e.g. DEV)"},
					"name":       map[string]any{"type": "string"},
					"start_date": map[string]any{"type": "string", "description": "YYYY-MM-DD"},
					"end_date":   map[string]any{"type": "string", "description": "YYYY-MM-DD"},
					"status":     map[string]any{"type": "string", "description": "active (default), upcoming, or completed"},
				},
				"required": []string{"project", "name"},
			},
		},
		{
			"name":        "export_project",
			"description": "Export a whole project as Markdown (readable summary) or JSON (full bundle).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project key (e.g. DEV)"},
					"format":  map[string]any{"type": "string", "description": "markdown (default) or json"},
				},
				"required": []string{"project"},
			},
		},
	}
}
