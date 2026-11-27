package importer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves the files in testdata/github like the GitHub REST API,
// including a second page of issues behind a Link header. It records the
// Authorization headers it saw.
func fakeGitHub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var auth []string
	var ts *httptest.Server
	file := func(name string) []byte {
		raw, err := os.ReadFile("testdata/github/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return []byte(strings.ReplaceAll(string(raw), "{{API}}", ts.URL))
	}
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/o/r/milestones":
			w.Write(file("milestones.json"))
		case r.URL.Path == "/repos/o/r/issues" && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<`+ts.URL+`/repositories/42/issues?state=all&page=2>; rel="next", <`+ts.URL+`/repositories/42/issues?state=all&page=2>; rel="last"`)
			w.Write(file("issues-1.json"))
		case r.URL.Path == "/repositories/42/issues" && r.URL.Query().Get("page") == "2":
			w.Write(file("issues-2.json"))
		case r.URL.Path == "/repos/o/r/issues/comments":
			w.Write(file("comments.json"))
		case r.URL.Path == "/repos/o/r/issues/1/sub_issues":
			w.Write(file("sub-issues-1.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &auth
}

// #16: a repository becomes a batch: issues without pull requests, both
// pages, states from state and state_reason, labels, milestones as cycles,
// mapped assignees, comments and sub-issues of the same repository.
func TestGitHubBatch(t *testing.T) {
	ts, auth := fakeGitHub(t)
	g := &GitHub{APIURL: ts.URL, Token: "gh-secret", Users: map[string]string{"bob": "bob@velo.dev"}}
	b, err := g.Batch("O/R") // external IDs use the lower-case name, whatever was typed
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != "github" || len(b.Issues) != 4 {
		t.Fatalf("source %q, %d issues", b.Source, len(b.Issues))
	}
	byID := map[string]int{}
	for i, it := range b.Issues {
		byID[it.ExternalID] = i
	}
	if _, ok := byID["o/r#2"]; ok {
		t.Fatal("the pull request was imported")
	}
	one := b.Issues[byID["o/r#1"]]
	if one.Title != "Login fails on Safari" || one.URL != "https://github.com/o/r/issues/1" || one.Author != "@alice" ||
		one.StateCategory != "completed" || one.Cycle != "v1.0" || one.Assignee != "bob@velo.dev" ||
		strings.Join(one.Labels, ",") != "bug,Lỗi" || !one.CreatedAt.Equal(time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("#1: %+v", one)
	}
	if len(one.Comments) != 1 || one.Comments[0].ExternalID != "101" || one.Comments[0].Author != "@bob" || one.Comments[0].Body != "Reproduced." {
		t.Fatalf("#1 comments: %+v", one.Comments)
	}
	three := b.Issues[byID["o/r#3"]]
	if three.Description != "" || three.StateCategory != "unstarted" || three.Assignee != "@dave" || three.Cycle != "v2.0" ||
		len(three.Comments) != 1 || !three.Comments[0].CreatedAt.Equal(time.Date(2024, 3, 4, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("#3: %+v", three)
	}
	if four := b.Issues[byID["o/r#4"]]; four.StateCategory != "canceled" {
		t.Fatalf("#4 not_planned: %+v", four)
	}
	if five := b.Issues[byID["o/r#5"]]; five.Parent != "o/r#1" {
		t.Fatalf("#5 parent: %+v", five)
	}
	if len(b.Cycles) != 2 || b.Cycles[0].Status != "completed" || b.Cycles[0].EndDate != "2024-06-30" || b.Cycles[1].Status != "upcoming" {
		t.Fatalf("cycles: %+v", b.Cycles)
	}
	for _, a := range *auth {
		if a != "Bearer gh-secret" {
			t.Fatalf("request without the token: %q", a)
		}
	}
}

// GitHub's errors come back as messages that say what to do.
func TestGitHubErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		token   string
		want    string
	}{
		{"not found, no token", 404, nil, "", "or it is private: pass --github-token"}, // not api.github.com
		{"not found, token", 404, nil, "t", "or the token cannot read it"},
		{"bad token", 401, nil, "t", "GitHub rejected the token"},
		{"rate limit", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1700000000"}, "", "60 requests an hour"},
		{"secondary", 500, nil, "", "GitHub API 500: boom"},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range tc.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"message":"boom"}`))
		}))
		_, err := (&GitHub{APIURL: ts.URL, Token: tc.token}).Batch("o/r")
		ts.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
	if _, err := (&GitHub{}).Batch("not a repo"); err == nil || !strings.Contains(err.Error(), "OWNER/NAME") {
		t.Fatalf("bad repo name: %v", err)
	}
}

// A next-page link to another host is refused, so the token never leaves
// the GitHub API host.
func TestGitHubNextPageStaysOnHost(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token sent to another host: %q", r.Header.Get("Authorization"))
		w.Write([]byte(`[]`))
	}))
	defer other.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+other.URL+`/steal?page=2>; rel="next"`)
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	if _, err := (&GitHub{APIURL: ts.URL, Token: "gh-secret"}).Batch("o/r"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("want refusal of the other host, got %v", err)
	}
}
