package server

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLinksAPIAndBlockedRefresh(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"blocker"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"waiting"}`)

	if code, b := call(t, "POST", ts.URL+"/api/issues/DEV-1/links", admin, `{"kind":"blocks","target":"DEV-2"}`); code != http.StatusCreated || !strings.Contains(b, `"kind":"blocks"`) {
		t.Fatalf("link: %d %s", code, b)
	}
	if _, b := call(t, "GET", ts.URL+"/api/issues/DEV-2", admin, ""); !strings.Contains(b, `"blocked_by_count":1`) {
		t.Fatalf("DEV-2 not blocked: %s", b)
	}

	// Finishing the blocker pushes a fresh DEV-2 to open boards.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	rd.ReadString('\n')
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", admin, `{"state_id":"Done"}`)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("no refreshed DEV-2 event: %v", err)
		}
		if strings.Contains(line, `"identifier":"DEV-2"`) && strings.Contains(line, `"blocked_by_count":0`) {
			break
		}
	}

	if code, _ := call(t, "DELETE", ts.URL+"/api/issues/DEV-2/links/DEV-1", admin, ""); code != http.StatusOK {
		t.Fatalf("unlink: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/issues/DEV-2/links/DEV-1", admin, ""); code != http.StatusNotFound {
		t.Fatalf("unlink twice: %d", code)
	}
}
