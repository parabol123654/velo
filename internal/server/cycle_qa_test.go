package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// qaCycleEnv: admin session, projects DEV and OPS, one DEV cycle with every field set.
func qaCycleEnv(t *testing.T) (url, admin, cycleID string, srv *Server) {
	t.Helper()
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin = sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	code, b := call(t, "POST", ts.URL+"/api/projects/DEV/cycles", admin, `{"name":"Sprint 1","description":"Ship search","start_date":"2026-12-01","end_date":"2026-12-14","status":"upcoming"}`)
	if code != http.StatusCreated {
		t.Fatalf("create cycle: %d %s", code, b)
	}
	return ts.URL, admin, idOf(b), srv
}

func qaGetCycle(t *testing.T, srv *Server, id string) db.Cycle {
	t.Helper()
	pid, err := srv.store.ProjectIDOf("cycle", id)
	if err != nil {
		t.Fatal(err)
	}
	cycles, err := srv.store.ListCycles(pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cycles {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("cycle %s not found", id)
	return db.Cycle{}
}

func qaWantOriginal(t *testing.T, what string, c db.Cycle) {
	t.Helper()
	if c.Name != "Sprint 1" || c.Description != "Ship search" || c.StartDate != "2026-12-01" || c.EndDate != "2026-12-14" || c.Status != "upcoming" {
		t.Errorf("%s changed the cycle: %+v", what, c)
	}
}

// JSON null must never blank a field (null name would otherwise bypass the
// "name cannot be empty" rule) and must not 500.
func TestQACyclePatchNull(t *testing.T) {
	url, admin, id, srv := qaCycleEnv(t)
	for _, body := range []string{`{"name":null}`, `{"status":null}`, `{"start_date":null,"end_date":null}`, `{"description":null}`} {
		code, b := call(t, "PATCH", url+"/api/cycles/"+id, admin, body)
		if code != http.StatusOK && code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, code, b)
		}
		qaWantOriginal(t, body, qaGetCycle(t, srv, id))
	}
}

// Unknown fields are ignored, wrong types and broken bodies are 400, and a
// failed PATCH writes nothing (even when other fields in it are valid).
func TestQACyclePatchBadInput(t *testing.T) {
	url, admin, id, srv := qaCycleEnv(t)
	if code, b := call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"foo":"bar","project_id":"x","id":"y"}`); code != http.StatusOK {
		t.Errorf("unknown fields: %d %s", code, b)
	}
	qaWantOriginal(t, "unknown fields", qaGetCycle(t, srv, id))

	for _, body := range []string{
		`{"name":5}`, `{"status":true}`, `{"description":["a"]}`, ``, `not json`, `[]`, `"x"`,
		`{"name":"   "}`, `{"status":"done"}`, `{"status":"Active"}`, `{"status":""}`,
		`{"description":"new","status":"bogus"}`, `{"name":"","end_date":"2027-01-01"}`,
	} {
		code, b := call(t, "PATCH", url+"/api/cycles/"+id, admin, body)
		if code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d %s", body, code, b)
		}
		qaWantOriginal(t, body, qaGetCycle(t, srv, id))
	}

	if code, _ := call(t, "PATCH", url+"/api/cycles/nope", admin, `{"status":"active"}`); code != http.StatusNotFound {
		t.Errorf("unknown cycle: want 404, got %d", code)
	}
	if code, _ := call(t, "PATCH", url+"/api/cycles/"+id, "", `{"status":"active"}`); code != http.StatusUnauthorized {
		t.Errorf("no login: want 401, got %d", code)
	}

	// Very long values and trimming.
	long := strings.Repeat("x", 10000)
	code, b := call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"name":"  Sprint 2  ","description":"`+long+`"}`)
	if code != http.StatusOK || !strings.Contains(b, `"name":"Sprint 2"`) {
		t.Fatalf("long description: %d %.200s", code, b)
	}
	c := qaGetCycle(t, srv, id)
	if c.Name != "Sprint 2" || len(c.Description) != 10000 || c.Status != "upcoming" || c.StartDate != "2026-12-01" {
		t.Fatalf("after rename: %+v", c)
	}
}

