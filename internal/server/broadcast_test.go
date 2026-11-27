package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

// Label and user mutations reach open boards over SSE, label events scoped
// to their project.
func TestLabelAndUserEventsAreBroadcast(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	users, _ := srv.store.ListUsers()
	devTok, _, err := srv.store.CreateAPIToken(users[0].ID, "dev only", db.TokenScope{Projects: []string{"DEV"}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := func(auth string) *bufio.Reader {
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		rd := bufio.NewReader(resp.Body)
		rd.ReadString('\n') // connected
		return rd
	}
	all, dev := stream(admin), stream(devTok)

	_, b := call(t, "POST", ts.URL+"/api/projects/OPS/labels", admin, `{"name":"ops-secret","color":"#000000"}`)
	opsLabel := idOf(b)
	call(t, "DELETE", ts.URL+"/api/labels/"+opsLabel, admin, "")
	_, b = call(t, "POST", ts.URL+"/api/projects/DEV/labels", admin, `{"name":"infra","color":"#000000"}`)
	devLabel := idOf(b)
	call(t, "DELETE", ts.URL+"/api/labels/"+devLabel, admin, "")
	if code, b := call(t, "POST", ts.URL+"/api/users", admin, `{"email":"direct@velo.dev","name":"Direct","password":"password123"}`); code != http.StatusCreated {
		t.Fatalf("create user: %d %s", code, b)
	}
	_, b = call(t, "POST", ts.URL+"/api/invites", admin, `{"role":"member"}`)
	var inv db.Invite
	if err := json.Unmarshal([]byte(b), &inv); err != nil || inv.Code == "" {
		t.Fatalf("create invite: %v %s", err, b)
	}
	if c, b := call(t, "POST", ts.URL+"/api/auth/accept-invite", "", `{"code":"`+inv.Code+`","email":"invited@velo.dev","name":"Invited","password":"password123"}`); c != http.StatusCreated {
		t.Fatalf("accept invite: %d %s", c, b)
	}

	// Read each stream until the last event; check what arrived in between.
	read := func(rd *bufio.Reader) string {
		var got strings.Builder
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended early; got:\n%s", got.String())
			}
			got.WriteString(line)
			if strings.Contains(line, `"type":"user.created"`) && strings.Contains(line, "invited@velo.dev") {
				return got.String()
			}
		}
	}
	for name, rd := range map[string]*bufio.Reader{"admin": all, "dev token": dev} {
		got := read(rd)
		for _, want := range []string{`"type":"label.created"`, `"name":"infra"`, `"type":"label.deleted"`, `"id":"` + devLabel + `"`, "direct@velo.dev"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s stream lacks %s:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "password_hash") {
			t.Errorf("%s stream leaks a password hash:\n%s", name, got)
		}
		// Both OPS label events (created by name, deleted by id) reach only the admin.
		for _, ops := range []string{"ops-secret", `"id":"` + opsLabel + `"`} {
			if present := strings.Contains(got, ops); present != (name == "admin") {
				t.Errorf("%s stream: OPS label event %s present = %v:\n%s", name, ops, present, got)
			}
		}
	}
}
