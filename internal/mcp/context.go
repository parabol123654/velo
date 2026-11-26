package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
)

// MCP resources (read-only context as Markdown) and prompts (ready-made task
// instructions that use Velo's tools).

const (
	issueURIPrefix   = "velo://issue/"
	projectURIPrefix = "velo://project/"
)

func resourceTemplates() []map[string]any {
	return []map[string]any{
		{"uriTemplate": issueURIPrefix + "{identifier}", "name": "Issue", "mimeType": "text/markdown",
			"description": "An issue with its description, links, sub-issues and comments, e.g. velo://issue/DEV-12"},
		{"uriTemplate": projectURIPrefix + "{key}", "name": "Project", "mimeType": "text/markdown",
			"description": "A project's workflow states, labels and cycles, e.g. velo://project/DEV"},
	}
}

// listResources offers every project the token can see; issues are reached
// through the issue template.
func listResources(api *client.Client) ([]map[string]any, error) {
	projects, err := api.ListProjects()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range projects {
		out = append(out, map[string]any{
			"uri": projectURIPrefix + p.Key, "name": p.Key + " — " + p.Name, "mimeType": "text/markdown",
		})
	}
	return out, nil
}

func readResource(api *client.Client, uri string) (string, error) {
	switch {
	case strings.HasPrefix(uri, issueURIPrefix):
		d, err := api.GetIssueDetail(strings.TrimPrefix(uri, issueURIPrefix))
		if err != nil {
			return "", err
		}
		return issueMarkdown(d), nil
	case strings.HasPrefix(uri, projectURIPrefix):
		b, err := api.GetProject(strings.TrimPrefix(uri, projectURIPrefix))
		if err != nil {
			return "", err
		}
		return projectMarkdown(b), nil
	}
	return "", fmt.Errorf("unknown resource %q (use %s<KEY-N> or %s<KEY>)", uri, issueURIPrefix, projectURIPrefix)
}

func issueMarkdown(d *client.IssueDetail) string {
	it := d.Issue
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", it.Identifier, it.Title)
	fmt.Fprintf(&b, "- State: %s (%s)\n- Priority: %s\n- Assignee: %s\n", it.StateName, it.StateCategory, priorityLabel(it.Priority), orNone(it.AssigneeName))
	if it.CycleName != "" {
		fmt.Fprintf(&b, "- Cycle: %s\n", it.CycleName)
	}
	if len(it.Labels) > 0 {
		names := make([]string, len(it.Labels))
		for i, l := range it.Labels {
			names[i] = l.Name
		}
		fmt.Fprintf(&b, "- Labels: %s\n", strings.Join(names, ", "))
	}
	if it.ParentIdentifier != "" {
		fmt.Fprintf(&b, "- Parent: %s\n", it.ParentIdentifier)
	}
	if it.BlockedByCount > 0 {
		fmt.Fprintf(&b, "- Blocked by %d unfinished issue(s)\n", it.BlockedByCount)
	}
	fmt.Fprintf(&b, "\n## Description\n\n%s\n", orNone(strings.TrimSpace(it.Description)))
	if len(it.Links) > 0 {
		b.WriteString("\n## Links\n\n")
		for _, l := range it.Links {
			fmt.Fprintf(&b, "- %s %s [%s] %s\n", strings.ReplaceAll(l.Kind, "_", " "), l.Issue.Identifier, l.Issue.StateName, l.Issue.Title)
		}
	}
	if len(it.SubIssues) > 0 {
		b.WriteString("\n## Sub-issues\n\n")
		for _, s := range it.SubIssues {
			fmt.Fprintf(&b, "- %s [%s] %s\n", s.Identifier, s.StateName, s.Title)
		}
	}
	if len(d.Comments) > 0 {
		b.WriteString("\n## Comments\n")
		for _, c := range d.Comments {
			via := ""
			if c.Via != "" {
				via = " via " + c.Via
			}
			fmt.Fprintf(&b, "\n**%s**%s, %s:\n\n%s\n", c.AuthorName, via, c.CreatedAt.Format("2006-01-02 15:04"), c.Body)
		}
	}
	return b.String()
}

func projectMarkdown(p *client.ProjectBundle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", p.Project.Key, p.Project.Name)
	if p.Project.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", p.Project.Description)
	}
	fmt.Fprintf(&b, "%d open of %d issues.\n\n## Workflow states (in order)\n\n", p.Project.OpenIssueCount, p.Project.IssueCount)
	for _, s := range p.States {
		fmt.Fprintf(&b, "- %s (%s)\n", s.Name, s.Category)
	}
	b.WriteString("\n## Labels\n\n")
	for _, l := range p.Labels {
		fmt.Fprintf(&b, "- %s\n", l.Name)
	}
	if len(p.Cycles) > 0 {
		b.WriteString("\n## Cycles\n\n")
		for _, c := range p.Cycles {
			fmt.Fprintf(&b, "- %s [%s] %s → %s, %d/%d done\n", c.Name, c.Status, orNone(c.StartDate), orNone(c.EndDate), c.CompletedCount, c.IssueCount)
		}
	}
	return b.String()
}

