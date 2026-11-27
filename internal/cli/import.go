package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/importer"
)

// velo import reads issues from another tracker on this machine, turns them
// into an import batch and sends it through the HTTP API (#16), so the CLI
// never opens the database. Running it again skips what is already imported.

const importUsage = `usage: velo import <source> ... --project KEY [--dry-run]
  velo import github OWNER/REPO --project KEY  issues, comments, milestones and sub-issues of a GitHub repository
  velo import jira EXPORT.csv --project KEY    a Jira CSV export (all fields)
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

	case "github":
		fs, common := importFlags("import github")
		token := fs.String("github-token", "", "GitHub token; for api.github.com it defaults to $GITHUB_TOKEN, then $GH_TOKEN. Needed for private repositories and big imports")
		api := fs.String("github-api", "https://api.github.com", "GitHub API URL; for GitHub Enterprise, https://HOST/api/v3")
		users := userMap{}
		fs.Var(users, "map-user", "LOGIN=EMAIL: assign LOGIN's issues to the Velo user with EMAIL (repeatable)")
		repo, err := parseWithArg(fs, args[1:], "velo import github OWNER/REPO --project KEY [--map-user LOGIN=EMAIL]... [--dry-run]")
		if err != nil {
			return err
		}
		if err := common.check(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Reading %s from GitHub...\n", repo)
		b, err := (&importer.GitHub{APIURL: *api, Token: githubToken(*api, *token), Users: users}).Batch(repo)
		if err != nil {
			return err
		}
		return common.send(b)

	case "jira":
		fs, common := importFlags("import jira")
		site := fs.String("jira-url", "", "Jira site, e.g. https://SITE.atlassian.net, to link each issue to its original")
		users := userMap{}
		fs.Var(users, "map-user", "NAME=EMAIL: assign NAME's issues to the Velo user with EMAIL (repeatable)")
		states := stateMap{}
		fs.Var(states, "state-map", "STATUS=STATE: put issues in Jira STATUS into the Velo state STATE (repeatable)")
		path, err := parseWithArg(fs, args[1:], "velo import jira EXPORT.csv --project KEY [--map-user NAME=EMAIL]... [--state-map STATUS=STATE]... [--dry-run]")
		if err != nil {
			return err
		}
		if err := common.check(); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		b, warnings, err := (&importer.JiraCSV{BaseURL: *site, Users: users, StateMap: states}).Batch(f)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		printWarnings(warnings)
		return common.send(b)
	}
	return fmt.Errorf("unknown import source %q\n%s", args[0], importUsage)
}

// githubToken is the --github-token flag, else for https://api.github.com
// the environment's GITHUB_TOKEN or GH_TOKEN. Those are github.com tokens:
// another host (GitHub Enterprise, a typo) or plain HTTP gets none of them.
func githubToken(api, flag string) string {
	if flag != "" {
		return flag
	}
	u, err := url.Parse(api)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "api.github.com") {
		return ""
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
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

func (o *importCommon) check() error {
	if strings.TrimSpace(*o.project) == "" {
		return fmt.Errorf("--project is required")
	}
	return nil
}

func (o *importCommon) send(b *db.ImportBatch) error {
	if err := o.check(); err != nil {
		return err
	}
	if len(b.Issues) == 0 {
		fmt.Println("Nothing to import: no issues found.")
		return nil
	}
	res, err := sendImport(newAPIClient(*o.url, *o.token), *o.project, b, *o.dryRun)
	if err != nil {
		return err
	}
	printImportResult(res, *o.project)
	return nil
}

// userMap is a repeatable LOGIN=EMAIL flag; logins match ignoring case and a leading @.
type userMap map[string]string

func (m userMap) String() string { return "" }

func (m userMap) Set(v string) error {
	login, email, ok := strings.Cut(v, "=")
	login = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(login), "@"))
	if !ok || login == "" || !strings.Contains(email, "@") {
		return fmt.Errorf("want LOGIN=EMAIL, got %q", v)
	}
	m[login] = strings.TrimSpace(email)
	return nil
}

// stateMap is a repeatable STATUS=STATE flag; statuses match ignoring case.
type stateMap map[string]string

func (m stateMap) String() string { return "" }

func (m stateMap) Set(v string) error {
	status, state, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(status) == "" || strings.TrimSpace(state) == "" {
		return fmt.Errorf("want STATUS=STATE, got %q", v)
	}
	m[strings.ToLower(strings.TrimSpace(status))] = strings.TrimSpace(state)
	return nil
}

func printWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
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
	seen, seenComment := map[string]bool{}, map[string]bool{}
	for _, it := range b.Issues {
		id := strings.TrimSpace(it.ExternalID)
		if seen[id] {
			return nil, fmt.Errorf("issue %s appears twice in the import", id)
		}
		seen[id] = true
		for _, c := range it.Comments {
			cid := strings.TrimSpace(c.ExternalID)
			if seenComment[cid] {
				return nil, fmt.Errorf("comment %s appears twice in the import", cid)
			}
			seenComment[cid] = true
		}
	}
	parts, linkOnly, err := splitImport(b)
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
		if linkOnly[i] { // its one issue is a copy, sent because a request needs one
			total.Links += res.Links
			continue
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
// their comments) first, parents before children, then the links, in the
// last of those requests while they fit and in requests of their own after
// it. linkOnly marks those: they repeat one issue, as a request needs one.
func splitImport(b *db.ImportBatch) (parts []*db.ImportBatch, linkOnly []bool, err error) {
	cycles, err := json.Marshal(b.Cycles)
	if err != nil {
		return nil, nil, err
	}
	base := len(cycles) + 1024 // the cycles go with every request; room for the rest
	cur := &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
	size, comments := base, 0
	for _, it := range parentsFirst(b.Issues) {
		raw, err := json.Marshal(it)
		if err != nil {
			return nil, nil, err
		}
		if len(cur.Issues) > 0 && (len(cur.Issues) >= importPartIssues || size+len(raw) > importPartBytes || comments+len(it.Comments) > importPartComments) {
			parts, linkOnly = append(parts, cur), append(linkOnly, false)
			cur = &db.ImportBatch{Source: b.Source, Cycles: b.Cycles}
			size, comments = base, 0
		}
		cur.Issues = append(cur.Issues, it)
		size += len(raw)
		comments += len(it.Comments)
	}
	parts, linkOnly = append(parts, cur), append(linkOnly, false)
	for _, l := range b.Links {
		raw, err := json.Marshal(l)
		if err != nil {
			return nil, nil, err
		}
		if len(cur.Links) >= importPartLinks || size+len(raw) > importPartBytes {
			cur = &db.ImportBatch{Source: b.Source, Issues: cur.Issues[:1]}
			one, _ := json.Marshal(cur.Issues[0])
			size = base + len(one)
			parts, linkOnly = append(parts, cur), append(linkOnly, true)
		}
		cur.Links = append(cur.Links, l)
		size += len(raw)
	}
	return parts, linkOnly, nil
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
