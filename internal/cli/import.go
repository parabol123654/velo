package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
)

// velo import reads issues from another tracker on this machine, turns them
// into an import batch and sends it through the HTTP API (#16), so the CLI
// never opens the database. Running it again skips what is already imported.

const importUsage = `usage: velo import <file|github|jira|linear> ... --project KEY [--dry-run]
  velo import file BATCH.json --project KEY    a batch in the API's import format (see SPEC.md)`

func runImport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", importUsage)
	}
	switch args[0] {
	case "file":
		fs, common := importFlags("import file")
		path, err := parseWithArg(fs, args[1:], "velo import file BATCH.json --project KEY [--dry-run]")
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var b db.ImportBatch
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return common.send(&b)
	}
	return fmt.Errorf("unknown import source %q\n%s", args[0], importUsage)
}

// importCommon holds the flags every import source takes.
type importCommon struct {
	project, url, token *string
	dryRun              *bool
}

func importFlags(name string) (*flag.FlagSet, *importCommon) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return fs, &importCommon{
		project: fs.String("project", "", "Project key to import into (required)"),
		dryRun:  fs.Bool("dry-run", false, "Report what would be imported and write nothing"),
		url:     fs.String("url", "", "Velo server URL"),
		token:   fs.String("token", "", "API token"),
	}
}

// parseWithArg parses flags that may come before or after one positional argument.
func parseWithArg(fs *flag.FlagSet, args []string, usage string) (string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return "", fmt.Errorf("usage: %s", usage)
	}
	return pos[0], nil
}

func (o *importCommon) send(b *db.ImportBatch) error {
	if strings.TrimSpace(*o.project) == "" {
		return fmt.Errorf("--project is required")
	}
	res, err := sendImport(newAPIClient(*o.url, *o.token), *o.project, b, *o.dryRun)
	if err != nil {
		return err
	}
	printImportResult(res, *o.project)
	return nil
}

// Limits for one request, under the server's 5000 issues and 32 MB.
const (
	importPartIssues = 1000
	importPartBytes  = 24 << 20
)

// sendImport posts the batch, split into parts the server accepts when it is
// big. Parents go before their children and links go with the last part, so
// every reference is to an issue the server already has. A dry run cannot see
// what earlier parts would create, so it leaves out references across parts.
func sendImport(c *client.Client, project string, b *db.ImportBatch, dryRun bool) (*db.ImportResult, error) {
	parts, err := splitImport(b)
	if err != nil {
		return nil, err
	}
	total := &db.ImportResult{DryRun: dryRun}
	for i, part := range parts {
		if dryRun && len(parts) > 1 {
			part = withinPart(part)
		}
		res, err := c.ImportIssues(project, part, dryRun)
		if err != nil {
			if len(parts) > 1 {
				return nil, fmt.Errorf("part %d of %d (earlier parts are imported; run again to continue): %w", i+1, len(parts), err)
			}
			return nil, err
		}
		total.Created += res.Created
		total.Skipped += res.Skipped
		total.Comments += res.Comments
		total.Links += res.Links
		total.LabelsCreated = append(total.LabelsCreated, res.LabelsCreated...)
		total.CyclesCreated = append(total.CyclesCreated, res.CyclesCreated...)
		total.Unassigned = append(total.Unassigned, res.Unassigned...)
		total.Issues = append(total.Issues, res.Issues...)
	}
	return total, nil
}

func splitImport(b *db.ImportBatch) ([]*db.ImportBatch, error) {
	issues := parentsFirst(b.Issues)
	var parts []*db.ImportBatch
	cur := &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
	size := 0
	for _, it := range issues {
		raw, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		if len(cur.Issues) > 0 && (len(cur.Issues) >= importPartIssues || size+len(raw) > importPartBytes) {
			parts = append(parts, cur)
			cur = &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
			size = 0
		}
		cur.Issues = append(cur.Issues, it)
		size += len(raw)
	}
	cur.Links = b.Links
	return append(parts, cur), nil
}

// parentsFirst orders issues so a parent in the batch comes before its
// children, keeping the original order otherwise.
func parentsFirst(issues []db.ImportIssue) []db.ImportIssue {
	byID := map[string]db.ImportIssue{}
	for _, it := range issues {
		byID[it.ExternalID] = it
	}
	done := map[string]bool{}
	out := make([]db.ImportIssue, 0, len(issues))
	var visit func(it db.ImportIssue, depth int)
	visit = func(it db.ImportIssue, depth int) {
		if done[it.ExternalID] {
			return
		}
		done[it.ExternalID] = true
		if p, ok := byID[it.Parent]; ok && depth < len(issues) {
			visit(p, depth+1)
		}
		out = append(out, it)
	}
	for _, it := range issues {
		visit(it, 0)
	}
	return out
}

// withinPart drops parents and links that name issues outside the part.
func withinPart(b *db.ImportBatch) *db.ImportBatch {
	in := map[string]bool{}
	for _, it := range b.Issues {
		in[it.ExternalID] = true
	}
	out := *b
	out.Issues = make([]db.ImportIssue, len(b.Issues))
	for i, it := range b.Issues {
		if !in[it.Parent] {
			it.Parent = ""
		}
		out.Issues[i] = it
	}
	out.Links = nil
	for _, l := range b.Links {
		if in[l.Source] && in[l.Target] {
			out.Links = append(out.Links, l)
		}
	}
	return &out
}

func printImportResult(r *db.ImportResult, project string) {
	fmt.Printf("%s into %s.\n", r.Summary(), strings.ToUpper(project))
	if len(r.LabelsCreated) > 0 {
		fmt.Printf("New labels: %s\n", strings.Join(r.LabelsCreated, ", "))
	}
	if len(r.CyclesCreated) > 0 {
		fmt.Printf("New cycles: %s\n", strings.Join(r.CyclesCreated, ", "))
	}
	if len(r.Unassigned) > 0 {
		fmt.Printf("Left unassigned, no enabled Velo user matches (%d):\n", len(r.Unassigned))
		for _, u := range r.Unassigned {
			fmt.Printf("  %s\n", u)
		}
	}
}
