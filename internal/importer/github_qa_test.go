package importer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// qaGitHub is a fake GitHub API whose routes a test fills in. A route that is
// not set answers [] for the three lists and 404 otherwise. It records every
// request's path and Authorization header.
type qaGitHub struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc // path -> handler
	seen   []string                    // "path auth"
}

func newQAGitHub(t *testing.T) *qaGitHub {
	t.Helper()
	g := &qaGitHub{routes: map[string]http.HandlerFunc{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.seen = append(g.seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		h := g.routes[r.URL.Path]
		g.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/milestones", "/repos/o/r/issues", "/repos/o/r/issues/comments":
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *qaGitHub) set(path, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[path] = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }
}

func (g *qaGitHub) paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.seen))
	for i, s := range g.seen {
		out[i], _, _ = strings.Cut(s, " ")
	}
	return out
}

func qaIssue(n int, extra string) string {
	return fmt.Sprintf(`{"number":%d,"title":"Issue %d","html_url":"https://github.com/o/r/issues/%d","state":"open","user":{"login":"u"}%s}`, n, n, n, extra)
}

// An empty repository is an empty batch, not an error, and asks no more than
// the three lists.
func TestQAGitHubEmptyRepository(t *testing.T) {
	g := newQAGitHub(t)
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil || b == nil || len(b.Issues) != 0 || len(b.Links) != 0 {
		t.Fatalf("empty repo: %+v %v", b, err)
	}
	if p := g.paths(); len(p) != 3 {
		t.Fatalf("requests: %v", p)
	}
}

// A repository with only pull requests, comments on them and a pull request
// claiming sub-issues imports nothing and asks for no sub-issues.
func TestQAGitHubOnlyPullRequests(t *testing.T) {
	g := newQAGitHub(t)
	g.set("/repos/o/r/issues", `[`+qaIssue(1, `,"pull_request":{"url":"x"},"sub_issues_summary":{"total":3}`)+`,`+qaIssue(2, `,"pull_request":{}`)+`]`)
	g.set("/repos/o/r/issues/comments", `[{"id":1,"issue_url":"`+g.URL+`/repos/o/r/issues/1","user":{"login":"a"},"body":"PR comment"}]`)
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil || len(b.Issues) != 0 {
		t.Fatalf("pull requests only: %+v %v", b, err)
	}
	for _, p := range g.paths() {
		if strings.Contains(p, "sub_issues") {
			t.Fatalf("asked a pull request for sub-issues: %s", p)
		}
	}
}

// Pull requests between issues, and their comments, are left out while the
// issues around them keep theirs.
func TestQAGitHubPullRequestCommentsMixedIn(t *testing.T) {
	g := newQAGitHub(t)
	g.set("/repos/o/r/issues", `[`+qaIssue(1, "")+`,`+qaIssue(2, `,"pull_request":{"url":"x"}`)+`,`+qaIssue(3, "")+`]`)
	g.set("/repos/o/r/issues/comments", `[
		{"id":10,"issue_url":"`+g.URL+`/repos/o/r/issues/2","user":{"login":"a"},"body":"on the PR"},
		{"id":11,"issue_url":"`+g.URL+`/repos/o/r/issues/3","user":{"login":"a"},"body":"on #3"},
		{"id":12,"issue_url":"","user":{"login":"a"},"body":"no url"},
		{"id":13,"issue_url":"garbage","user":{"login":"a"},"body":"bad url"},
		{"id":14,"issue_url":"`+g.URL+`/repos/o/r/issues/99","user":{"login":"a"},"body":"unknown issue"}]`)
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil || len(b.Issues) != 2 {
		t.Fatalf("batch: %+v %v", b, err)
	}
	if len(b.Issues[0].Comments) != 0 || len(b.Issues[1].Comments) != 1 || b.Issues[1].Comments[0].ExternalID != "11" {
		t.Fatalf("comments: %+v / %+v", b.Issues[0].Comments, b.Issues[1].Comments)
	}
}

