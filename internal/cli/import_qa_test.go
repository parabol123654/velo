package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
)

// QA tests for velo import (#16).

// bigBatch has more issues than one part, a label and a cycle on issues in
// every part, a parent and a link across parts.
func bigBatch() *db.ImportBatch {
	n := importPartIssues + 50
	b := &db.ImportBatch{Source: "file"}
	for i := 1; i <= n; i++ {
		b.Issues = append(b.Issues, db.ImportIssue{ExternalID: fmt.Sprintf("X-%d", i), Title: fmt.Sprintf("Issue %d", i),
			Labels: []string{"Lỗi"}, Cycle: "Đợt 1"})
	}
	b.Issues[n-1].Parent = "X-1"
	b.Links = []db.ImportLink{{Source: "X-1", Kind: "blocks", Target: fmt.Sprintf("X-%d", n)}}
	return b
}

// velo import --dry-run reports exactly what the real run then does when the
// batch fits one request: the same counts, each new label and cycle once,
// the same identifiers.
func TestQAImportCLIDryRunMatchesRealRun(t *testing.T) {
	_, url, tok := importServer(t)
	c := client.New(url, tok)
	small := bigBatch()
	small.Issues = append(small.Issues[:299], small.Issues[len(small.Issues)-1]) // keeps the parent and the link
	dry, err := sendImport(c, "DEV", small, true)
	if err != nil {
		t.Fatal(err)
	}
	real, err := sendImport(c, "DEV", small, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Summary()[len("Would import"):] != real.Summary()[len("Imported"):] || real.Links != 1 {
		t.Errorf("summary: dry %q, real %q", dry.Summary(), real.Summary())
	}
	if fmt.Sprint(dry.LabelsCreated, dry.CyclesCreated, dry.Issues) != fmt.Sprint(real.LabelsCreated, real.CyclesCreated, real.Issues) {
		t.Errorf("dry and real reports differ")
	}
}

// A batch that needs several requests is checked request by request in a dry
// run: the issue count is right and each new label and cycle is named once;
// links between requests are only counted by the real run.
func TestQAImportCLIDryRunInParts(t *testing.T) {
	_, url, tok := importServer(t)
	c := client.New(url, tok)
	dry, err := sendImport(c, "DEV", bigBatch(), true)
	if err != nil {
		t.Fatal(err)
	}
	real, err := sendImport(c, "DEV", bigBatch(), false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Created != real.Created || real.Links != 1 {
		t.Errorf("dry %q, real %q", dry.Summary(), real.Summary())
	}
	if fmt.Sprint(dry.LabelsCreated, dry.CyclesCreated) != fmt.Sprint(real.LabelsCreated, real.CyclesCreated) {
		t.Errorf("new labels/cycles: dry %q %q, real %q %q", dry.LabelsCreated, dry.CyclesCreated, real.LabelsCreated, real.CyclesCreated)
	}
}

// one request; split into parts it must still go before its child.
func TestQAImportCLIParentWithSpacesAcrossParts(t *testing.T) {
	st, url, tok := importServer(t)
	n := importPartIssues + 10
	b := &db.ImportBatch{Source: "file"}
	for i := 1; i <= n; i++ {
		b.Issues = append(b.Issues, db.ImportIssue{ExternalID: fmt.Sprintf("X-%d", i), Title: "t"})
	}
	b.Issues[0].Parent = fmt.Sprintf(" X-%d ", n) // the parent is last, named with spaces
	if _, err := sendImport(client.New(url, tok), "DEV", b, false); err != nil {
		t.Fatalf("import in parts: %v", err)
	}
	issues, _ := st.ListIssues(db.IssueFilter{ProjectRef: "DEV"})
	if len(issues) != n {
		t.Fatalf("issues: %d", len(issues))
	}
}

// Errors come back as errors (exit code 1 in main), naming the part.
func TestQAImportCLIErrors(t *testing.T) {
	_, url, tok := importServer(t)
	b := bigBatch()
	b.Issues[len(b.Issues)-1].State = "Nope" // only the second part is bad
	_, err := sendImport(client.New(url, tok), "DEV", b, false)
	if err == nil || !strings.Contains(err.Error(), "request 2 of 2") || !strings.Contains(err.Error(), `"Nope"`) {
		t.Fatalf("bad second part: %v", err)
	}
	path := writeBatch(t, &db.ImportBatch{Source: "f", Issues: []db.ImportIssue{{ExternalID: "1", Title: "t"}}})
	for _, args := range [][]string{
		{"import"},
		{"import", "file"},
		{"import", "file", path, path, "--project", "DEV"},
		{"import", "file", path, "--project", "  "},
		{"import", "file", path, "--project", "DEV", "--token", "vt_bad"},
		{"import", "file", path, "--project", "DEV", "--bogus"},
	} {
		if err := Run(append(args, "--url", url)); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}
