package cli

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// #16: velo import github reads the repository from the GitHub API and
// imports it; a rerun adds nothing, and the GitHub token is sent to GitHub
// only, never to Velo.
func TestImportGitHubCommand(t *testing.T) {
	st, velo, tok := importServer(t)
	if _, err := st.CreateUserDirect("bob@velo.dev", "Bob", "secret123", "member"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ghAuth []string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ghAuth = append(ghAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/o/r/issues":
			w.Write([]byte(`[{"number":7,"title":"Crash","body":"stack","html_url":"https://github.com/o/r/issues/7","state":"open",
				"user":{"login":"alice"},"assignees":[{"login":"BOB"}],"labels":[{"name":"bug"}],"created_at":"2024-01-01T00:00:00Z"}]`))
		case "/repos/o/r/milestones", "/repos/o/r/issues/comments":
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer gh.Close()
	// A proxy in front of Velo that records what Velo receives.
	var veloSeen []string
	target, _ := url.Parse(velo)
	rp := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		veloSeen = append(veloSeen, r.Header.Get("Authorization"))
		mu.Unlock()
		rp.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	t.Setenv("GITHUB_TOKEN", "gh-secret")
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			return Run(append([]string{"import", "github", "o/r", "--github-api", gh.URL, "--url", proxy.URL, "--token", tok}, args...))
		})
	}
	out, err := run("--project", "DEV", "--map-user", "@bob=bob@velo.dev", "--github-token", "gh-secret")
	if err != nil || !strings.Contains(out, "Imported 1 issues") {
		t.Fatalf("import: %q %v", out, err)
	}
	iss, err := st.GetIssue("DEV-1")
	if err != nil || iss.Title != "Crash" || iss.AssigneeName != "Bob" || !strings.Contains(iss.Description, "Imported from [o/r#7](https://github.com/o/r/issues/7), opened by @alice") {
		t.Fatalf("DEV-1: %+v %v", iss, err)
	}
	if out, err := run("--project", "DEV", "--github-token", "gh-secret"); err != nil || !strings.Contains(out, "Imported 0 issues (1 already imported)") {
		t.Fatalf("rerun: %q %v", out, err)
	}
	for _, a := range ghAuth {
		if a != "Bearer gh-secret" {
			t.Fatalf("GitHub request without its token: %q", a)
		}
	}
	for _, a := range veloSeen {
		if strings.Contains(a, "gh-secret") {
			t.Fatal("the GitHub token reached Velo")
		}
	}
	// GITHUB_TOKEN is for github.com: another API host gets no token from it.
	ghAuth = nil
	if _, err := run("--project", "DEV"); err != nil {
		t.Fatal(err)
	}
	for _, a := range ghAuth {
		if a != "" {
			t.Fatalf("GITHUB_TOKEN sent to a host other than api.github.com: %q", a)
		}
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("missing project: %v", err)
	}
	if _, err := run("--project", "DEV", "--map-user", "bob"); err == nil || !strings.Contains(err.Error(), "LOGIN=EMAIL") {
		t.Fatalf("bad --map-user: %v", err)
	}
}

// The environment's tokens are github.com tokens: used for
// https://api.github.com only, and never over the --github-token flag.
func TestGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-cli")
	for _, tc := range []struct{ api, flag, want string }{
		{"https://api.github.com", "", "gh-cli"},
		{"https://API.github.com/", "", "gh-cli"},
		{"https://api.github.com", "flag", "flag"},
		{"http://api.github.com", "", ""},
		{"https://ghe.acme.dev/api/v3", "", ""},
		{"https://ghe.acme.dev/api/v3", "flag", "flag"},
		{"://bad", "", ""},
	} {
		if got := githubToken(tc.api, tc.flag); got != tc.want {
			t.Errorf("githubToken(%q, %q) = %q, want %q", tc.api, tc.flag, got, tc.want)
		}
	}
	t.Setenv("GITHUB_TOKEN", "env")
	if got := githubToken("https://api.github.com", ""); got != "env" {
		t.Errorf("GITHUB_TOKEN before GH_TOKEN: %q", got)
	}
}
