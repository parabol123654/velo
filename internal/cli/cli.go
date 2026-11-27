package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/parabol123654/velo/internal/agent"
	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/mcp"
	"github.com/parabol123654/velo/internal/server"
	"github.com/parabol123654/velo/internal/version"
	"github.com/parabol123654/velo/web"
)

func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "login":
		return runLogin(args[1:])
	case "project", "projects":
		return runProject(args[1:])
	case "issue", "issues":
		return runIssue(args[1:])
	case "search":
		return runSearch(args[1:])
	case "label", "labels":
		return runLabel(args[1:])
	case "cycle", "cycles":
		return runCycle(args[1:])
	case "user", "users":
		return runUsers(args[1:])
	case "mcp":
		return runMCP(args[1:])
	case "backup":
		return runBackup(args[1:])
	case "uploads":
		return runUploads(args[1:])
	case "agent":
		return runAgent(args[1:])
	case "import":
		return runImport(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("velo %s\n", version.Version)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage() {
	fmt.Print(`Velo — Zero-bloat, local-first issue tracker for developers & AI agents

Usage:
  velo serve [--addr :8080] [--data ./data]     Start Web UI, REST API & SSE server
             [--github-secret ...]              Enable GitHub webhook at /api/webhooks/github
             [--trust-proxy]                    Behind a reverse proxy (HTTPS cookie, client IPs)
  velo login --token <velo_...> [--url ...]     Save API token & server URL to ~/.config/velo/config.json
  velo project list                             List projects
  velo project create <KEY> <NAME> [--desc ...] Create a new project
  velo project show <KEY>                       Show states, labels and cycles
  velo project export <KEY> [--format json]     Print project as Markdown (default) or JSON
  velo project import <FILE.json>               Create a project from a JSON export
  velo issue list [--project KEY] [--state ...] List issues (also --assignee, --cycle, --label, --parent, --priority)
  velo issue list --q 'assignee:me is:open bug' Filter with the query syntax (see velo search)
  velo issue show <KEY-N>                       Show issue details, sub-issues, comments & activity
  velo issue create --project KEY --title ...   Create a new issue (--parent for a sub-issue)
  velo issue update <KEY-N> [--state ...]       Update state, priority, title, desc, assignee, cycle, parent, labels
  velo issue next [--project KEY] [--label L]   Show the next unstarted, unassigned, unblocked issue
  velo issue claim <KEY-N>                      Assign to yourself and move to In Progress
  velo issue check <KEY-N> <ITEM> [--uncheck]   Tick a "- [ ]" item in the description (or --index N)
  velo issue delete <KEY-N>                     Delete an issue
  velo issue link <KEY-N> blocks <KEY-M>        Link issues (blocks, blocked_by, relates, duplicates, duplicated_by)
  velo issue unlink <KEY-N> <KEY-M>             Remove links between two issues
  velo issue comment <KEY-N> <BODY>             Add Markdown comment to an issue
  velo label create --project KEY <NAME>        Create a label [--color #hex]
  velo cycle create --project KEY <NAME>        Create a cycle [--start, --end, --status]
  velo cycle update --project KEY <NAME|ID>     Change only the given --name, --desc, --start, --end, --status
  velo user list                                List users (emails work as --assignee; so does "me")
  velo search <QUERY>                           Search: text plus assignee: state: label: priority: cycle:
                                                project: parent: is:open|closed|blocked|unblocked has:/no:parent
  velo mcp [--url ...] [--token ...]            Start MCP server over stdio for AI coding agents
  velo backup [--data ./data] <FILE.db>         Copy the database safely, even while serving
  velo uploads prune [--data ./data] [--apply]  List (or with --apply, delete) uploads nothing uses
  velo agent --cmd '<command>' [--label agent]  Run a local coding agent on issues labeled for it
  velo import file <BATCH.json> --project KEY   Import issues from another tracker [--dry-run]; reruns skip what exists
  velo version                                  Print version
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "HTTP listen address (e.g. :8080 or 127.0.0.1:8080)")
	dataDir := fs.String("data", "./data", "Directory for SQLite database and uploads")
	ghSecret := fs.String("github-secret", os.Getenv("VELO_GITHUB_WEBHOOK_SECRET"), "Secret for GitHub webhooks at /api/webhooks/github (disabled if empty)")
	envTrust, _ := strconv.ParseBool(os.Getenv("VELO_TRUST_PROXY"))
	trustProxy := fs.Bool("trust-proxy", envTrust, "Behind a reverse proxy: believe X-Forwarded-Proto (Secure cookie) and X-Forwarded-For (login throttling); also VELO_TRUST_PROXY=1 or true")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dbPath := filepath.Join(*dataDir, "velo.db")
	st, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	distFS, err := web.DistFS()
	if err != nil {
		return err
	}
	srv, err := server.New(st, *dataDir, distFS)
	if err != nil {
		return err
	}
	srv.GitHubWebhookSecret = *ghSecret
	srv.TrustProxy = *trustProxy

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	displayURL := *addr
	if strings.HasPrefix(displayURL, ":") {
		displayURL = "http://localhost" + displayURL
	} else if !strings.HasPrefix(displayURL, "http") {
		displayURL = "http://" + displayURL
	}

	fmt.Printf("⚡ Velo %s running at %s\n", version.Version, displayURL)
	fmt.Printf("   Data directory : %s\n", *dataDir)
	fmt.Printf("   MCP command    : velo mcp\n")
	if *ghSecret != "" {
		fmt.Printf("   GitHub webhook : %s/api/webhooks/github\n", displayURL)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-sigCh:
		fmt.Println("\nShutting down Velo...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(ctx)
	}
}

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	u := fs.String("url", "http://127.0.0.1:8080", "Velo server URL")
	tok := fs.String("token", "", "Personal API token (velo_...)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*tok) == "" {
		return fmt.Errorf("--token is required (generate one in Web UI -> Settings -> API & MCP)")
	}
	cfg := client.Config{URL: strings.TrimSpace(*u), Token: strings.TrimSpace(*tok)}
	if err := client.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Saved credentials to %s\n", client.ConfigPath())
	return nil
}

func newAPIClient(flagURL, flagToken string) *client.Client {
	cfg := client.LoadConfig(flagURL, flagToken)
	return client.New(cfg.URL, cfg.Token)
}

func runProject(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fs := flag.NewFlagSet("project list", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		asJSON := fs.Bool("json", false, "Output raw JSON")
		subArgs := args
		if len(args) > 0 && args[0] == "list" {
			subArgs = args[1:]
		}
		if err := fs.Parse(subArgs); err != nil {
			return err
		}
		projects, err := newAPIClient(*u, *tok).ListProjects()
		if err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(projects, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tNAME\tOPEN\tTOTAL")
		for _, p := range projects {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", p.Key, p.Name, p.OpenIssueCount, p.IssueCount)
		}
		return tw.Flush()
	}

	if args[0] == "create" {
		fs := flag.NewFlagSet("project create", flag.ContinueOnError)
		desc := fs.String("desc", "", "Project description")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		// Support positional KEY NAME before or after flags
		var pos []string
		var flagPart []string
		for i := 1; i < len(args); i++ {
			if strings.HasPrefix(args[i], "-") {
				flagPart = append(flagPart, args[i:]...)
				break
			}
			pos = append(pos, args[i])
		}
		if err := fs.Parse(flagPart); err != nil {
			return err
		}
		pos = append(pos, fs.Args()...)
		if len(pos) < 2 {
			return fmt.Errorf("usage: velo project create <KEY> <NAME> [--desc ...]")
		}
		p, err := newAPIClient(*u, *tok).CreateProject(pos[0], pos[1], *desc)
		if err != nil {
			return err
		}
		fmt.Printf("Created project %s (%s)\n", p.Name, p.Key)
		return nil
	}
	switch args[0] {
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo project show <KEY>")
		}
		fs := flag.NewFlagSet("project show", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "Output raw JSON")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		b, err := newAPIClient(*u, *tok).GetProject(args[1])
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(b)
		}
		fmt.Printf("%s — %s\n", b.Project.Key, b.Project.Name)
		if b.Project.Description != "" {
			fmt.Println(b.Project.Description)
		}
		fmt.Println("\nStates:")
		for _, st := range b.States {
			fmt.Printf("  %s (%s)\n", st.Name, st.Category)
		}
		fmt.Println("\nLabels:")
		for _, lb := range b.Labels {
			fmt.Printf("  %s %s\n", lb.Name, lb.Color)
		}
		fmt.Println("\nCycles:")
		for _, cy := range b.Cycles {
			fmt.Printf("  %s [%s] %s → %s\n", cy.Name, cy.Status, valueOrDash(cy.StartDate), valueOrDash(cy.EndDate))
		}
		return nil

	case "export":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo project export <KEY> [--format markdown|json]")
		}
		fs := flag.NewFlagSet("project export", flag.ContinueOnError)
		format := fs.String("format", "markdown", "markdown or json")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		out, err := newAPIClient(*u, *tok).ExportProject(args[1], *format)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(out)
		return err

	case "import":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo project import <FILE.json>")
		}
		fs := flag.NewFlagSet("project import", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		p, err := newAPIClient(*u, *tok).ImportProject(data)
		if err != nil {
			return err
		}
		fmt.Printf("Imported project %s (%s)\n", p.Name, p.Key)
		return nil
	}
	return fmt.Errorf("unknown project subcommand %q", args[0])
}

func priorityName(p int) string {
	switch p {
	case 4:
		return "Urgent"
	case 3:
		return "High"
	case 2:
		return "Medium"
	case 1:
		return "Low"
	default:
		return "None"
	}
}

func runIssue(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
		project := fs.String("project", "", "Filter by project key (e.g. DEV)")
		state := fs.String("state", "", "Filter by state name")
		category := fs.String("category", "", "Filter by category")
		q := fs.String("q", "", "Query: filters such as assignee:me is:open label:bug priority:>=3, plus search text")
		fs.StringVar(q, "query", "", "Same as --q")
		assignee := fs.String("assignee", "", "Assignee email, \"me\" or \"none\"")
		cycle := fs.String("cycle", "", "Cycle name or \"none\"")
		label := fs.String("label", "", "Label name")
		parent := fs.String("parent", "", "Parent identifier (sub-issues) or \"root\"")
		unblocked := fs.Bool("unblocked", false, "Only issues not blocked by unfinished issues")
		prio := fs.Int("priority", -1, "Priority 0..4")
		limit := fs.Int("limit", 50, "Max results")
		asJSON := fs.Bool("json", false, "Output raw JSON")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		subArgs := args
		if len(args) > 0 && args[0] == "list" {
			subArgs = args[1:]
		}
		if err := fs.Parse(subArgs); err != nil {
			return err
		}
		var prioFilter *int
		if *prio >= 0 {
			prioFilter = prio
		}
		issues, err := newAPIClient(*u, *tok).ListIssues(client.IssueQuery{
			Project: *project, State: *state, Category: *category, Query: *q,
			Assignee: *assignee, Cycle: *cycle, Label: *label, Parent: *parent,
			Priority: prioFilter, Limit: *limit, Unblocked: *unblocked,
		})
		if err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(issues, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tPRIORITY\tASSIGNEE\tTITLE")
		for _, it := range issues {
			assignee := it.AssigneeName
			if assignee == "" {
				assignee = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Identifier, it.StateName, priorityName(it.Priority), assignee, it.Title)
		}
		return tw.Flush()
	}

	switch args[0] {
	case "show", "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue show <KEY-N>")
		}
		ref := args[1]
		fs := flag.NewFlagSet("issue show", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "Output raw JSON")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		d, err := newAPIClient(*u, *tok).GetIssueDetail(ref)
		if err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(d, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		it := d.Issue
		fmt.Printf("%s — %s\n", it.Identifier, it.Title)
		fmt.Printf("State: %s | Priority: %s | Assignee: %s\n", it.StateName, priorityName(it.Priority), valueOrDash(it.AssigneeName))
		if it.Description != "" {
			fmt.Printf("\n%s\n", it.Description)
		}
		if it.BlockedByCount > 0 {
			fmt.Printf("Blocked by %d unfinished issue(s)\n", it.BlockedByCount)
		}
		if len(it.Links) > 0 {
			fmt.Println("\nLinks:")
			for _, l := range it.Links {
				fmt.Printf("  %s %s [%s] %s\n", strings.ReplaceAll(l.Kind, "_", " "), l.Issue.Identifier, l.Issue.StateName, l.Issue.Title)
			}
		}
		if len(it.SubIssues) > 0 {
			fmt.Println("\nSub-issues:")
			for _, sub := range it.SubIssues {
				fmt.Printf("  - [%s] %s: %s\n", sub.StateName, sub.Identifier, sub.Title)
			}
		}
		if len(d.Comments) > 0 {
			fmt.Println("\nComments:")
			for _, cm := range d.Comments {
				fmt.Printf("  @%s%s (%s): %s\n", cm.AuthorName, viaSuffix(cm.Via), cm.CreatedAt.Format("2006-01-02 15:04"), cm.Body)
			}
		}
		if len(d.Activities) > 0 {
			fmt.Println("\nActivity:")
			for _, a := range d.Activities {
				line := a.Action
				switch {
				case a.Field == "checklist":
					line = a.NewValue
				case a.Field == "link" && a.NewValue != "":
					line = "linked: " + strings.ReplaceAll(a.NewValue, "_", " ")
				case a.Field == "link" && strings.Contains(a.OldValue, " "):
					line = "unlinked: " + strings.ReplaceAll(a.OldValue, "_", " ")
				case a.Field == "link":
					line = "removed the link to " + a.OldValue
				case a.Field == "description":
					line = "edited the description"
				case a.Field != "":
					line = fmt.Sprintf("%s: %s → %s", a.Field, valueOrDash(a.OldValue), valueOrDash(a.NewValue))
				}
				fmt.Printf("  %s @%s%s %s\n", a.CreatedAt.Format("2006-01-02 15:04"), a.ActorName, viaSuffix(a.Via), line)
			}
		}
		return nil

	case "create":
		fs := flag.NewFlagSet("issue create", flag.ContinueOnError)
		project := fs.String("project", "", "Project key (required)")
		title := fs.String("title", "", "Issue title (required)")
		desc := fs.String("desc", "", "Markdown description")
		state := fs.String("state", "", "Initial workflow state")
		prio := fs.Int("priority", 0, "Priority 0..4")
		parent := fs.String("parent", "", "Parent issue identifier")
		label := fs.String("label", "", "Comma-separated labels")
		assignee := fs.String("assignee", "", "Assignee email or \"me\"")
		cycle := fs.String("cycle", "", "Cycle name")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		labels := splitList(*label)
		iss, err := newAPIClient(*u, *tok).CreateIssue(db.CreateIssueInput{
			ProjectRef:  *project,
			ParentRef:   *parent,
			Title:       *title,
			Description: *desc,
			StateID:     *state,
			Priority:    *prio,
			AssigneeID:  *assignee,
			CycleID:     *cycle,
			LabelIDs:    labels,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Created %s: %s [%s]\n", iss.Identifier, iss.Title, iss.StateName)
		return nil

	case "update":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue update <KEY-N> [--state ...] [--priority ...] [--title ...] [--desc ...] [--assignee ...] [--cycle ...] [--parent ...] [--label ...]")
		}
		ref := args[1]
		fs := flag.NewFlagSet("issue update", flag.ContinueOnError)
		state := fs.String("state", "", "New state name")
		title := fs.String("title", "", "New title")
		desc := fs.String("desc", "", "New description")
		prio := fs.Int("priority", 0, "New priority 0..4")
		assignee := fs.String("assignee", "", "Assignee email or \"me\"; empty to unassign")
		cycle := fs.String("cycle", "", "Cycle name; empty to clear")
		parent := fs.String("parent", "", "Parent identifier; empty to make top-level")
		label := fs.String("label", "", "Comma-separated labels (replaces current); empty to clear")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		// Only flags given on the command line change the issue, so --assignee "" can clear a field.
		var patch db.UpdateIssueInput
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "state":
				patch.StateID = state
			case "title":
				patch.Title = title
			case "desc":
				patch.Description = desc
			case "priority":
				patch.Priority = prio
			case "assignee":
				patch.AssigneeID = assignee
			case "cycle":
				patch.CycleID = cycle
			case "parent":
				patch.ParentID = parent
			case "label":
				labels := splitList(*label)
				patch.LabelIDs = &labels
			}
		})
		iss, err := newAPIClient(*u, *tok).UpdateIssue(ref, patch)
		if err != nil {
			return err
		}
		fmt.Printf("Updated %s -> State: %s | Priority: %s | Assignee: %s\n", iss.Identifier, iss.StateName, priorityName(iss.Priority), valueOrDash(iss.AssigneeName))
		return nil

	case "next":
		fs := flag.NewFlagSet("issue next", flag.ContinueOnError)
		project := fs.String("project", "", "Project key")
		label := fs.String("label", "", "Label name, e.g. agent")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		iss, err := newAPIClient(*u, *tok).NextIssue(*project, *label)
		if err != nil {
			return err
		}
		if iss == nil {
			fmt.Println("No issue is waiting.")
			return nil
		}
		fmt.Printf("%s [%s, %s] %s\n", iss.Identifier, iss.StateName, priorityName(iss.Priority), iss.Title)
		return nil

	case "claim":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue claim <KEY-N>")
		}
		fs := flag.NewFlagSet("issue claim", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		iss, err := newAPIClient(*u, *tok).ClaimIssue(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Claimed %s -> %s, assigned to %s\n", iss.Identifier, iss.StateName, iss.AssigneeName)
		return nil

	case "check":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue check <KEY-N> [ITEM TEXT] [--index N] [--uncheck]")
		}
		fs := flag.NewFlagSet("issue check", flag.ContinueOnError)
		index := fs.Int("index", 0, "1-based item position instead of text")
		uncheck := fs.Bool("uncheck", false, "Untick instead of tick")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		item, err := parseWithName(fs, args[2:])
		if err != nil {
			return err
		}
		iss, err := newAPIClient(*u, *tok).CheckItem(args[1], item, *index, !*uncheck)
		if err != nil {
			return err
		}
		fmt.Println(iss.Description)
		return nil

	case "link":
		if len(args) < 4 {
			return fmt.Errorf("usage: velo issue link <KEY-N> <blocks|blocked_by|relates|duplicates|duplicated_by> <KEY-M>")
		}
		fs := flag.NewFlagSet("issue link", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[4:]); err != nil {
			return err
		}
		if _, err := newAPIClient(*u, *tok).LinkIssues(args[1], args[2], args[3]); err != nil {
			return err
		}
		fmt.Printf("Linked: %s %s %s\n", args[1], args[2], args[3])
		return nil

	case "unlink":
		if len(args) < 3 {
			return fmt.Errorf("usage: velo issue unlink <KEY-N> <KEY-M>")
		}
		fs := flag.NewFlagSet("issue unlink", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if _, err := newAPIClient(*u, *tok).UnlinkIssues(args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("Unlinked %s and %s\n", args[1], args[2])
		return nil

	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue delete <KEY-N>")
		}
		fs := flag.NewFlagSet("issue delete", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if err := newAPIClient(*u, *tok).DeleteIssue(args[1]); err != nil {
			return err
		}
		fmt.Printf("Deleted %s\n", args[1])
		return nil

	case "comment":
		if len(args) < 3 {
			return fmt.Errorf("usage: velo issue comment <KEY-N> <BODY>")
		}
		ref := args[1]
		body := args[2]
		fs := flag.NewFlagSet("issue comment", flag.ContinueOnError)
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		cm, err := newAPIClient(*u, *tok).AddComment(ref, body)
		if err != nil {
			return err
		}
		fmt.Printf("Added comment on %s by %s\n", ref, cm.AuthorName)
		return nil
	}

	return fmt.Errorf("unknown issue subcommand %q", args[0])
}

func runSearch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: velo search <QUERY>")
	}
	query := args[0]
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	hits, err := newAPIClient(*u, *tok).SearchIssues(query, 20)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tPRIORITY\tTITLE")
	for _, it := range hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.Identifier, it.StateName, priorityName(it.Priority), it.Title)
	}
	return tw.Flush()
}

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return mcp.ServeStdio(os.Stdin, os.Stdout, newAPIClient(*u, *tok))
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func splitList(csv string) []string {
	out := []string{}
	for _, part := range strings.Split(csv, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// parseWithName parses flags that may come before or after one positional NAME.
func parseWithName(fs *flag.FlagSet, args []string) (string, error) {
	var name string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if name != "" {
			return "", fmt.Errorf("unexpected argument %q", rest[0])
		}
		name, args = rest[0], rest[1:]
	}
	return name, nil
}

func runLabel(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: velo label create --project KEY <NAME> [--color #hex] (list labels with: velo project show KEY)")
	}
	fs := flag.NewFlagSet("label create", flag.ContinueOnError)
	project := fs.String("project", "", "Project key (required)")
	color := fs.String("color", "", "Hex color such as #ef4444")
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	name, err := parseWithName(fs, args[1:])
	if err != nil {
		return err
	}
	lb, err := newAPIClient(*u, *tok).CreateLabel(*project, name, *color)
	if err != nil {
		return err
	}
	fmt.Printf("Created label %s (%s)\n", lb.Name, lb.Color)
	return nil
}

func runCycle(args []string) error {
	if len(args) > 0 && args[0] == "update" {
		return runCycleUpdate(args[1:])
	}
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: velo cycle create --project KEY <NAME> [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--status active|upcoming|completed]\n       velo cycle update --project KEY <NAME|ID> [--name ...] [--desc ...] [--start ...] [--end ...] [--status ...]\n(list cycles with: velo project show KEY)")
	}
	fs := flag.NewFlagSet("cycle create", flag.ContinueOnError)
	project := fs.String("project", "", "Project key (required)")
	start := fs.String("start", "", "Start date YYYY-MM-DD")
	end := fs.String("end", "", "End date YYYY-MM-DD")
	status := fs.String("status", "", "active (default), upcoming or completed")
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	name, err := parseWithName(fs, args[1:])
	if err != nil {
		return err
	}
	cy, err := newAPIClient(*u, *tok).CreateCycle(*project, name, *start, *end, *status)
	if err != nil {
		return err
	}
	fmt.Printf("Created cycle %s [%s]\n", cy.Name, cy.Status)
	return nil
}

// runCycleUpdate changes only the flags given: `velo cycle update --project
// DEV "Sprint 1" --status completed` keeps the dates and description.
func runCycleUpdate(args []string) error {
	fs := flag.NewFlagSet("cycle update", flag.ContinueOnError)
	project := fs.String("project", "", "Project key (required)")
	name := fs.String("name", "", "New name")
	desc := fs.String("desc", "", "Description (empty clears it)")
	start := fs.String("start", "", "Start date YYYY-MM-DD (empty clears it)")
	end := fs.String("end", "", "End date YYYY-MM-DD (empty clears it)")
	status := fs.String("status", "", "upcoming, active or completed")
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	ref, err := parseWithName(fs, args)
	if err != nil {
		return err
	}
	if *project == "" || ref == "" {
		return fmt.Errorf("usage: velo cycle update --project KEY <NAME|ID> [--name ...] [--desc ...] [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--status upcoming|active|completed]")
	}
	var patch db.UpdateCycleInput
	fields := map[string]**string{"name": &patch.Name, "desc": &patch.Description, "start": &patch.StartDate, "end": &patch.EndDate, "status": &patch.Status}
	values := map[string]*string{"name": name, "desc": desc, "start": start, "end": end, "status": status}
	fs.Visit(func(f *flag.Flag) {
		if field, ok := fields[f.Name]; ok {
			*field = values[f.Name]
		}
	})
	if patch == (db.UpdateCycleInput{}) {
		return fmt.Errorf("nothing to change: pass --name, --desc, --start, --end or --status")
	}
	api := newAPIClient(*u, *tok)
	cy, err := api.FindCycle(*project, ref)
	if err != nil {
		return err
	}
	cy, err = api.UpdateCycle(cy.ID, patch)
	if err != nil {
		return err
	}
	fmt.Printf("Updated cycle %s [%s] %s → %s\n", cy.Name, cy.Status, valueOrDash(cy.StartDate), valueOrDash(cy.EndDate))
	return nil
}

func runUsers(args []string) error {
	if len(args) > 0 && args[0] == "list" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("user list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "Output raw JSON")
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	users, err := newAPIClient(*u, *tok).ListUsers()
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(users)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tNAME\tROLE\tSTATUS")
	for _, usr := range users {
		status := "active"
		if usr.DisabledAt != nil {
			status = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", usr.Email, usr.Name, usr.Role, status)
	}
	return tw.Flush()
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "Data directory of the Velo server")
	out, err := parseWithName(fs, args)
	if err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("usage: velo backup [--data ./data] <FILE.db>")
	}
	dbPath := filepath.Join(*dataDir, "velo.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no database at %s: %w", dbPath, err)
	}
	st, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.BackupTo(out); err != nil {
		return err
	}
	fmt.Printf("Backed up %s to %s (uploads are files in %s; copy that folder too)\n",
		dbPath, out, filepath.Join(*dataDir, "uploads"))
	return nil
}

// runUploads works on the data directory directly, like backup: it is
// server-side maintenance, so it needs no login (SPEC.md, Invariant 3).
func runUploads(args []string) error {
	if len(args) == 0 || args[0] != "prune" {
		return fmt.Errorf("usage: velo uploads prune [--data ./data] [--apply]")
	}
	fs := flag.NewFlagSet("uploads prune", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "Data directory of the Velo server")
	apply := fs.Bool("apply", false, "Delete what is listed (default: only list it)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; usage: velo uploads prune [--data ./data] [--apply]", fs.Arg(0))
	}
	dbPath := filepath.Join(*dataDir, "velo.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no database at %s: %w", dbPath, err)
	}
	st, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := st.PruneUploads(filepath.Join(*dataDir, "uploads"), *apply)
	if rep != nil {
		for _, f := range rep.Files {
			fmt.Printf("%-8s %10d  %s\n", f.Reason, f.Bytes, f.Name)
		}
	}
	if err != nil {
		if *apply && rep != nil {
			fmt.Printf("Deleted %d files (%d bytes) before the error.\n", len(rep.Files), rep.Bytes)
		}
		return err
	}
	switch {
	case len(rep.Files) == 0:
		fmt.Println("Nothing to prune.")
	case *apply:
		fmt.Printf("Deleted %d files (%d bytes).\n", len(rep.Files), rep.Bytes)
	default:
		fmt.Printf("%d files (%d bytes) would be deleted. Run again with --apply to delete them.\n", len(rep.Files), rep.Bytes)
	}
	return nil
}

