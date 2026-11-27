package importer

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

func readJira(t *testing.T, j *JiraCSV, name string) (*db.ImportBatch, []string, error) {
	t.Helper()
	f, err := os.Open("testdata/jira/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return j.Batch(f)
}

// #16: a Jira CSV export becomes a batch: repeated Labels, Sprint and
// Comment columns, quoted multi-line text, Status Category and Resolution
// for the state, priorities, sub-tasks by parent ID, and issue links.
func TestJiraBatch(t *testing.T) {
	j := &JiraCSV{
		BaseURL:  "https://acme.atlassian.net/",
		Users:    map[string]string{"nguyễn văn a": "a@velo.dev"},
		StateMap: map[string]string{"qa": "In Review"},
	}
	b, warnings, err := readJira(t, j, "export.csv")
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "jira" || len(b.Issues) != 4 {
		t.Fatalf("source %q, %d issues", b.Source, len(b.Issues))
	}
	one, two, three, four := b.Issues[0], b.Issues[1], b.Issues[2], b.Issues[3]
	if one.ExternalID != "WEB-1" || one.URL != "https://acme.atlassian.net/browse/WEB-1" || one.Title != "Login fails on Safari" ||
		one.Author != "Trần Thị B" || one.State != "Done" || one.StateCategory != "completed" || one.Priority != 3 ||
		strings.Join(one.Labels, ",") != "bug,Lỗi" || one.Cycle != "Sprint 2" || one.Assignee != "a@velo.dev" ||
		one.Description != "Steps:\n1. Open Safari, then \"Login\"\n2. See error" ||
		!one.CreatedAt.Equal(time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("WEB-1: %+v", one)
	}
	if len(one.Comments) != 2 || one.Comments[0].ExternalID != "WEB-1#c1" || one.Comments[0].Body != "Reproduced, on 17.2." ||
		one.Comments[0].Author != "557058:abc" || !one.Comments[0].CreatedAt.Equal(time.Date(2024, 3, 2, 11, 0, 0, 0, time.UTC)) ||
		one.Comments[1].Body != "Đã sửa." || !one.Comments[1].CreatedAt.Equal(time.Date(2024, 3, 3, 9, 5, 0, 0, time.UTC)) {
		t.Fatalf("WEB-1 comments: %+v", one.Comments)
	}
	if two.StateCategory != "canceled" || two.Priority != 1 || two.Assignee != "" || !two.CreatedAt.Equal(time.Date(2024, 3, 3, 20, 30, 0, 0, time.UTC)) {
		t.Fatalf("WEB-2 (Won't Do): %+v", two)
	}
	if three.State != "In Review" || three.StateCategory != "started" || three.Assignee != "Bob Smith" || three.Cycle != "Sprint 2" || three.Priority != 2 {
		t.Fatalf("WEB-3: %+v", three)
	}
	if four.Parent != "WEB-3" || four.Priority != 4 || four.StateCategory != "unstarted" {
		t.Fatalf("WEB-4 (sub-task of 10003): %+v", four)
	}
	links := []string{}
	for _, l := range b.Links {
		links = append(links, l.Source+" "+l.Kind+" "+l.Target)
	}
	if strings.Join(links, "; ") != "WEB-1 blocks WEB-3; WEB-2 duplicates WEB-1" {
		t.Fatalf("links: %v", links)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "WEB-2 relates OTHER-9") {
		t.Fatalf("warnings: %v", warnings)
	}
}

// A file without the columns an import needs, or that is not CSV, is
// refused with a message naming the problem.
func TestJiraBadFiles(t *testing.T) {
	if _, _, err := readJira(t, &JiraCSV{}, "no-summary.csv"); err == nil || !strings.Contains(err.Error(), `no "Summary" column`) {
		t.Fatalf("missing column: %v", err)
	}
	if _, _, err := (&JiraCSV{}).Batch(strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
	if _, _, err := (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key\n\"unclosed,WEB-1\n")); err == nil {
		t.Fatal("broken CSV accepted")
	}
	b, _, err := (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key\nOnly,WEB-9\n,WEB-10\n"))
	if err != nil || len(b.Issues) != 2 || b.Issues[1].Title != "WEB-10" {
		t.Fatalf("minimal file (an empty summary falls back to the key): %+v %v", b, err)
	}
}