// Link headers in the orders GitHub and proxies send them: next after prev,
// next with extra spaces, an empty page in the middle, and a last page that
// has only prev and first.
func TestQAGitHubPaginationEdgeCases(t *testing.T) {
	g := newQAGitHub(t)
	link := func(rest string) string { return g.URL + rest }
	g.routes["/repos/o/r/issues"] = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+link("/repos/o/r/issues?page=2")+`>;  rel="next", <`+link("/repos/o/r/issues?page=4")+`>; rel="last"`)
			w.Write([]byte(`[` + qaIssue(1, "") + `]`))
		case "2":
			w.Header().Set("Link", `<`+link("/repos/o/r/issues?page=1")+`>; rel="prev", <`+link("/repos/o/r/issues?page=3")+`>; rel="next"`)
			w.Write([]byte(`[]`))
		case "3":
			w.Header().Set("Link", `<`+link("/repos/o/r/issues?page=2")+`>; rel="prev", <`+link("/repos/o/r/issues?page=4")+`>; rel="next", <`+link("/repos/o/r/issues?page=1")+`>; rel="first"`)
			w.Write([]byte(`[` + qaIssue(2, "") + `]`))
		case "4":
			w.Header().Set("Link", `<`+link("/repos/o/r/issues?page=3")+`>; rel="prev", <`+link("/repos/o/r/issues?page=1")+`>; rel="first"`)
			w.Write([]byte(`[` + qaIssue(3, `,"sub_issues_summary":{"total":2}`) + `]`))
		default:
			t.Errorf("asked for page %q", r.URL.Query().Get("page"))
		}
	}
	// Sub-issues on two pages too.
	g.routes["/repos/o/r/issues/3/sub_issues"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+link("/repos/o/r/issues/3/sub_issues?page=2")+`>; rel="next"`)
			w.Write([]byte(`[{"number":1,"repository_url":"` + g.URL + `/repos/o/r"}]`))
			return
		}
		w.Write([]byte(`[{"number":2,"repository_url":"` + g.URL + `/repos/o/r"}]`))
	}
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil || len(b.Issues) != 3 {
		t.Fatalf("batch: %+v %v", b, err)
	}
	if b.Issues[0].Parent != "o/r#3" || b.Issues[1].Parent != "o/r#3" || b.Issues[2].Parent != "" {
		t.Fatalf("parents over two sub-issue pages: %q %q %q", b.Issues[0].Parent, b.Issues[1].Parent, b.Issues[2].Parent)
	}
}

// A sub-issue in another repository with the same number as an issue here
// must not become that issue's child; the repository URL matches ignoring
// case.
func TestQAGitHubSubIssueInAnotherRepository(t *testing.T) {
	g := newQAGitHub(t)
	g.set("/repos/o/r/issues", `[`+qaIssue(1, `,"sub_issues_summary":{"total":3}`)+`,`+qaIssue(2, "")+`,`+qaIssue(3, "")+`]`)
	g.set("/repos/o/r/issues/1/sub_issues", `[
		{"number":2,"repository_url":"`+g.URL+`/repos/x/y"},
		{"number":3,"repository_url":"`+g.URL+`/repos/O/R/"},
		{"number":77,"repository_url":"`+g.URL+`/repos/o/r"}]`)
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if b.Issues[1].Parent != "" {
		t.Fatalf("x/y#2 made o/r#2 a child: %+v", b.Issues[1])
	}
	if b.Issues[2].Parent != "o/r#1" {
		t.Fatalf("o/r#3 (repository URL in other case) lost its parent: %+v", b.Issues[2])
	}
}

