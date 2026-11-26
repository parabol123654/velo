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
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/velo-dev/velo/internal/client"
	"github.com/velo-dev/velo/internal/db"
	"github.com/velo-dev/velo/internal/mcp"
	"github.com/velo-dev/velo/internal/server"
	"github.com/velo-dev/velo/web"
)

const Version = "0.1.0"

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
	case "mcp":
		return runMCP(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("velo v%s\n", Version)
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
  velo login --token <velo_...> [--url ...]     Save API token & server URL to ~/.config/velo/config.json
  velo project list                             List projects
  velo project create <KEY> <NAME> [--desc ...] Create a new project
  velo issue list [--project KEY] [--state ...] List issues
  velo issue show <KEY-N>                       Show issue details & comments
  velo issue create --project KEY --title ...   Create a new issue
  velo issue update <KEY-N> [--state ...]       Update issue state, priority, title, or description
  velo issue comment <KEY-N> <BODY>             Add Markdown comment to an issue
  velo search <QUERY>                           Instant FTS5 full-text search
  velo mcp [--url ...] [--token ...]            Start MCP server over stdio for AI coding agents
  velo version                                  Print version
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "HTTP listen address (e.g. :8080 or 127.0.0.1:8080)")
	dataDir := fs.String("data", "./data", "Directory for SQLite database and uploads")
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

	fmt.Printf("⚡ Velo v%s running at %s\n", Version, displayURL)
	fmt.Printf("   Data directory : %s\n", *dataDir)
	fmt.Printf("   MCP command    : velo mcp\n")

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
		q := fs.String("q", "", "Search text")
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
		issues, err := newAPIClient(*u, *tok).ListIssues(*project, *state, *category, *q, nil, *limit)
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
		if len(it.SubIssues) > 0 {
			fmt.Println("\nSub-issues:")
			for _, sub := range it.SubIssues {
				fmt.Printf("  - [%s] %s: %s\n", sub.StateName, sub.Identifier, sub.Title)
			}
		}
		if len(d.Comments) > 0 {
			fmt.Println("\nComments:")
			for _, cm := range d.Comments {
				fmt.Printf("  @%s (%s): %s\n", cm.AuthorName, cm.CreatedAt.Format("2006-01-02 15:04"), cm.Body)
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
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var labels []string
		for _, part := range strings.Split(*label, ",") {
			if s := strings.TrimSpace(part); s != "" {
				labels = append(labels, s)
			}
		}
		iss, err := newAPIClient(*u, *tok).CreateIssue(db.CreateIssueInput{
			ProjectRef:  *project,
			ParentRef:   *parent,
			Title:       *title,
			Description: *desc,
			StateID:     *state,
			Priority:    *prio,
			LabelIDs:    labels,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Created %s: %s [%s]\n", iss.Identifier, iss.Title, iss.StateName)
		return nil

	case "update":
		if len(args) < 2 {
			return fmt.Errorf("usage: velo issue update <KEY-N> [--state ...] [--priority ...] [--title ...] [--desc ...]")
		}
		ref := args[1]
		fs := flag.NewFlagSet("issue update", flag.ContinueOnError)
		state := fs.String("state", "", "New state name")
		title := fs.String("title", "", "New title")
		desc := fs.String("desc", "", "New description")
		prio := fs.Int("priority", -1, "New priority 0..4")
		u := fs.String("url", "", "Velo server URL")
		tok := fs.String("token", "", "API token")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var patch db.UpdateIssueInput
		if *state != "" {
			patch.StateID = state
		}
		if *title != "" {
			patch.Title = title
		}
		if *desc != "" {
			patch.Description = desc
		}
		if *prio >= 0 {
			patch.Priority = prio
		}
		iss, err := newAPIClient(*u, *tok).UpdateIssue(ref, patch)
		if err != nil {
			return err
		}
		fmt.Printf("Updated %s -> State: %s | Priority: %s\n", iss.Identifier, iss.StateName, priorityName(iss.Priority))
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
