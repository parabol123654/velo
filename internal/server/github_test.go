package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

func TestParseRefs(t *testing.T) {
	closes, mentions := parseRefs("Fixes DEV-1, closes: dev-2\n\nsee DEV-3 and DEV-1, utf-8")
	if !reflect.DeepEqual(closes, []string{"DEV-1", "DEV-2"}) {
		t.Fatalf("closes = %v", closes)
	}
	if !reflect.DeepEqual(mentions, []string{"DEV-3"}) {
		t.Fatalf("mentions = %v", mentions)
	}
}

func TestGitHubWebhook(t *testing.T) {
	ts, srv := newTestServer(t)
	const secret = "s3cret"
	srv.GitHubWebhookSecret = secret

	admin, err := srv.store.CreateFirstAdmin("dev@velo.dev", "Dev", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateProject("DEV", "Dev", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"one", "two", "three", "four"} {
		if _, err := srv.store.CreateIssue(admin.ID, db.CreateIssueInput{ProjectRef: "DEV", Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	send := func(event, body, sig string) int {
		t.Helper()
		if sig == "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(body))
			sig = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		req, _ := http.NewRequest("POST", ts.URL+"/api/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	state := func(ref string) string {
		t.Helper()
		iss, err := srv.store.GetIssue(ref)
		if err != nil {
			t.Fatal(err)
		}
		return iss.StateName
	}
	comments := func(ref string) []db.Comment {
		t.Helper()
		iss, _ := srv.store.GetIssue(ref)
		cms, err := srv.store.ListComments(iss.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cms
	}

	push := `{"ref":"refs/heads/main","repository":{"default_branch":"main"},"commits":[
		{"id":"abc1234def","message":"Fixes DEV-1, refs DEV-2\n\nlong body","url":"https://github.com/o/r/commit/abc1234def",
		 "author":{"name":"Dev","email":"dev@velo.dev"}}]}`

	if code := send("push", push, "sha256=00"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: got %d, want 401", code)
	}
	if code := send("push", push, ""); code != http.StatusOK {
		t.Fatalf("push: got %d", code)
	}
	if code := send("push", push, ""); code != http.StatusOK { // redelivery must be idempotent
		t.Fatalf("push redelivery: got %d", code)
	}

	if got := state("DEV-1"); got != "Done" {
		t.Fatalf("DEV-1 state = %q, want Done", got)
	}
	if got := state("DEV-2"); got != "Todo" {
		t.Fatalf("DEV-2 state = %q, want Todo", got)
	}
	c1 := comments("DEV-1")
	if len(c1) != 1 || c1[0].AuthorName != "Dev" || !strings.Contains(c1[0].Body, "Closed by commit [`abc1234`]") {
		t.Fatalf("DEV-1 comments = %+v", c1)
	}
	if c2 := comments("DEV-2"); len(c2) != 1 || !strings.Contains(c2[0].Body, "Referenced in commit") {
		t.Fatalf("DEV-2 comments = %+v", c2)
	}

	// Closing keyword on a feature branch only comments.
	branchPush := `{"ref":"refs/heads/feature","repository":{"default_branch":"main"},"commits":[
		{"id":"fff0000","message":"Fixes DEV-3","url":"https://github.com/o/r/commit/fff0000","author":{"name":"X","email":"x@else.dev"}}]}`
	if code := send("push", branchPush, ""); code != http.StatusOK {
		t.Fatalf("branch push: got %d", code)
	}
	if got := state("DEV-3"); got != "Todo" {
		t.Fatalf("DEV-3 state = %q, want Todo", got)
	}
	if c3 := comments("DEV-3"); len(c3) != 1 || c3[0].AuthorName != "GitHub" {
		t.Fatalf("DEV-3 comments = %+v", c3)
	}

	// PR opened links; PR merged into default branch closes.
	pr := func(action string, merged bool) string {
		m := "false"
		if merged {
			m = "true"
		}
		return `{"action":"` + action + `","repository":{"default_branch":"main"},"pull_request":{"number":7,
			"title":"Add export","body":"Closes DEV-4","html_url":"https://github.com/o/r/pull/7","merged":` + m + `,"base":{"ref":"main"}}}`
	}
	if code := send("pull_request", pr("opened", false), ""); code != http.StatusOK {
		t.Fatalf("pr opened: got %d", code)
	}
	if got := state("DEV-4"); got != "Todo" {
		t.Fatalf("DEV-4 after open = %q, want Todo", got)
	}
	if code := send("pull_request", pr("closed", true), ""); code != http.StatusOK {
		t.Fatalf("pr merged: got %d", code)
	}
	if got := state("DEV-4"); got != "Done" {
		t.Fatalf("DEV-4 after merge = %q, want Done", got)
	}
	if c4 := comments("DEV-4"); len(c4) != 2 {
		t.Fatalf("DEV-4 comments = %+v", c4)
	}

	// A commit by a disabled user is posted as "GitHub", not in their name.
	gone, err := srv.store.CreateUserDirect("gone@velo.dev", "Gone", "password123", "member")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := srv.store.UpdateUser(admin.ID, gone.ID, db.UpdateUserInput{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	pushGone := `{"ref":"refs/heads/feature","repository":{"default_branch":"main"},"commits":[
		{"id":"fff0000aaa","message":"refs DEV-3","url":"https://github.com/o/r/commit/fff0000aaa",
		 "author":{"name":"Gone","email":"gone@velo.dev"}}]}`
	if code := send("push", pushGone, ""); code != http.StatusOK {
		t.Fatalf("push by disabled user: %d", code)
	}
	c3 := comments("DEV-3")
	if last := c3[len(c3)-1]; last.AuthorName != "GitHub" {
		t.Fatalf("comment by disabled user posted as %q, want GitHub", last.AuthorName)
	}

	// Disabled when no secret is configured.
	srv.GitHubWebhookSecret = ""
	if code := send("push", push, ""); code != http.StatusNotFound {
		t.Fatalf("disabled webhook: got %d, want 404", code)
	}
}