// Read-only tokens and tokens for another project cannot PATCH; a token for
// the cycle's own project can.
func TestQACyclePatchPermissions(t *testing.T) {
	url, admin, id, srv := qaCycleEnv(t)
	_, b := call(t, "POST", url+"/api/projects/OPS/cycles", admin, `{"name":"Ops sprint"}`)
	opsID := idOf(b)
	users, _ := srv.store.ListUsers()
	ro, _, _ := srv.store.CreateAPIToken(users[0].ID, "ro", db.TokenScope{Access: "read"})
	opsTok, _, _ := srv.store.CreateAPIToken(users[0].ID, "ops", db.TokenScope{Projects: []string{"OPS"}})
	devTok, _, _ := srv.store.CreateAPIToken(users[0].ID, "dev", db.TokenScope{Projects: []string{"DEV"}})

	if code, b := call(t, "PATCH", url+"/api/cycles/"+id, ro, `{"status":"active"}`); code != http.StatusForbidden {
		t.Errorf("read-only token: want 403, got %d %s", code, b)
	}
	if code, b := call(t, "PATCH", url+"/api/cycles/"+id, opsTok, `{"status":"active"}`); code != http.StatusForbidden {
		t.Errorf("OPS token on DEV cycle: want 403, got %d %s", code, b)
	}
	qaWantOriginal(t, "forbidden PATCH", qaGetCycle(t, srv, id))
	if code, b := call(t, "PATCH", url+"/api/cycles/"+opsID, devTok, `{"name":"pwned"}`); code != http.StatusForbidden {
		t.Errorf("DEV token on OPS cycle: want 403, got %d %s", code, b)
	}
	if code, b := call(t, "PATCH", url+"/api/cycles/"+id, devTok, `{"status":"active"}`); code != http.StatusOK || !strings.Contains(b, `"status":"active"`) {
		t.Errorf("DEV token on DEV cycle: %d %s", code, b)
	}
	// A restricted token cannot list the other project's cycles (FindCycle's source).
	if code, _ := call(t, "GET", url+"/api/projects/OPS/cycles", devTok, ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("DEV token listing OPS cycles: %d", code)
	}
}

