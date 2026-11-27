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

const importUsage = `usage: velo import <source> ... --project KEY [--dry-run]
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

// Limits for one request, just under the server's (db.ImportIssues and the
// 32 MB body limit). A batch within them goes in one request, so its dry run
// reports exactly what the import will do.
const (
	importPartIssues   = 5000
	importPartComments = 20000
	importPartLinks    = 10000
	importPartBytes    = 30 << 20
)

// sendImport posts the batch, split into parts the server accepts when it is
// bigger than one request. Parents go before their children and links follow
// the issues, so every reference is to an issue the server already has.
func sendImport(c *client.Client, project string, b *db.ImportBatch, dryRun bool) (*db.ImportResult, error) {
	// The server refuses a repeated ID within a request; across requests the
	// second copy would look already imported and vanish, so check here.
	seen := map[string]bool{}
	for _, it := range b.Issues {
		id := strings.TrimSpace(it.ExternalID)
		if seen[id] {
			return nil, fmt.Errorf("issue %s appears twice in the import", id)
		}
		seen[id] = true
	}
	parts, err := splitImport(b)
	if err != nil {
		return nil, err
	}
	if dryRun && len(parts) > 1 {
		fmt.Fprintf(os.Stderr, "The import needs %d requests; the dry run checks each alone, so parents and links between them are checked when you import.\n", len(parts))
	}
	all := map[string]bool{}
	for _, it := range b.Issues {
		all[strings.TrimSpace(it.ExternalID)] = true
	}
	total := &db.ImportResult{DryRun: dryRun}
	seenLabel, seenCycle := map[string]bool{}, map[string]bool{}
	for i, part := range parts {
		if dryRun && len(parts) > 1 {
			part = withinPart(part, all)
		}
		res, err := c.ImportIssues(project, part, dryRun)
		if err != nil {
			if len(parts) > 1 && i > 0 && !dryRun {
				return nil, fmt.Errorf("request %d of %d failed (the first %d are imported; run again to continue): %w", i+1, len(parts), i, err)
			}
			return nil, err
		}
		total.Created += res.Created
		total.Skipped += res.Skipped
		total.Comments += res.Comments
		total.Links += res.Links
		for _, l := range res.LabelsCreated {
			if !seenLabel[l] {
				seenLabel[l] = true
				total.LabelsCreated = append(total.LabelsCreated, l)
			}
		}
		for _, c := range res.CyclesCreated {
			if !seenCycle[c] {
				seenCycle[c] = true
				total.CyclesCreated = append(total.CyclesCreated, c)
			}
		}
		total.Unassigned = append(total.Unassigned, res.Unassigned...)
		total.Issues = append(total.Issues, res.Issues...)
	}
	return total, nil
}

// splitImport cuts the batch into requests under the limits: issues (with
// their comments) first, parents before children, then the links.
func splitImport(b *db.ImportBatch) ([]*db.ImportBatch, error) {
	var parts []*db.ImportBatch
	cur := &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
	size, comments := 0, 0
	for _, it := range parentsFirst(b.Issues) {
		raw, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		if len(cur.Issues) > 0 && (len(cur.Issues) >= importPartIssues || size+len(raw) > importPartBytes || comments+len(it.Comments) > importPartComments) {
			parts = append(parts, cur)
			cur = &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
			size, comments = 0, 0
		}
		cur.Issues = append(cur.Issues, it)
		size += len(raw)
		comments += len(it.Comments)
	}
	parts = append(parts, cur)
	// Links ride with the last part while they fit, then fill more requests,
	// which repeat the last part's issues (already imported, so skipped).
	links := b.Links
	last := parts[len(parts)-1]
	for len(links) > 0 {
		n := min(len(links), importPartLinks)
		if last.Links != nil {
			last = &db.ImportBatch{Source: b.Source, Issues: last.Issues[:1]}
			parts = append(parts, last)
		}
		last.Links = links[:n]
		links = links[n:]
	}
	return parts, nil
}

// parentsFirst orders issues so a parent in the batch comes before its
// children, keeping the original order otherwise. IDs match as the server
// matches them, trimmed.
func parentsFirst(issues []db.ImportIssue) []db.ImportIssue {
	byID := map[string]db.ImportIssue{}
	for _, it := range issues {
		byID[strings.TrimSpace(it.ExternalID)] = it
	}
	done := map[string]bool{}
	out := make([]db.ImportIssue, 0, len(issues))
	var visit func(it db.ImportIssue, depth int)
	visit = func(it db.ImportIssue, depth int) {
		id := strings.TrimSpace(it.ExternalID)
		if done[id] {
			return
		}
		done[id] = true
		if p, ok := byID[strings.TrimSpace(it.Parent)]; ok && depth < len(issues) {
			visit(p, depth+1)
		}
		out = append(out, it)
	}
	for _, it := range issues {
		visit(it, 0)
	}
	return out
}

// withinPart drops, for a dry run, parents and links that name issues of the
// batch sent in another request. References to IDs outside the batch stay, so
// the server still checks them against earlier imports.
func withinPart(b *db.ImportBatch, all map[string]bool) *db.ImportBatch {
	in := map[string]bool{}
	for _, it := range b.Issues {
		in[strings.TrimSpace(it.ExternalID)] = true
	}
	elsewhere := func(id string) bool { id = strings.TrimSpace(id); return all[id] && !in[id] }
	out := *b
	out.Issues = make([]db.ImportIssue, len(b.Issues))
	for i, it := range b.Issues {
		if elsewhere(it.Parent) {
			it.Parent = ""
		}
		out.Issues[i] = it
	}
	out.Links = nil
	for _, l := range b.Links {
		if !elsewhere(l.Source) && !elsewhere(l.Target) {
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
