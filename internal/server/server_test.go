package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
	"github.com/parabol123654/velo/web"
)

func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	tmp := t.TempDir()
	st, err := db.Open(filepath.Join(tmp, "velo.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	distFS, err := web.DistFS()
	if err != nil {
		t.Fatalf("distFS: %v", err)
	}
	srv, err := New(st, tmp, distFS)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
}

func TestEndToEndHTTPAuthIssuesUploadAndSSE(t *testing.T) {
	ts, _ := newTestServer(t)

	// 1. Unauthenticated access to /api/projects must fail 401
	resp, err := http.Get(ts.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 before setup, got %d", resp.StatusCode)
	}

	// 2. First-run setup creates admin and returns session cookie + token
	setupPayload := `{"email":"admin@velo.dev","name":"Founder","password":"password123"}`
	resp, err = http.Post(ts.URL+"/api/auth/setup", "application/json", strings.NewReader(setupPayload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup expected 201, got %d", resp.StatusCode)
	}

	// 3. Create Personal API Token using session cookie
	var setupRes struct {
		SessionToken string `json:"session_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&setupRes)

	req, _ := http.NewRequest("POST", ts.URL+"/api/tokens", strings.NewReader(`{"name":"CLI"}`))
	req.Header.Set("Authorization", "Bearer "+setupRes.SessionToken)
	tokResp, err := http.DefaultClient.Do(req)
	if err != nil || tokResp.StatusCode != http.StatusCreated {
		t.Fatalf("create token failed: %v, status=%d", err, tokResp.StatusCode)
	}
	var tokData struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(tokResp.Body).Decode(&tokData)
	tokResp.Body.Close()
	if !strings.HasPrefix(tokData.Token, "velo_") {
		t.Fatalf("expected velo_ token, got %s", tokData.Token)
	}

	// 4. Subscribe to SSE stream with Bearer token
	sseCtx, cancelSSE := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSSE()
	sseReq, _ := http.NewRequestWithContext(sseCtx, "GET", ts.URL+"/api/events", nil)
	sseReq.Header.Set("Authorization", "Bearer "+tokData.Token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil || sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer sseResp.Body.Close()
	sseReader := bufio.NewReader(sseResp.Body)

	// Read initial connected event
	line, err := sseReader.ReadString('\n')
	if err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("expected connected SSE event, got %q (%v)", line, err)
	}
	_, _ = sseReader.ReadString('\n') // data line
	_, _ = sseReader.ReadString('\n') // blank line

	// 5. Create Project + Issue using API token, verify SSE receives issue.created
	doAPI := func(method, path, body string) *http.Response {
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokData.Token)
		r.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return res
	}

	pRes := doAPI("POST", "/api/projects", `{"key":"APP","name":"Velo App"}`)
	if pRes.StatusCode != http.StatusCreated {
		t.Fatalf("create project status=%d", pRes.StatusCode)
	}
	pRes.Body.Close()

	iRes := doAPI("POST", "/api/issues", `{"project_ref":"APP","title":"Keyboard navigation j/k","priority":3}`)
	if iRes.StatusCode != http.StatusCreated {
		t.Fatalf("create issue status=%d", iRes.StatusCode)
	}
	iRes.Body.Close()

	// Drain SSE lines until we observe issue.created
	foundIssueEvent := false
	for i := 0; i < 12; i++ {
		l, err := sseReader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(l, "issue.created") {
			foundIssueEvent = true
			break
		}
	}
	if !foundIssueEvent {
		t.Fatalf("did not receive issue.created over SSE")
	}

	// 6. Upload clipboard image attachment
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "bug paste.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nfake-png-bytes"))
	_ = mw.Close()

	upReq, _ := http.NewRequest("POST", ts.URL+"/api/attachments", &buf)
	upReq.Header.Set("Authorization", "Bearer "+tokData.Token)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRes, err := http.DefaultClient.Do(upReq)
	if err != nil || upRes.StatusCode != http.StatusCreated {
		t.Fatalf("upload attachment failed: %v, status=%d", err, upRes.StatusCode)
	}
	var upPayload struct {
		Markdown string `json:"markdown"`
	}
	_ = json.NewDecoder(upRes.Body).Decode(&upPayload)
	upRes.Body.Close()
	if !strings.HasPrefix(upPayload.Markdown, "![bug_paste.png](/api/attachments/") {
		t.Fatalf("unexpected attachment markdown: %s", upPayload.Markdown)
	}

	// 7. SPA fallback serves HTML for client routes
	spaRes, err := http.Get(ts.URL + "/projects/APP")
	if err != nil || spaRes.StatusCode != http.StatusOK {
		t.Fatalf("SPA fallback failed: %v", err)
	}
	spaRes.Body.Close()
}