// cycle.updated carries the full updated cycle and reaches only streams
// allowed to see its project.
func TestQACycleUpdatedSSE(t *testing.T) {
	url, admin, id, srv := qaCycleEnv(t)
	_, b := call(t, "POST", url+"/api/projects/OPS/cycles", admin, `{"name":"Ops sprint"}`)
	opsID := idOf(b)
	users, _ := srv.store.ListUsers()
	devTok, _, _ := srv.store.CreateAPIToken(users[0].ID, "dev", db.TokenScope{Projects: []string{"DEV"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := func(auth string) *bufio.Reader {
		req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		rd := bufio.NewReader(resp.Body)
		rd.ReadString('\n')
		return rd
	}
	all, dev := stream(admin), stream(devTok)

	call(t, "PATCH", url+"/api/cycles/"+opsID, admin, `{"name":"ops-secret-rename"}`)
	call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"status":"completed"}`)
	// A failed PATCH sends nothing.
	call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"status":"bogus-status"}`)
	call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"name":"Sprint END"}`)

	read := func(name string, rd *bufio.Reader) string {
		var got strings.Builder
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("%s: stream ended early; got:\n%s", name, got.String())
			}
			got.WriteString(line)
			if strings.Contains(line, "Sprint END") {
				return got.String()
			}
		}
	}
	gotAll, gotDev := read("admin", all), read("dev", dev)
	if !strings.Contains(gotAll, "ops-secret-rename") {
		t.Errorf("admin stream missed the OPS cycle.updated:\n%s", gotAll)
	}
	if strings.Contains(gotDev, "ops-secret") || strings.Contains(gotDev, opsID) {
		t.Errorf("DEV token received an OPS cycle event:\n%s", gotDev)
	}
	for name, got := range map[string]string{"admin": gotAll, "dev": gotDev} {
		if strings.Contains(got, "bogus-status") {
			t.Errorf("%s: failed PATCH was broadcast:\n%s", name, got)
		}
		// The status-only event carries the whole cycle, kept fields included.
		var found bool
		for _, line := range strings.Split(got, "\n") {
			if !strings.Contains(line, `"cycle.updated"`) || !strings.Contains(line, `"status":"completed"`) || strings.Contains(line, "Sprint END") {
				continue
			}
			found = true
			for _, want := range []string{`"id":"` + id + `"`, `"name":"Sprint 1"`, `"description":"Ship search"`, `"start_date":"2026-12-01"`, `"end_date":"2026-12-14"`} {
				if !strings.Contains(line, want) {
					t.Errorf("%s: cycle.updated missing %s: %s", name, want, line)
				}
			}
		}
		if !found {
			t.Errorf("%s: no cycle.updated event with status completed:\n%s", name, got)
		}
	}
}

// Concurrent PATCHes of different fields must all survive.
func TestQACycleConcurrentPatches(t *testing.T) {
	url, admin, id, srv := qaCycleEnv(t)
	for round := 0; round < 5; round++ {
		bodies := []string{
			fmt.Sprintf(`{"name":"Sprint r%d"}`, round),
			fmt.Sprintf(`{"description":"desc r%d"}`, round),
			fmt.Sprintf(`{"start_date":"2027-01-0%d"}`, round+1),
			fmt.Sprintf(`{"end_date":"2027-02-0%d"}`, round+1),
			[]string{`{"status":"active"}`, `{"status":"completed"}`, `{"status":"upcoming"}`, `{"status":"active"}`, `{"status":"completed"}`}[round],
		}
		var wg sync.WaitGroup
		errs := make(chan string, len(bodies)*4)
		for i := 0; i < 4; i++ { // each body sent four times at once
			for _, body := range bodies {
				wg.Add(1)
				go func(body string) {
					defer wg.Done()
					req, _ := http.NewRequest("PATCH", url+"/api/cycles/"+id, strings.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+admin)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						errs <- err.Error()
						return
					}
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						errs <- fmt.Sprintf("%s: %d", body, resp.StatusCode)
					}
				}(body)
			}
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Errorf("round %d: %s", round, e)
		}
		c := qaGetCycle(t, srv, id)
		var st struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(bodies[4]), &st)
		if c.Name != fmt.Sprintf("Sprint r%d", round) || c.Description != fmt.Sprintf("desc r%d", round) ||
			c.StartDate != fmt.Sprintf("2027-01-0%d", round+1) || c.EndDate != fmt.Sprintf("2027-02-0%d", round+1) || c.Status != st.Status {
			t.Fatalf("round %d: a concurrent PATCH was lost: %+v", round, c)
		}
	}
}

// After a rename, issues filter by the new name (any case) and not the old;
// the issue shows the new cycle name.
func TestQACycleRenameAndIssueFilter(t *testing.T) {
	url, admin, id, _ := qaCycleEnv(t)
	if code, b := call(t, "POST", url+"/api/issues", admin, `{"project_ref":"DEV","title":"in sprint","cycle_id":"Sprint 1"}`); code != http.StatusCreated {
		t.Fatalf("create issue: %d %s", code, b)
	}
	call(t, "POST", url+"/api/issues", admin, `{"project_ref":"DEV","title":"no sprint"}`)
	if code, b := call(t, "PATCH", url+"/api/cycles/"+id, admin, `{"name":"Renamed Sprint"}`); code != http.StatusOK {
		t.Fatalf("rename: %d %s", code, b)
	}
	_, b := call(t, "GET", url+"/api/issues?project=DEV&cycle=renamed%20sprint", admin, "")
	if !strings.Contains(b, "in sprint") || strings.Contains(b, "no sprint") {
		t.Errorf("filter by new name: %s", b)
	}
	_, b = call(t, "GET", url+"/api/issues?project=DEV&cycle=Sprint%201", admin, "")
	if strings.Contains(b, "in sprint") {
		t.Errorf("filter by old name still matches: %s", b)
	}
	_, b = call(t, "GET", url+"/api/issues/DEV-1", admin, "")
	if !strings.Contains(b, "Renamed Sprint") {
		t.Errorf("issue does not show the new cycle name: %s", b)
	}
	// The issue can still be moved into the cycle by its new name.
	if code, b := call(t, "PATCH", url+"/api/issues/DEV-2", admin, `{"cycle_id":"RENAMED SPRINT"}`); code != http.StatusOK || !strings.Contains(b, id) {
		t.Errorf("assign by new name: %d %s", code, b)
	}
}