// Every error status gives an error, not a partial batch, and the token is
// never in the message.
func TestQAGitHubErrorStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
		headers    map[string]string
		body, want string
	}{
		{"401 on the first list", "/repos/o/r/milestones", 401, nil, `{"message":"Bad credentials"}`, "GitHub rejected the token (401 Bad credentials)"},
		{"429 rate limit with token", "/repos/o/r/issues", 429, map[string]string{"X-RateLimit-Remaining": "0"}, `{}`, "resets later"},
		{"403 secondary limit", "/repos/o/r/issues", 403, map[string]string{"Retry-After": "60"}, `{"message":"You have exceeded a secondary rate limit"}`, "GitHub API 403: You have exceeded a secondary rate limit"},
		{"500 with HTML", "/repos/o/r/issues/comments", 500, nil, `<html>oops</html>`, "GitHub API 500"},
		{"502 on sub-issues", "/repos/o/r/issues/1/sub_issues", 502, nil, `{"message":"Bad Gateway"}`, "GitHub API 502"},
		{"410 issues disabled", "/repos/o/r/issues", 410, nil, `{"message":"Issues are disabled for this repo"}`, "Issues are disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newQAGitHub(t)
			g.set("/repos/o/r/issues", `[`+qaIssue(1, `,"sub_issues_summary":{"total":1}`)+`]`)
			g.routes[tc.path] = func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}
			b, err := (&GitHub{APIURL: g.URL, Token: "gh-secret-123"}).Batch("o/r")
			if err == nil || b != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v (batch %v)", tc.want, err, b)
			}
			if strings.Contains(err.Error(), "gh-secret-123") {
				t.Fatalf("token in the error: %v", err)
			}
		})
	}
}

// Malformed or unexpected JSON is an error, never a panic or a silent
// partial import.
func TestQAGitHubMalformedJSON(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":      `[{"number":1,"title":"x"`,
		"object":         `{"message":"hello"}`,
		"html":           `<!doctype html><p>login</p>`,
		"wrong types":    `[{"number":"one","title":2}]`,
		"empty body":     ``,
		"bad created_at": `[{"number":1,"title":"x","created_at":"yesterday"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			g := newQAGitHub(t)
			g.set("/repos/o/r/issues", body)
			if b, err := (&GitHub{APIURL: g.URL}).Batch("o/r"); err == nil {
				t.Fatalf("accepted %q: %+v", body, b)
			}
		})
	}
	// null where GitHub may send null is fine.
	g := newQAGitHub(t)
	g.set("/repos/o/r/issues", `[{"number":1,"title":"x","body":null,"user":null,"assignees":null,"labels":null,"milestone":null,"created_at":null,"state_reason":null}]`)
	g.set("/repos/o/r/issues/comments", `null`)
	b, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err != nil || len(b.Issues) != 1 || !b.Issues[0].CreatedAt.IsZero() {
		t.Fatalf("nulls: %+v %v", b, err)
	}
}

// A next link anywhere but the API host is refused before any request, so
// the token cannot leave: another port, another scheme, userinfo tricks, and
// a relative link.
func TestQAGitHubLinkHeadersToOtherHosts(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		w.Write([]byte(`[]`))
	}))
	defer other.Close()
	g := newQAGitHub(t)
	api, _ := url.Parse(g.URL)
	ou, _ := url.Parse(other.URL)
	for _, next := range []string{
		other.URL + "/repos/o/r/issues?page=2",
		"http://" + api.Host + "@" + ou.Host + "/steal",
		"https://" + api.Host + "/repos/o/r/issues?page=2",
		"//" + ou.Host + "/steal",
		"http://localhost:" + api.Port() + "/repos/o/r/issues?page=2",
	} {
		g.routes["/repos/o/r/issues"] = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", `<`+next+`>; rel="next"`)
			w.Write([]byte(`[` + qaIssue(1, "") + `]`))
		}
		if _, err := (&GitHub{APIURL: g.URL, Token: "gh-secret"}).Batch("o/r"); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("next %q: want refusal, got %v", next, err)
		}
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("%d requests reached the other host", n)
	}
}

// A redirect from the API host to another host does not carry the token.
func TestQAGitHubRedirectDropsToken(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("unset")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`[]`))
	}))
	defer other.Close()
	ou, _ := url.Parse(other.URL)
	g := newQAGitHub(t)
	g.routes["/repos/o/r/milestones"] = func(w http.ResponseWriter, r *http.Request) {
		// localhost and 127.0.0.1 are different hosts to the HTTP client.
		http.Redirect(w, r, "http://localhost:"+ou.Port()+"/elsewhere", http.StatusFound)
	}
	// A redirect to another host is refused outright, as a Link page there is.
	if _, err := (&GitHub{APIURL: g.URL, Token: "gh-secret"}).Batch("o/r"); err == nil || !strings.Contains(err.Error(), "redirected to another host") {
		t.Fatalf("want the redirect refused, got %v", err)
	}
	if a := gotAuth.Load().(string); a != "unset" {
		t.Fatalf("redirect target got a request, Authorization %q", a)
	}
}

// A server that never stops sending next links is cut off at maxPages.
func TestQAGitHubEndlessPages(t *testing.T) {
	var n atomic.Int32
	g := newQAGitHub(t)
	g.routes["/repos/o/r/milestones"] = func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/milestones?page=%d>; rel="next"`, g.URL, n.Load()+1))
		w.Write([]byte(`[]`))
	}
	_, err := (&GitHub{APIURL: g.URL}).Batch("o/r")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d pages", maxPages)) {
		t.Fatalf("endless pages: %v", err)
	}
	if got := n.Load(); got != maxPages {
		t.Fatalf("made %d requests, want %d", got, maxPages)
	}
}

