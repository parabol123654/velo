package importer

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// LinearCSV reads a Linear CSV export (Settings > Import / export > Export
// CSV). The export has no comments or issue relations, so neither is
// imported.
type LinearCSV struct {
	Workspace string            // the workspace's URL name, for links to the originals; optional
	Users     map[string]string // lower-cased Linear name -> email of the Velo user to assign
	Now       time.Time         // for cycle status; zero means now
}

var linearKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+`)

// Batch reads the export. The warnings name parents outside the file.
func (l *LinearCSV) Batch(r io.Reader) (*db.ImportBatch, []string, error) {
	t, err := readTable(r, "ID", "Title")
	if err != nil {
		return nil, nil, err
	}
	now := l.Now
	if now.IsZero() {
		now = time.Now()
	}
	b := &db.ImportBatch{Source: "linear"}
	cycles := map[string]bool{}
	for n, row := range t.rows {
		key := t.get(row, "ID")
		if key == "" {
			return nil, nil, fmt.Errorf("row %d has no ID", n+2)
		}
		it := db.ImportIssue{
			ExternalID:  key,
			Title:       t.get(row, "Title"),
			Description: t.get(row, "Description"),
			Author:      t.get(row, "Creator"),
			Priority:    priorityOf(t.get(row, "Priority")),
			CreatedAt:   parseTime(t.get(row, "Created")),
			State:       t.get(row, "Status"),
		}
		if it.Title == "" {
			it.Title = key
		}
		if l.Workspace != "" {
			it.URL = "https://linear.app/" + l.Workspace + "/issue/" + key
		}
		switch {
		case t.get(row, "Canceled") != "":
			it.StateCategory = "canceled"
		case t.get(row, "Completed") != "":
			it.StateCategory = "completed"
		case t.get(row, "Started") != "":
			it.StateCategory = "started"
		case strings.EqualFold(it.State, "backlog") || strings.EqualFold(it.State, "triage"):
			it.StateCategory = "backlog"
		default:
			it.StateCategory = "unstarted"
		}
		for _, label := range strings.Split(t.get(row, "Labels"), ",") {
			if label = strings.TrimSpace(label); label != "" {
				it.Labels = append(it.Labels, label)
			}
		}
		if a := t.get(row, "Assignee"); a != "" {
			it.Assignee = a
			if email, ok := l.Users[strings.ToLower(a)]; ok {
				it.Assignee = email
			}
		}
		if p := linearKeyRe.FindString(t.get(row, "Parent issue")); p != "" {
			it.Parent = p
		}
		it.Cycle = t.get(row, "Cycle Name")
		if num := t.get(row, "Cycle Number"); it.Cycle == "" && num != "" {
			it.Cycle = "Cycle " + num
		}
		if it.Cycle != "" && !cycles[it.Cycle] {
			cycles[it.Cycle] = true
			b.Cycles = append(b.Cycles, linearCycle(it.Cycle, t.get(row, "Cycle Start"), t.get(row, "Cycle End"), now))
		}
		b.Issues = append(b.Issues, it)
	}
	return b, keepInside(b), nil
}

// linearCycle dates a cycle and sets its status from today.
func linearCycle(name, start, end string, now time.Time) db.ImportCycle {
	c := db.ImportCycle{Name: name, Status: "upcoming"}
	s, e := parseTime(start), parseTime(end)
	if !s.IsZero() {
		c.StartDate = s.Format("2006-01-02")
	}
	if !e.IsZero() {
		c.EndDate = e.Format("2006-01-02")
	}
	switch {
	case !e.IsZero() && e.Before(now):
		c.Status = "completed"
	case !s.IsZero() && !s.After(now):
		c.Status = "active"
	}
	return c
}
