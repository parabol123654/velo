package importer

import (
	"strings"
	"testing"
	"time"
)

// A BOM, CRLF line endings, quoted commas, quotes and CRLF inside quoted
// fields, and semicolons in comment bodies all survive.
func TestQAJiraBOMCRLFAndQuoting(t *testing.T) {
	csv := "\xef\xbb\xbfSummary,Issue key,Description,Comment,Comment\r\n" +
		"\"Fix \"\"login\"\", now\",WEB-1,\"line 1\r\nline 2, with comma\",\"02/Mar/24 11:00 AM;u1;a; b; c\",\"03/Mar/24 9:05 PM;u2;multi\r\nline; body\"\r\n" +
		"Two,WEB-2,,,\r\n"
	b, warnings, err := (&JiraCSV{}).Batch(strings.NewReader(csv))
	if err != nil || len(warnings) != 0 || len(b.Issues) != 2 {
		t.Fatalf("batch: %+v %v %v", b, warnings, err)
	}
	one := b.Issues[0]
	if one.Title != `Fix "login", now` || one.Description != "line 1\nline 2, with comma" {
		t.Fatalf("WEB-1 text: %q / %q", one.Title, one.Description)
	}
	if len(one.Comments) != 2 || one.Comments[0].Author != "u1" || one.Comments[0].Body != "a; b; c" ||
		one.Comments[1].Body != "multi\nline; body" || !one.Comments[1].CreatedAt.Equal(time.Date(2024, 3, 3, 21, 5, 0, 0, time.UTC)) {
		t.Fatalf("WEB-1 comments: %+v", one.Comments)
	}
	if b.Issues[1].Title != "Two" || len(b.Issues[1].Comments) != 0 {
		t.Fatalf("WEB-2: %+v", b.Issues[1])
	}
}

// Repeated columns keep every non-empty value in order; the latest non-empty
// sprint wins; header names match ignoring case and spaces; ragged rows do
// not panic.
func TestQAJiraRepeatedAndRaggedColumns(t *testing.T) {
	csv := " SUMMARY ,issue KEY,Labels,Labels,Labels,Sprint,Sprint,Sprint\n" +
		"A,WEB-1,x,,z,S1,S2,\n" +
		"B,WEB-2\n" + // short row
		"C,WEB-3,,,,,,,extra,extra\n" // long row
	b, _, err := (&JiraCSV{}).Batch(strings.NewReader(csv))
	if err != nil || len(b.Issues) != 3 {
		t.Fatalf("batch: %+v %v", b, err)
	}
	if strings.Join(b.Issues[0].Labels, ",") != "x,z" || b.Issues[0].Cycle != "S2" {
		t.Fatalf("WEB-1: %+v", b.Issues[0])
	}
	if b.Issues[1].Title != "B" || b.Issues[1].Labels != nil || b.Issues[1].Cycle != "" {
		t.Fatalf("WEB-2: %+v", b.Issues[1])
	}
}