// viaSuffix marks changes made through an API token, e.g. " (via Claude Code)".
func viaSuffix(via string) string {
	if via == "" {
		return ""
	}
	return " (via " + via + ")"
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	cmdLine := fs.String("cmd", "", `Command to run per issue, e.g. 'claude -p {prompt}'. {prompt} and {issue} are replaced; without {prompt} the prompt goes to stdin. Runs without a shell.`)
	label := fs.String("label", "agent", `Only issues with this label ("" for any)`)
	project := fs.String("project", "", "Only this project")
	dir := fs.String("dir", ".", "Working directory for the command")
	timeout := fs.Duration("timeout", time.Hour, "Stop a run after this long")
	poll := fs.Duration("poll", time.Minute, "Also check for work this often")
	once := fs.Bool("once", false, "Handle at most one issue, then exit")
	dryRun := fs.Bool("dry-run", false, "Show the issue and command that would run; change nothing")
	u := fs.String("url", "", "Velo server URL")
	tok := fs.String("token", "", "API token (give the agent its own, limited to its projects)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv, err := agent.SplitCommand(*cmdLine)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return fmt.Errorf("usage: velo agent --cmd '<command>' [--label agent] [--project KEY] [--once] [--dry-run]")
	}
	cfg := client.LoadConfig(*u, *tok)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	who := "any label"
	if *label != "" {
		who = "label " + *label
	}
	fmt.Fprintf(os.Stderr, "velo agent: watching %s for issues with %s (Ctrl+C to stop)\n", cfg.URL, who)
	return agent.Run(ctx, client.New(cfg.URL, cfg.Token), agent.Config{
		Project: *project, Label: *label, Cmd: argv, Dir: *dir,
		Timeout: *timeout, Poll: *poll, Once: *once, DryRun: *dryRun,
		URL: cfg.URL, Token: cfg.Token,
	}, os.Stderr)
}
