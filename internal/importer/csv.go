package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// table is a CSV export with a header row. A column name may repeat, as
// Jira repeats Labels, Sprint and Comment.
type table struct {
	cols map[string][]int // lower-cased header -> column indexes, in order
	rows [][]string
}

func readTable(r io.Reader, required ...string) (*table, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // Excel and Jira add a BOM
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("the file is empty")
	}
	cr := csv.NewReader(bytes.NewReader(raw))
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("not a CSV file: %w", err)
	}
	t := &table{cols: map[string][]int{}, rows: records[1:]}
	for i, name := range records[0] {
		key := strings.ToLower(strings.TrimSpace(name))
		t.cols[key] = append(t.cols[key], i)
	}
	for _, name := range required {
		if len(t.cols[strings.ToLower(name)]) == 0 {
			return nil, fmt.Errorf("no %q column: export the CSV with all fields", name)
		}
	}
	return t, nil
}

// get returns the first non-empty value of a column in a row.
func (t *table) get(row []string, name string) string {
	for _, v := range t.all(row, name) {
		return v
	}
	return ""
}

// all returns the non-empty values of every column with that name.
func (t *table) all(row []string, name string) []string {
	var out []string
	for _, i := range t.cols[strings.ToLower(name)] {
		if i < len(row) {
			if v := strings.TrimSpace(row[i]); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// priorityOf maps the priority names Jira and Linear use onto Velo's 0-4.
func priorityOf(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "urgent", "highest", "blocker", "critical":
		return 4
	case "high", "major":
		return 3
	case "medium", "normal":
		return 2
	case "low", "lowest", "minor", "trivial":
		return 1
	}
	return 0
}

// Date formats seen in exports. Jira writes the exporting user's format and
// time zone without saying which; times are read as UTC.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.000Z",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"02/Jan/06 3:04 PM",
	"2/Jan/06 3:04 PM",
	"02/Jan/06 15:04",
	"02/Jan/2006 3:04 PM",
	"2006-01-02",
}

// parseTime returns the zero time for a value it cannot read: the server
// then uses the import time.
func parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// keepInside drops parents and links that name issues outside the batch,
// which an export filtered by project or date often has, and says which.
func keepInside(b *db.ImportBatch) []string {
	in := map[string]bool{}
	for _, it := range b.Issues {
		in[it.ExternalID] = true
	}
	var warnings []string
	for i, it := range b.Issues {
		if it.Parent != "" && !in[it.Parent] {
			warnings = append(warnings, fmt.Sprintf("%s: parent %s is not in the file; imported without a parent", it.ExternalID, it.Parent))
			b.Issues[i].Parent = ""
		}
	}
	links := b.Links[:0]
	seen := map[string]bool{}
	for _, l := range b.Links {
		switch {
		case !in[l.Source] || !in[l.Target]:
			warnings = append(warnings, fmt.Sprintf("link %s %s %s: an issue is not in the file; left out", l.Source, l.Kind, l.Target))
		case !seen[l.Source+" "+l.Kind+" "+l.Target]:
			seen[l.Source+" "+l.Kind+" "+l.Target] = true
			links = append(links, l)
		}
	}
	b.Links = links
	return warnings
}