// A header-only file is an empty batch, not an error; a blank line between
// rows is skipped.
func TestQAJiraHeaderOnlyAndBlankLines(t *testing.T) {
	b, _, err := (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key\r\n"))
	if err != nil || len(b.Issues) != 0 {
		t.Fatalf("header only: %+v %v", b, err)
	}
	b, _, err = (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key\n\nA,WEB-1\n\n\nB,WEB-2\n\n"))
	if err != nil || len(b.Issues) != 2 {
		t.Fatalf("blank lines: %+v %v", b, err)
	}
}

// A row whose every field is empty, as a spreadsheet leaves when it saves a
// CSV with trailing empty rows, is skipped rather than failing the file.
func TestQAJiraEmptyRowsFromSpreadsheet(t *testing.T) {
	csv := "Summary,Issue key,Status\nA,WEB-1,To Do\n,,\n , , \n"
	b, _, err := (&JiraCSV{}).Batch(strings.NewReader(csv))
	if err != nil || len(b.Issues) != 1 {
		t.Fatalf("want WEB-1 only and no error, got %+v %v", b, err)
	}
}

// A row with content but no key is still refused, naming the row.
func TestQAJiraRowWithoutKey(t *testing.T) {
	_, _, err := (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key\nA,WEB-1\nOrphan,\n"))
	if err == nil || !strings.Contains(err.Error(), "row 3 has no issue key") {
		t.Fatalf("got %v", err)
	}
}

// Links: outward and inward of the same link collapse to one; links to
// issues outside the file are dropped with a warning, inward ones too; a
// parent given by key, by id, or by an id outside the file.
func TestQAJiraLinksAndParents(t *testing.T) {
	csv := "Summary,Issue key,Issue id,Parent,Outward issue link (Blocks),Inward issue link (Blocks),Inward issue link (Duplicate),Outward issue link (Relates),Outward issue link (Cloners)\n" +
		"A,WEB-1,1,,WEB-2,,,WEB-2,WEB-2\n" +
		"B,WEB-2,2,WEB-1,,WEB-1,EXT-5,WEB-1,\n" +
		"C,WEB-3,3,2,,EXT-9,,,\n" +
		"D,WEB-4,4,99999,,,,,\n"
	b, warnings, err := (&JiraCSV{}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	var links []string
	for _, l := range b.Links {
		links = append(links, l.Source+" "+l.Kind+" "+l.Target)
	}
	if strings.Join(links, "; ") != "WEB-1 blocks WEB-2; WEB-1 relates WEB-2; WEB-2 relates WEB-1" {
		t.Fatalf("links: %v", links)
	}
	if b.Issues[1].Parent != "WEB-1" || b.Issues[2].Parent != "WEB-2" || b.Issues[3].Parent != "" {
		t.Fatalf("parents: %q %q %q", b.Issues[1].Parent, b.Issues[2].Parent, b.Issues[3].Parent)
	}
	all := strings.Join(warnings, "\n")
	for _, w := range []string{"EXT-5 duplicates WEB-2", "EXT-9 blocks WEB-3", "WEB-4: parent 99999"} {
		if !strings.Contains(all, w) {
			t.Fatalf("no warning %q in %v", w, warnings)
		}
	}
}

// Parent loops and self parents pass through the importer as they are; the
// server refuses them (see the CLI QA tests).
func TestQAJiraParentLoopIsPassedOn(t *testing.T) {
	b, warnings, err := (&JiraCSV{}).Batch(strings.NewReader("Summary,Issue key,Parent\nA,WEB-1,WEB-2\nB,WEB-2,WEB-1\nC,WEB-3,WEB-3\n"))
	if err != nil || len(warnings) != 0 || b.Issues[0].Parent != "WEB-2" || b.Issues[1].Parent != "WEB-1" || b.Issues[2].Parent != "WEB-3" {
		t.Fatalf("%+v %v %v", b, warnings, err)
	}
}

// Vietnamese and other Unicode: user and state maps match names in any case,
// labels and text keep their marks.
func TestQAJiraUnicode(t *testing.T) {
	csv := "Summary,Issue key,Status,Assignee,Reporter,Labels,Sprint\n" +
		"Sửa lỗi đăng nhập 🐛,VN-1,ĐANG LÀM,NGUYỄN VĂN Ấ,Trần Thị Ḅ,lỗi-gấp,Đợt 1\n" +
		"日本語,VN-2,完了,Ünïcödé,,タグ,\n"
	j := &JiraCSV{Users: map[string]string{"nguyễn văn ấ": "a@velo.dev"}, StateMap: map[string]string{"đang làm": "Đang làm"}}
	b, _, err := j.Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	one, two := b.Issues[0], b.Issues[1]
	if one.Title != "Sửa lỗi đăng nhập 🐛" || one.State != "Đang làm" || one.Assignee != "a@velo.dev" || one.Author != "Trần Thị Ḅ" ||
		strings.Join(one.Labels, ",") != "lỗi-gấp" || one.Cycle != "Đợt 1" {
		t.Fatalf("VN-1: %+v", one)
	}
	if two.Title != "日本語" || two.State != "完了" || two.Assignee != "Ünïcödé" || two.StateCategory != "unstarted" {
		t.Fatalf("VN-2: %+v", two)
	}
}

// Dates in the forms Jira writes: one- and two-digit days and hours, noon
// and midnight, four-digit years, ISO; an unreadable date is the zero time.
func TestQAJiraDates(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2/Mar/24 9:05 AM":          time.Date(2024, 3, 2, 9, 5, 0, 0, time.UTC),
		"02/Mar/24 12:30 PM":        time.Date(2024, 3, 2, 12, 30, 0, 0, time.UTC),
		"02/Mar/24 12:30 AM":        time.Date(2024, 3, 2, 0, 30, 0, 0, time.UTC),
		"02/Mar/24 23:15":           time.Date(2024, 3, 2, 23, 15, 0, 0, time.UTC),
		"02/Mar/2024 10:00 AM":      time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC),
		"2024-03-02 10:00":          time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC),
		"2024-03-02 10:00:59":       time.Date(2024, 3, 2, 10, 0, 59, 0, time.UTC),
		"2024-03-02T10:00:00+07:00": time.Date(2024, 3, 2, 3, 0, 0, 0, time.UTC),
		" 02/Mar/24 10:00 AM ":      time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC),
		"02.03.2024 10:00":          {},
		"30/Feb/24 10:00 AM":        {},
		"":                          {},
	} {
		if got := parseTime(in); !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
	// A comment whose date cannot be read keeps its whole text as the body.
	c := jiraComment("WEB-1", 1, "02.03.2024 10:00;u;body", &notes{})
	if c.Body != "02.03.2024 10:00;u;body" || c.Author != "" || !c.CreatedAt.IsZero() {
		t.Fatalf("unreadable comment date: %+v", c)
	}
	// A comment with only a date and an author has an empty body.
	if c := jiraComment("WEB-1", 2, "02/Mar/24 10:00 AM;u;", &notes{}); c.Author != "u" {
		t.Fatalf("empty body comment: %+v", c)
	}
}

// Status mapping without a Status Category column, resolutions, and a
// state-map entry that wins over the status name.
func TestQAJiraStates(t *testing.T) {
	j := &JiraCSV{StateMap: map[string]string{"closed": "Shipped"}}
	for _, tc := range []struct{ status, category, resolution, name, cat string }{
		{"Backlog", "", "", "Backlog", "backlog"},
		{"In Review", "", "", "In Review", "started"},
		{"Resolved", "", "Duplicate", "", "canceled"}, // canceled whatever the status is called
		{"Closed", "", "Fixed", "Shipped", ""},        // a --state-map entry is taken as given
		{"Closed", "", "Won't Fix", "Shipped", ""},    // even over the resolution
		{"Weird", "Done", "won't do", "", "canceled"},
		{"Weird", "In Progress", "Won't Do", "Weird", "started"},
		{"", "", "", "", "unstarted"},
	} {
		name, cat := j.state(tc.status, tc.category, tc.resolution)
		if name != tc.name || cat != tc.cat {
			t.Errorf("state(%q, %q, %q) = %q %q, want %q %q", tc.status, tc.category, tc.resolution, name, cat, tc.name, tc.cat)
		}
	}
}

// A very long description and many comments are kept whole.
func TestQAJiraLongFields(t *testing.T) {
	long := strings.Repeat("Đ,\"x\"\n", 200000)
	var row strings.Builder
	row.WriteString("Summary,Issue key,Description")
	for i := 0; i < 50; i++ {
		row.WriteString(",Comment")
	}
	row.WriteString("\nA,WEB-1,\"" + strings.ReplaceAll(long, `"`, `""`) + "\"")
	for i := 0; i < 50; i++ {
		row.WriteString(",02/Mar/24 10:00 AM;u;c")
	}
	row.WriteString("\n")
	b, _, err := (&JiraCSV{}).Batch(strings.NewReader(row.String()))
	if err != nil || b.Issues[0].Description != strings.TrimSpace(long) || len(b.Issues[0].Comments) != 50 || b.Issues[0].Comments[49].ExternalID != "WEB-1#c50" {
		t.Fatalf("long: %v, %d comments", err, len(b.Issues[0].Comments))
	}
}

// Files that are not Jira exports: a semicolon-separated file, a file of
// only a BOM and whitespace.
func TestQAJiraNotAnExport(t *testing.T) {
	if _, _, err := (&JiraCSV{}).Batch(strings.NewReader("Summary;Issue key\nA;WEB-1\n")); err == nil {
		t.Fatal("semicolon-separated file accepted")
	}
	if _, _, err := (&JiraCSV{}).Batch(strings.NewReader("\xef\xbb\xbf \r\n\t\n")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("BOM only: %v", err)
	}
}