type promptDef struct {
	name, description string
	args              []map[string]any
	build             func(api *client.Client, args map[string]string) (string, error)
}

func arg(name, desc string) map[string]any {
	return map[string]any{"name": name, "description": desc, "required": true}
}

var prompts = []promptDef{
	{
		name:        "work_on_issue",
		description: "Take an issue from claim to review using Velo's tools.",
		args:        []map[string]any{arg("ref", "Issue identifier, e.g. DEV-12")},
		build: func(api *client.Client, a map[string]string) (string, error) {
			d, err := api.GetIssueDetail(a["ref"])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(`Work on %[1]s in this repository.

1. Call claim_issue with ref %[1]s. If it fails (taken, finished or blocked), stop and say why.
2. Do the work described below. Keep the change focused on this issue.
3. As you finish each "- [ ]" item in the description, call check_item for it.
4. When done, call add_comment on %[1]s with a short summary: what changed, how you verified it, and the commit or PR if there is one.
5. Call update_issue to move %[1]s to the review state (see the project's states with get_project), or leave it in progress and comment what is left if you could not finish.

%[2]s`, d.Issue.Identifier, issueMarkdown(d)), nil
		},
	},
	{
		name:        "split_issue",
		description: "Break a large issue into sub-issues with clear acceptance criteria.",
		args:        []map[string]any{arg("ref", "Issue identifier, e.g. DEV-12")},
		build: func(api *client.Client, a map[string]string) (string, error) {
			d, err := api.GetIssueDetail(a["ref"])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(`Split %[1]s into sub-issues that can each be finished and reviewed on their own.

- Propose the list first: a title and a "- [ ]" checklist of acceptance criteria for each.
- After the user agrees, create each one with create_issue (project %[2]s, parent %[1]s).
- When one must be done before another, record it with link_issues (kind "blocks").
- Do not change %[1]s itself except to add a comment listing the new sub-issues.

%[3]s`, d.Issue.Identifier, d.Issue.ProjectKey, issueMarkdown(d)), nil
		},
	},
	{
		name:        "summarize_cycle",
		description: "Summarize a cycle (sprint): done, in progress, blocked and not started.",
		args:        []map[string]any{arg("project", "Project key, e.g. DEV"), arg("cycle", "Cycle name, e.g. Sprint 3")},
		build: func(api *client.Client, a map[string]string) (string, error) {
			issues, err := api.ListIssues(client.IssueQuery{Project: a["project"], Cycle: a["cycle"], Limit: 500})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(`Write a short status summary of cycle %q in %s for the team: what was finished, what is in progress, what is blocked and why, and what has not started. Point out risks to finishing the cycle. Use issue identifiers.

%s`, a["cycle"], a["project"], cycleMarkdown(issues)), nil
		},
	},
}

func cycleMarkdown(issues []db.Issue) string {
	if len(issues) == 0 {
		return "The cycle has no issues."
	}
	order := map[string]int{"completed": 0, "started": 1, "unstarted": 2, "backlog": 3, "canceled": 4}
	sort.SliceStable(issues, func(a, b int) bool { return order[issues[a].StateCategory] < order[issues[b].StateCategory] })
	var b strings.Builder
	for _, it := range issues {
		blocked := ""
		if it.BlockedByCount > 0 {
			blocked = " (blocked)"
		}
		fmt.Fprintf(&b, "- %s [%s]%s %s — %s, %s\n", it.Identifier, it.StateName, blocked, it.Title, priorityLabel(it.Priority), orNone(it.AssigneeName))
	}
	return b.String()
}

func promptList() []map[string]any {
	out := make([]map[string]any, len(prompts))
	for i, p := range prompts {
		out[i] = map[string]any{"name": p.name, "description": p.description, "arguments": p.args}
	}
	return out
}

func getPrompt(api *client.Client, name string, args map[string]string) (map[string]any, error) {
	for _, p := range prompts {
		if p.name != name {
			continue
		}
		for _, a := range p.args {
			if strings.TrimSpace(args[a["name"].(string)]) == "" {
				return nil, fmt.Errorf("argument %q is required", a["name"])
			}
		}
		text, err := p.build(api, args)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"description": p.description,
			"messages":    []map[string]any{{"role": "user", "content": map[string]any{"type": "text", "text": text}}},
		}, nil
	}
	return nil, fmt.Errorf("unknown prompt %q", name)
}

func priorityLabel(p int) string {
	return [...]string{"no priority", "low", "medium", "high", "urgent"}[max(0, min(p, 4))]
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
