package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/parabol123654/velo/internal/db"
)

var (
	// "Fixes DEV-1", "closes: dev-2", "Resolved DEV-3"
	closeRefRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+([a-z][a-z0-9]{1,9}-\d+)\b`)
	// Any plain "DEV-1" mention (uppercase only, to limit false positives).
	issueRefRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9}-\d+)\b`)
)

// parseRefs splits issue references into closing refs and plain mentions.
func parseRefs(text string) (closes, mentions []string) {
	seen := map[string]bool{}
	for _, m := range closeRefRe.FindAllStringSubmatch(text, -1) {
		ref := strings.ToUpper(m[1])
		if !seen[ref] {
			seen[ref] = true
			closes = append(closes, ref)
		}
	}
	for _, m := range issueRefRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			mentions = append(mentions, m[1])
		}
	}
	return closes, mentions
}

func validGitHubSignature(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type ghRepo struct {
	DefaultBranch string `json:"default_branch"`
}

type ghPush struct {
	Ref        string `json:"ref"`
	Repository ghRepo `json:"repository"`
	Commits    []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		URL     string `json:"url"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"author"`
	} `json:"commits"`
}

type ghPullRequest struct {
	Action      string `json:"action"`
	Repository  ghRepo `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		Base    struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.GitHubWebhookSecret == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "github webhook not enabled"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !validGitHubSignature(s.GitHubWebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	switch r.Header.Get("X-GitHub-Event") {
	case "push":
		var p ghPush
		if err = json.Unmarshal(body, &p); err == nil {
			err = s.handleGitHubPush(p)
		}
	case "pull_request":
		var p ghPullRequest
		if err = json.Unmarshal(body, &p); err == nil {
			err = s.handleGitHubPullRequest(p)
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGitHubPush(p ghPush) error {
	onDefault := p.Repository.DefaultBranch != "" && p.Ref == "refs/heads/"+p.Repository.DefaultBranch
	for _, c := range p.Commits {
		closes, mentions := parseRefs(c.Message)
		if !onDefault {
			// Like GitHub: closing keywords only take effect on the default branch.
			mentions = append(closes, mentions...)
			closes = nil
		}
		if len(closes)+len(mentions) == 0 {
			continue
		}
		actor, err := s.store.UserIDForGitEmail(c.Author.Email)
		if err != nil {
			return err
		}
		short := c.ID
		if len(short) > 7 {
			short = short[:7]
		}
		summary, _, _ := strings.Cut(c.Message, "\n")
		link := fmt.Sprintf("[`%s`](%s)", short, c.URL)
		for _, ref := range closes {
			body := fmt.Sprintf("Closed by commit %s (%s): %s", link, c.Author.Name, summary)
			if err := s.applyGitRef(actor, ref, true, c.URL, body); err != nil {
				return err
			}
		}
		for _, ref := range mentions {
			body := fmt.Sprintf("Referenced in commit %s (%s): %s", link, c.Author.Name, summary)
			if err := s.applyGitRef(actor, ref, false, c.URL, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) handleGitHubPullRequest(p ghPullRequest) error {
	pr := p.PullRequest
	closes, mentions := parseRefs(pr.Title + "\n" + pr.Body)
	if len(closes)+len(mentions) == 0 {
		return nil
	}
	actor, err := s.store.UserIDForGitEmail("")
	if err != nil {
		return err
	}
	title := fmt.Sprintf("[#%d %s](%s)", pr.Number, pr.Title, pr.HTMLURL)

	switch {
	case p.Action == "opened" || p.Action == "reopened" || p.Action == "ready_for_review":
		key := "Linked to pull request " + pr.HTMLURL
		for _, ref := range append(closes, mentions...) {
			if err := s.applyGitRef(actor, ref, false, key, key+" "+title); err != nil {
				return err
			}
		}
	case p.Action == "closed" && pr.Merged && pr.Base.Ref == p.Repository.DefaultBranch:
		key := "Closed by merged pull request " + pr.HTMLURL
		for _, ref := range closes {
			if err := s.applyGitRef(actor, ref, true, key, key+" "+title); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyGitRef comments on an issue (once per dedupeKey) and optionally moves it
// to the project's first completed state. Unknown refs are ignored.
func (s *Server) applyGitRef(actorID, ref string, closeIt bool, dedupeKey, body string) error {
	iss, err := s.store.GetIssue(ref)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	dup, err := s.store.HasCommentContaining(iss.ID, dedupeKey)
	if err != nil {
		return err
	}
	if !dup {
		cm, err := s.store.AddComment(actorID, iss.ID, body)
		if err != nil {
			return err
		}
		s.hub.Broadcast("comment.created", iss.ProjectID, cm)
	}

	if !closeIt || iss.StateCategory == "completed" || iss.StateCategory == "canceled" {
		return nil
	}
	stateID, err := s.store.CompletedStateID(iss.ProjectID)
	if err != nil {
		return err
	}
	updated, err := s.store.UpdateIssue(actorID, iss.ID, db.UpdateIssueInput{StateID: &stateID})
	if err != nil {
		return err
	}
	s.hub.Broadcast("issue.updated", updated.ProjectID, updated)
	return nil
}
