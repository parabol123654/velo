// Package importer reads issues from other trackers and turns them into a
// db.ImportBatch, which velo import sends to the server (#16). It runs on the
// client: tokens for the other tracker never reach Velo.
package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// GitHub reads a repository's issues, comments, milestones and sub-issues
// through the GitHub REST API. Pull requests are skipped.
type GitHub struct {
	APIURL string            // https://api.github.com, or https://HOST/api/v3 for GitHub Enterprise
	Token  string            // optional; needed for private repositories and more than 60 requests an hour
	Users  map[string]string // lower-cased login -> email of the Velo user to assign
	HTTP   *http.Client
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// maxPages stops a server that keeps sending "next" links.
const maxPages = 2000

type ghUser struct {
	Login string `json:"login"`
}

type ghIssue struct {
	Number      int                     `json:"number"`
	Title       string                  `json:"title"`
	Body        string                  `json:"body"`
	HTMLURL     string                  `json:"html_url"`
	State       string                  `json:"state"`
	StateReason string                  `json:"state_reason"`
	User        ghUser                  `json:"user"`
	Assignees   []ghUser                `json:"assignees"`
	Labels      []struct{ Name string } `json:"labels"`
	Milestone   *struct{ Title string } `json:"milestone"`
	CreatedAt   time.Time               `json:"created_at"`
	PullRequest *struct{}               `json:"pull_request"`
	RepoURL     string                  `json:"repository_url"`
	SubIssues   struct {
		Total int `json:"total"`
	} `json:"sub_issues_summary"`
}

type ghComment struct {
	ID        int64     `json:"id"`
	IssueURL  string    `json:"issue_url"`
	User      ghUser    `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type ghMilestone struct {
	Title string     `json:"title"`
	State string     `json:"state"`
	DueOn *time.Time `json:"due_on"`
}

// Batch reads repo ("owner/name") into an import batch.
func (g *GitHub) Batch(repo string) (*db.ImportBatch, error) {
	if !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("repository must be OWNER/NAME, got %q", repo)
	}
	repo = strings.ToLower(repo) // GitHub names ignore case; external IDs must not
	if g.APIURL == "" {
		g.APIURL = "https://api.github.com"
	}
	g.APIURL = strings.TrimRight(g.APIURL, "/")
	if g.HTTP == nil {
		g.HTTP = &http.Client{Timeout: time.Minute, CheckRedirect: sameHostRedirect}
	}
	base := g.APIURL + "/repos/" + repo
	b := &db.ImportBatch{Source: "github"}

	milestones, err := pages[ghMilestone](g, base+"/milestones?state=all&per_page=100", repo)
	if err != nil {
		return nil, err
	}
	for _, m := range milestones {
		c := db.ImportCycle{Name: m.Title, Status: "upcoming"}
		if m.State == "closed" {
			c.Status = "completed"
		}
		if m.DueOn != nil {
			c.EndDate = m.DueOn.UTC().Format("2006-01-02")
		}
		b.Cycles = append(b.Cycles, c)
	}

	issues, err := pages[ghIssue](g, base+"/issues?state=all&sort=created&direction=asc&per_page=100", repo)
	if err != nil {
		return nil, err
	}
	ext := func(n int) string { return repo + "#" + strconv.Itoa(n) }
	index := map[int]int{} // issue number -> position in b.Issues
	for _, gi := range issues {
		if gi.PullRequest != nil {
			continue
		}
		it := db.ImportIssue{
			ExternalID:    ext(gi.Number),
			URL:           gi.HTMLURL,
			Title:         gi.Title,
			Description:   gi.Body,
			Author:        "@" + gi.User.Login,
			StateCategory: "unstarted",
			CreatedAt:     gi.CreatedAt,
		}
		if gi.State == "closed" {
			it.StateCategory = "completed"
			if gi.StateReason == "not_planned" || gi.StateReason == "duplicate" {
				it.StateCategory = "canceled"
			}
		}
		for _, l := range gi.Labels {
			it.Labels = append(it.Labels, l.Name)
		}
		if gi.Milestone != nil {
			it.Cycle = gi.Milestone.Title
		}
		if len(gi.Assignees) > 0 {
			login := gi.Assignees[0].Login
			if email, ok := g.Users[strings.ToLower(login)]; ok {
				it.Assignee = email
			} else {
				it.Assignee = "@" + login
			}
		}
		index[gi.Number] = len(b.Issues)
		b.Issues = append(b.Issues, it)
	}

	// One listing for every comment in the repository, instead of one per issue.
	comments, err := pages[ghComment](g, base+"/issues/comments?sort=created&direction=asc&per_page=100", repo)
	if err != nil {
		return nil, err
	}
	for _, c := range comments {
		n, err := strconv.Atoi(c.IssueURL[strings.LastIndex(c.IssueURL, "/")+1:])
		i, ok := index[n]
		if err != nil || !ok { // a pull request's comment
			continue
		}
		b.Issues[i].Comments = append(b.Issues[i].Comments, db.ImportComment{
			ExternalID: strconv.FormatInt(c.ID, 10),
			Author:     "@" + c.User.Login,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
		})
	}

	for _, gi := range issues {
		if gi.PullRequest != nil || gi.SubIssues.Total == 0 {
			continue
		}
		subs, err := pages[ghIssue](g, base+"/issues/"+strconv.Itoa(gi.Number)+"/sub_issues?per_page=100", repo)
		if err != nil {
			return nil, err
		}
		for _, s := range subs {
			i, ok := index[s.Number]
			// Compare with the parent's own repository URL: the typed name may be
			// an old name GitHub redirects.
			parentRepo := gi.RepoURL
			if parentRepo == "" {
				parentRepo = base
			}
			sameRepo := s.RepoURL == "" || strings.EqualFold(strings.TrimRight(s.RepoURL, "/"), strings.TrimRight(parentRepo, "/"))
			if ok && sameRepo { // a sub-issue in another repository is left out
				b.Issues[i].Parent = ext(gi.Number)
			}
		}
	}
	return b, nil
}

// sameHostRedirect refuses a redirect to another scheme or host, so the
// token stays with the API host, as it does for Link pages.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("GitHub redirected too many times")
	}
	if from := via[0].URL; req.URL.Scheme != from.Scheme || req.URL.Host != from.Host {
		return fmt.Errorf("GitHub redirected to another host: %s", req.URL.Redacted())
	}
	return nil
}

// pages fetches every page of a GitHub list, following Link: rel="next".
func pages[T any](g *GitHub, first, repo string) ([]T, error) {
	api, err := url.Parse(g.APIURL)
	if err != nil {
		return nil, fmt.Errorf("GitHub API URL: %w", err)
	}
	var all []T
	next := first
	for n := 0; next != ""; n++ {
		if n == maxPages {
			return nil, fmt.Errorf("GitHub sent more than %d pages for %s", maxPages, first)
		}
		u, err := url.Parse(next)
		if err != nil || u.Scheme != api.Scheme || u.Host != api.Host {
			// Never send the token to another host.
			return nil, fmt.Errorf("GitHub sent a next page outside %s: %q", g.APIURL, next)
		}
		var page []T
		if next, err = g.get(next, repo, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	return all, nil
}

var nextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// get decodes one page into out and returns the next page's URL, if any.
func (g *GitHub) get(u, repo string, out any) (string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "velo-import")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", githubError(resp, raw, repo, g.Token != "")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return "", fmt.Errorf("GitHub sent something that is not a list (%s): %w", u, err)
	}
	if m := nextRe.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		return m[1], nil
	}
	return "", nil
}

func githubError(resp *http.Response, raw []byte, repo string, hasToken bool) error {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	tokenHint := "set GITHUB_TOKEN or --github-token"
	if u, err := url.Parse(resp.Request.URL.String()); err == nil && !strings.EqualFold(u.Host, "api.github.com") {
		tokenHint = "pass --github-token (GITHUB_TOKEN is only used for api.github.com)"
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("GitHub rejected the token (401 %s); check GITHUB_TOKEN or --github-token", body.Message)
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && resp.Header.Get("X-RateLimit-Remaining") == "0":
		reset := "later"
		if sec, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = "at " + time.Unix(sec, 0).Format("15:04")
		}
		if hasToken {
			return fmt.Errorf("GitHub rate limit reached before anything was imported; it resets %s, then run the import again", reset)
		}
		return fmt.Errorf("GitHub rate limit reached; it resets %s. Without a token GitHub allows 60 requests an hour: %s", reset, tokenHint)
	case resp.StatusCode == http.StatusNotFound:
		if hasToken {
			return fmt.Errorf("GitHub has no repository %s, or the token cannot read it", repo)
		}
		return fmt.Errorf("GitHub has no repository %s, or it is private: %s", repo, tokenHint)
	}
	return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, body.Message)
}
