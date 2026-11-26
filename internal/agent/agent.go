// Package agent runs a local coding agent on issues that are ready for it:
// it waits for work (SSE events, plus a periodic check), takes the next
// matching issue with next/claim, runs the configured command, and reports
// the outcome as a comment. It runs on a developer's machine, never on the server.
package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/parabol123654/velo/internal/client"
	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/internal/mcp"
)

type Config struct {
	Project string        // optional project key
	Label   string        // only issues with this label ("" = any)
	Cmd     []string      // command and arguments; "{prompt}" and "{issue}" are replaced
	Dir     string        // working directory for the command
	Timeout time.Duration // per run
	Poll    time.Duration // fallback check interval when no events arrive
	Once    bool          // handle at most one issue, then return
	DryRun  bool          // report what would run, change nothing
	URL     string        // passed to the command as VELO_URL
	Token   string        // passed to the command as VELO_TOKEN
}

// Run handles issues one at a time until ctx ends (or after one issue with Once).
func Run(ctx context.Context, api *client.Client, cfg Config, log io.Writer) error {
	if len(cfg.Cmd) == 0 {
		return fmt.Errorf("no command given")
	}
	wake := make(chan struct{}, 1)
	if !cfg.Once && !cfg.DryRun {
		go watchEvents(ctx, api, wake, log)
	}
	for {
		iss, err := api.NextIssue(cfg.Project, cfg.Label)
		if err != nil {
			fmt.Fprintf(log, "velo agent: checking for work failed: %v\n", err)
		}
		if iss != nil {
			if cfg.DryRun {
				argv, stdin := buildArgv(cfg.Cmd, "<prompt for "+iss.Identifier+">", iss.Identifier)
				fmt.Fprintf(log, "velo agent: would claim %s (%s) and run %q (prompt on stdin: %v)\n", iss.Identifier, iss.Title, argv, stdin)
				return nil
			}
			if handle(ctx, api, cfg, iss, log) {
				if cfg.Once || ctx.Err() != nil {
					return nil
				}
				continue // more may be waiting
			}
			// Claim failed (taken meanwhile, or the token may not write): wait
			// for the next change instead of retrying in a tight loop.
		}
		if cfg.Once || cfg.DryRun {
			if iss == nil {
				fmt.Fprintln(log, "velo agent: no issue is waiting")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(cfg.Poll):
		}
	}
}

// handle claims one issue, runs the command on it and comments the outcome.
// It reports whether the issue was claimed.
func handle(ctx context.Context, api *client.Client, cfg Config, next *db.Issue, log io.Writer) bool {
	iss, err := api.ClaimIssue(next.Identifier)
	if err != nil {
		fmt.Fprintf(log, "velo agent: could not claim %s: %v\n", next.Identifier, err)
		return false
	}
	prompt, err := mcp.WorkOnIssuePrompt(api, iss.Identifier)
	if err != nil {
		comment(api, iss.Identifier, fmt.Sprintf("**velo agent** could not build the prompt: %v", err), log)
		return true
	}
	fmt.Fprintf(log, "velo agent: working on %s: %s\n", iss.Identifier, iss.Title)
	res := runCommand(ctx, cfg, iss.Identifier, prompt, log)
	comment(api, iss.Identifier, report(cfg, res), log)
	fmt.Fprintf(log, "velo agent: %s %s\n", iss.Identifier, res.summary())
	return true
}

type result struct {
	exitCode int
	err      error // start failure, timeout or interruption
	timedOut bool
	stopped  bool
	elapsed  time.Duration
	tail     string
}

func (r result) summary() string {
	switch {
	case r.timedOut:
		return fmt.Sprintf("timed out after %s", r.elapsed.Round(time.Second))
	case r.stopped:
		return "stopped (velo agent was interrupted)"
	case r.err != nil:
		return fmt.Sprintf("could not run the command: %v", r.err)
	}
	return fmt.Sprintf("finished with exit code %d after %s", r.exitCode, r.elapsed.Round(time.Second))
}

// buildArgv replaces {prompt} and {issue} in each argument. The prompt goes
// in as data, never through a shell; if no argument takes it, it is sent on stdin.
func buildArgv(cmd []string, prompt, ref string) ([]string, bool) {
	argv := make([]string, len(cmd))
	usesPrompt := false
	for i, a := range cmd {
		if strings.Contains(a, "{prompt}") {
			usesPrompt = true
		}
		argv[i] = strings.ReplaceAll(strings.ReplaceAll(a, "{prompt}", prompt), "{issue}", ref)
	}
	return argv, !usesPrompt
}

func runCommand(ctx context.Context, cfg Config, ref, prompt string, log io.Writer) result {
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	argv, viaStdin := buildArgv(cfg.Cmd, prompt, ref)
	c := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	c.Dir = cfg.Dir
	c.Env = append(os.Environ(), "VELO_URL="+cfg.URL, "VELO_TOKEN="+cfg.Token, "VELO_ISSUE="+ref)
	if viaStdin {
		c.Stdin = strings.NewReader(prompt)
	}
	tail := &tailBuffer{max: 3000}
	out := io.MultiWriter(log, tail)
	c.Stdout, c.Stderr = out, out
	c.WaitDelay = 5 * time.Second // don't hang on children that keep the pipes open

	start := time.Now()
	err := c.Run()
	res := result{elapsed: time.Since(start), tail: tail.String()}
	switch {
	case ctx.Err() != nil:
		res.stopped = true
	case runCtx.Err() == context.DeadlineExceeded:
		res.timedOut = true
	case err != nil:
		if exit, ok := err.(*exec.ExitError); ok {
			res.exitCode = exit.ExitCode()
		} else {
			res.err = err
		}
	}
	return res
}

func report(cfg Config, r result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**velo agent** ran `%s`: %s.", cfg.Cmd[0], r.summary())
	if r.exitCode != 0 || r.timedOut || r.stopped || r.err != nil {
		b.WriteString(" The issue stays assigned so it is not picked up again; reassign it or move it back to retry.")
	}
	if strings.TrimSpace(r.tail) != "" {
		fmt.Fprintf(&b, "\n\nLast output:\n\n```text\n%s\n```", strings.TrimRight(strings.ReplaceAll(r.tail, "```", "'''"), "\n"))
	}
	return b.String()
}

func comment(api *client.Client, ref, body string, log io.Writer) {
	if _, err := api.AddComment(ref, body); err != nil {
		fmt.Fprintf(log, "velo agent: could not comment on %s: %v\n", ref, err)
	}
}

// watchEvents wakes Run whenever the server reports a change, reconnecting
// after errors. Missed events are covered by the periodic check.
func watchEvents(ctx context.Context, api *client.Client, wake chan<- struct{}, log io.Writer) {
	for ctx.Err() == nil {
		body, err := api.Events(ctx)
		if err == nil {
			sc := bufio.NewScanner(body)
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "data:") {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			body.Close()
		} else if ctx.Err() == nil {
			fmt.Fprintf(log, "velo agent: event stream unavailable (%v); retrying\n", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// SplitCommand splits a command line into arguments with simple quoting
// ('single', "double", backslash escapes). No shell features: no pipes,
// variables or globbing.
func SplitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg, quote, escaped := false, rune(0), false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inArg = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unterminated quote or escape in command")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
