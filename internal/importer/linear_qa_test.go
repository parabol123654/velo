package importer

import (
	"strings"
	"testing"
	"time"
)

var qaLinearNow = time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)

// A BOM, CRLF line endings, quoted commas and newlines, and a header in
// other case survive; missing optional columns are fine.
func TestQALinearBOMCRLFAndMinimalColumns(t *testing.T) {
	csv := "\xef\xbb\xbfid,TITLE,Description,Labels\r\n" +
		"ENG-1,\"Hello, \"\"world\"\"\",\"a\r\nb, c\",\" Bug ,, Lỗi,bug \"\r\n" +
		"ENG-2,Only title,,\r\n"
	b, warnings, err := (&LinearCSV{Now: qaLinearNow}).Batch(strings.NewReader(csv))
	if err != nil || len(warnings) != 0 || len(b.Issues) != 2 {
		t.Fatalf("batch: %+v %v %v", b, warnings, err)
	}
	one := b.Issues[0]
	if one.Title != `Hello, "world"` || one.Description != "a\nb, c" || strings.Join(one.Labels, "|") != "Bug|Lỗi|bug" ||
		one.StateCategory != "unstarted" || one.Priority != 0 || len(b.Cycles) != 0 {
		t.Fatalf("ENG-1: %+v", one)
	}
}

// A row whose every field is empty, as a spreadsheet leaves when it saves a
// CSV with trailing empty rows, is skipped rather than failing the file.
func TestQALinearEmptyRowsFromSpreadsheet(t *testing.T) {
	csv := "ID,Title,Status\nENG-1,A,Todo\n,,\n"
	b, _, err := (&LinearCSV{Now: qaLinearNow}).Batch(strings.NewReader(csv))
	if err != nil || len(b.Issues) != 1 {
		t.Fatalf("want ENG-1 only and no error, got %+v %v", b, err)
	}
}

// States: Canceled wins over Completed and Started; Triage is backlog;
// statuses are kept by name for the server to match.
func TestQALinearStates(t *testing.T) {
	csv := "ID,Title,Status,Started,Completed,Canceled\n" +
		"E-1,a,Canceled,2024-01-01,2024-01-02,2024-01-03\n" +
		"E-2,b,Done,2024-01-01,2024-01-02,\n" +
		"E-3,c,Triage,,,\n" +
		"E-4,d,In Review,2024-01-01,,\n" +
		"E-5,e,TODO,,,\n"
	b, _, err := (&LinearCSV{Now: qaLinearNow}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Canceled canceled", "Done completed", "Triage backlog", "In Review started", "TODO unstarted"}
	for i, w := range want {
		if got := b.Issues[i].State + " " + b.Issues[i].StateCategory; got != w {
			t.Errorf("E-%d: %q, want %q", i+1, got, w)
		}
	}
}

// Priorities in any case; an unknown one is none.
func TestQALinearPriorities(t *testing.T) {
	csv := "ID,Title,Priority\nE-1,a,URGENT\nE-2,b,no priority\nE-3,c, low \nE-4,d,P1\nE-5,e,\n"
	b, _, err := (&LinearCSV{}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{4, 0, 1, 0, 0} {
		if b.Issues[i].Priority != want {
			t.Errorf("E-%d priority %d, want %d", i+1, b.Issues[i].Priority, want)
		}
	}
}

// Cycles: one per name, status from Now and the dates, Cycle N when there
// is no name, dates in JavaScript's Date.toString form.
func TestQALinearCycles(t *testing.T) {
	csv := "ID,Title,Cycle Number,Cycle Name,Cycle Start,Cycle End\n" +
		"E-1,a,1,Past,2024-01-01T00:00:00.000Z,2024-01-14T00:00:00.000Z\n" +
		"E-2,b,2,Now,Mon Mar 04 2024 07:00:00 GMT+0700 (Giờ Đông Dương),Sun Mar 17 2024 00:00:00 GMT-0700 (Pacific Daylight Time)\n" +
		"E-3,c,3,,2024-04-01,2024-04-14\n" +
		"E-4,d,2,Now,,\n" +
		"E-5,e,,,,\n"
	b, _, err := (&LinearCSV{Now: qaLinearNow}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Cycles) != 3 {
		t.Fatalf("cycles: %+v", b.Cycles)
	}
	past, now, future := b.Cycles[0], b.Cycles[1], b.Cycles[2]
	if past.Name != "Past" || past.Status != "completed" || past.StartDate != "2024-01-01" || past.EndDate != "2024-01-14" {
		t.Fatalf("past: %+v", past)
	}
	if now.Name != "Now" || now.Status != "active" || now.StartDate != "2024-03-04" || now.EndDate != "2024-03-17" {
		t.Fatalf("now: %+v", now)
	}
	if future.Name != "Cycle 3" || future.Status != "upcoming" {
		t.Fatalf("future: %+v", future)
	}
	if b.Issues[3].Cycle != "Now" || b.Issues[4].Cycle != "" {
		t.Fatalf("issue cycles: %q %q", b.Issues[3].Cycle, b.Issues[4].Cycle)
	}
}

// Parents: the key at the start of the cell, a parent outside the file with
// a warning, a value that is not a key ignored; loops are passed on for the
// server to refuse.
func TestQALinearParents(t *testing.T) {
	csv := "ID,Title,Parent issue\n" +
		"ENG-1,a,ENG-2 Some title\n" +
		"ENG-2,b,ENG-1\n" +
		"ENG-3,c,OPS-7\n" +
		"ENG-4,d,not a key\n" +
		"ENG-5,e,ENG-5\n"
	b, warnings, err := (&LinearCSV{}).Batch(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, it := range b.Issues {
		got = append(got, it.Parent)
	}
	if strings.Join(got, ",") != "ENG-2,ENG-1,,,ENG-5" {
		t.Fatalf("parents: %v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ENG-3: parent OPS-7") {
		t.Fatalf("warnings: %v", warnings)
	}
}

// Unicode names map users ignoring case; titles keep their marks; an empty
// title falls back to the ID; a row without an ID names its row.
func TestQALinearUnicodeAndBadRows(t *testing.T) {
	csv := "ID,Title,Assignee,Creator\nVN-1,Sửa lỗi ổ cứng,ĐỖ THỊ Ể,Ngô Bảo Châu\nVN-2,,,\n"
	b, _, err := (&LinearCSV{Users: map[string]string{"đỗ thị ể": "e@velo.dev"}}).Batch(strings.NewReader(csv))
	if err != nil || b.Issues[0].Title != "Sửa lỗi ổ cứng" || b.Issues[0].Assignee != "e@velo.dev" || b.Issues[0].Author != "Ngô Bảo Châu" || b.Issues[1].Title != "VN-2" {
		t.Fatalf("%+v %v", b, err)
	}
	if _, _, err := (&LinearCSV{}).Batch(strings.NewReader("ID,Title\nE-1,a\n,b\n")); err == nil || !strings.Contains(err.Error(), "row 3 has no ID") {
		t.Fatalf("row without ID: %v", err)
	}
	if _, _, err := (&LinearCSV{}).Batch(strings.NewReader("ID\nE-1\n")); err == nil || !strings.Contains(err.Error(), `no "Title" column`) {
		t.Fatalf("no Title column: %v", err)
	}
	if _, _, err := (&LinearCSV{}).Batch(strings.NewReader("ID,Title\n\"E-1,a\n")); err == nil {
		t.Fatal("unclosed quote accepted")
	}
}
