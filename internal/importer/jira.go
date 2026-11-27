package importer

import (
	"fmt"
	"io"
	"strings"

	"github.com/parabol123654/velo/internal/db"
)

// JiraCSV reads a Jira CSV export ("Export > Export CSV (all fields)").
type JiraCSV struct {
	BaseURL  string            // https://SITE.atlassian.net, for links to the originals; optional
	Users    map[string]string // lower-cased Jira name -> email of the Velo user to assign
	StateMap map[string]string // lower-cased Jira status -> Velo state name
}

// Jira resolutions that mean the work was not done.
var jiraCanceled = map[string]bool{
	"won't do": true, "won't fix": true, "duplicate": true, "cannot reproduce": true,
	"declined": true, "incomplete": true, "obsolete": true, "rejected": true,
}

// Batch reads the export. The warnings name parents and links that point
// outside the file and were left out.
func (j *JiraCSV) Batch(r io.Reader) (*db.ImportBatch, []string, error) {
	t, err := readTable(r, "Summary", "Issue key")
	if err != nil {
		return nil, nil, err
	}
	b := &db.ImportBatch{Source: "jira"}
	keyOfID := map[string]string{} // Jira issue id -> key, for parents given by id
	for _, row := range t.rows {
		if id := t.get(row, "Issue id"); id != "" {
			keyOfID[id] = t.get(row, "Issue key")
		}
	}
	for n, row := range t.rows {
		key := t.get(row, "Issue key")
		if key == "" {
			return nil, nil, fmt.Errorf("row %d has no issue key", n+2)
		}
		it := db.ImportIssue{
			ExternalID:  key,
			Title:       t.get(row, "Summary"),
			Description: t.get(row, "Description"),
			Author:      t.get(row, "Reporter"),
			Priority:    priorityOf(t.get(row, "Priority")),
			Labels:      t.all(row, "Labels"),
			CreatedAt:   parseTime(t.get(row, "Created")),
		}
		if it.Title == "" {
			it.Title = key
		}
		if j.BaseURL != "" {
			it.URL = strings.TrimRight(j.BaseURL, "/") + "/browse/" + key
		}
		it.State, it.StateCategory = j.state(t.get(row, "Status"), t.get(row, "Status Category"), t.get(row, "Resolution"))
		if sprints := t.all(row, "Sprint"); len(sprints) > 0 {
			it.Cycle = sprints[len(sprints)-1] // the latest sprint
		}
		if a := t.get(row, "Assignee"); a != "" {
			it.Assignee = a
			if email, ok := j.Users[strings.ToLower(a)]; ok {
				it.Assignee = email
			}
		}
		for _, p := range []string{t.get(row, "Parent"), t.get(row, "Parent id"), t.get(row, "Parent key")} {
			if p == "" {
				continue
			}
			if k, ok := keyOfID[p]; ok {
				p = k
			}
			it.Parent = p
			break
		}
		for i, c := range t.all(row, "Comment") {
			it.Comments = append(it.Comments, jiraComment(key, i+1, c))
		}
		b.Issues = append(b.Issues, it)
		b.Links = append(b.Links, jiraLinks(t, row, key)...)
	}
	return b, keepInside(b), nil
}

// state maps a Jira status. A --state-map entry or a Velo state of the same
// name wins; otherwise the status category decides, or the status name when
// the export has no Status Category column.
func (j *JiraCSV) state(status, category, resolution string) (name, cat string) {
	name = status
	if mapped, ok := j.StateMap[strings.ToLower(status)]; ok {
		name = mapped
	}
	switch strings.ToLower(category) {
	case "to do", "new":
		cat = "unstarted"
	case "in progress", "indeterminate":
		cat = "started"
	case "done", "complete":
		cat = "completed"
	default:
		switch strings.ToLower(status) {
		case "backlog":
			cat = "backlog"
		case "in progress", "in review", "in development", "review", "testing":
			cat = "started"
		case "done", "closed", "resolved", "complete", "completed":
			cat = "completed"
		default:
			cat = "unstarted"
		}
	}
	if cat == "completed" && jiraCanceled[strings.ToLower(resolution)] {
		cat = "canceled"
	}
	return name, cat
}

// jiraComment reads "DATE;AUTHOR;BODY". Jira's CSV has no comment IDs, so
// the external ID is the comment's place on the issue.
func jiraComment(key string, n int, raw string) db.ImportComment {
	c := db.ImportComment{ExternalID: fmt.Sprintf("%s#c%d", key, n), Body: raw}
	parts := strings.SplitN(raw, ";", 3)
	if len(parts) == 3 {
		if at := parseTime(parts[0]); !at.IsZero() {
			c.CreatedAt, c.Author, c.Body = at, strings.TrimSpace(parts[1]), parts[2]
		}
	}
	return c
}

// jiraLinks reads the "Outward/Inward issue link (TYPE)" columns. An inward
// link is the outward one seen from the other issue, so both give the same
// link and the batch keeps one.
func jiraLinks(t *table, row []string, key string) []db.ImportLink {
	var out []db.ImportLink
	for _, lt := range []struct{ jira, kind string }{{"blocks", "blocks"}, {"duplicate", "duplicates"}, {"relates", "relates"}} {
		for _, other := range t.all(row, "Outward issue link ("+lt.jira+")") {
			out = append(out, db.ImportLink{Source: key, Kind: lt.kind, Target: other})
		}
		for _, other := range t.all(row, "Inward issue link ("+lt.jira+")") {
			out = append(out, db.ImportLink{Source: other, Kind: lt.kind, Target: key})
		}
	}
	return out
}