// Without a token no Authorization header is sent at all; repository names
// that are not OWNER/NAME are refused before any request.
func TestQAGitHubNoTokenAndBadNames(t *testing.T) {
	g := newQAGitHub(t)
	if _, err := (&GitHub{APIURL: g.URL}).Batch("o/r"); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	for _, s := range g.seen {
		if !strings.HasSuffix(s, " ") {
			t.Fatalf("Authorization without a token: %q", s)
		}
	}
	g.seen = nil
	g.mu.Unlock()
	for _, repo := range []string{"", "o", "o/", "/r", "o/r/x", "o r/x", "o/r?x=1", "o/r#1", "https://github.com/o/r", "ô/r"} {
		if _, err := (&GitHub{APIURL: g.URL}).Batch(repo); err == nil || !strings.Contains(err.Error(), "OWNER/NAME") {
			t.Fatalf("%q: %v", repo, err)
		}
	}
	if p := g.paths(); len(p) != 0 {
		t.Fatalf("bad names made requests: %v", p)
	}
}

// Milestones: closed is completed, open is upcoming, due_on in another zone
// is a UTC date, and a milestone no issue uses is still offered as a cycle.
// The first assignee wins and maps ignoring case; labels keep their order.
func TestQAGitHubFieldMapping(t *testing.T) {
	g := newQAGitHub(t)
	g.set("/repos/o/r/milestones", `[{"title":"Sprint Ồ","state":"open","due_on":"2024-07-01T02:00:00+07:00"}]`)
	g.set("/repos/o/r/issues", `[
		{"number":1,"title":"a","state":"closed","state_reason":"duplicate","user":{"login":"x"},"assignees":[{"login":"Zed"},{"login":"bob"}],"labels":[{"name":"b"},{"name":"a"}],"milestone":{"title":"Sprint Ồ"}},
		{"number":2,"title":"b","state":"closed","state_reason":null,"user":{"login":"x"}},
		{"number":3,"title":"c","state":"open","state_reason":"reopened","user":{"login":"x"}}]`)
	b, err := (&GitHub{APIURL: g.URL, Users: map[string]string{"zed": "z@velo.dev", "bob": "b@velo.dev"}}).Batch("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Cycles) != 1 || b.Cycles[0].Status != "upcoming" || b.Cycles[0].EndDate != "2024-06-30" {
		t.Fatalf("cycles: %+v", b.Cycles)
	}
	one, two, three := b.Issues[0], b.Issues[1], b.Issues[2]
	if one.StateCategory != "canceled" || one.Assignee != "z@velo.dev" || strings.Join(one.Labels, ",") != "b,a" || one.Cycle != "Sprint Ồ" || one.Priority != 0 {
		t.Fatalf("#1: %+v", one)
	}
	if two.StateCategory != "completed" || three.StateCategory != "unstarted" {
		t.Fatalf("#2 %q, #3 %q", two.StateCategory, three.StateCategory)
	}
}
