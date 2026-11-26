package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

func TestEditAndDeleteEndpoints(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	if _, err := srv.store.CreateUserDirect("dev@velo.dev", "Dev", "devpass1", "member"); err != nil {
		t.Fatal(err)
	}
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"dev@velo.dev","password":"devpass1"}`)
	dev := sessionOf(t, body)

	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"one"}`)

	id := func(body string) string {
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(body), &v)
		return v.ID
	}

	// Comments: author edits; another member cannot; admin can delete.
	_, body = call(t, "POST", ts.URL+"/api/issues/DEV-1/comments", dev, `{"body":"first"}`)
	cid := id(body)
	if code, b := call(t, "PATCH", ts.URL+"/api/comments/"+cid, dev, `{"body":"edited"}`); code != http.StatusOK || !strings.Contains(b, "edited") {
		t.Fatalf("author edit: %d %s", code, b)
	}
	_, body = call(t, "POST", ts.URL+"/api/issues/DEV-1/comments", admin, `{"body":"admin note"}`)
	if code, _ := call(t, "DELETE", ts.URL+"/api/comments/"+id(body), dev, ""); code != http.StatusForbidden {
		t.Fatalf("member deleting admin comment: %d", code)
	}
	if code, _ := call(t, "PATCH", ts.URL+"/api/comments/"+cid, dev, `{"body":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("empty body: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/comments/"+cid, admin, ""); code != http.StatusOK {
		t.Fatalf("admin delete: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/comments/"+cid, admin, ""); code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", code)
	}

	// Cycles: deleting keeps the issue, without a cycle.
	_, body = call(t, "POST", ts.URL+"/api/projects/DEV/cycles", dev, `{"name":"S1"}`)
	cyc := id(body)
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", dev, `{"cycle_id":"`+cyc+`"}`)
	if code, _ := call(t, "DELETE", ts.URL+"/api/cycles/"+cyc, dev, ""); code != http.StatusOK {
		t.Fatalf("delete cycle: %d", code)
	}
	if iss, err := srv.store.GetIssue("DEV-1"); err != nil || iss.CycleID != "" {
		t.Fatalf("issue after cycle delete: %+v %v", iss, err)
	}

	// Invites: revoke unused; used ones stay.
	_, body = call(t, "POST", ts.URL+"/api/invites", admin, `{}`)
	if code, _ := call(t, "DELETE", ts.URL+"/api/invites/"+id(body), admin, ""); code != http.StatusOK {
		t.Fatalf("revoke invite: %d", code)
	}
	users, _ := srv.store.ListUsers()
	inv, err := srv.store.CreateInvite(users[0].ID, "member", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AcceptInvite(inv.Code, "new@velo.dev", "New", "secret123"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/invites/"+inv.ID, admin, ""); code != http.StatusConflict {
		t.Fatalf("revoke used invite: %d", code)
	}

	// Projects: members rename, only admins delete; delete cascades to issues.
	if code, b := call(t, "PATCH", ts.URL+"/api/projects/DEV", dev, `{"name":"Platform","description":"d"}`); code != http.StatusOK || !strings.Contains(b, "Platform") {
		t.Fatalf("rename: %d %s", code, b)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/projects/DEV", dev, ""); code != http.StatusForbidden {
		t.Fatalf("member delete project: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/projects/DEV", admin, ""); code != http.StatusOK {
		t.Fatalf("admin delete project: %d", code)
	}
	if _, err := srv.store.GetIssue("DEV-1"); err != db.ErrNotFound {
		t.Fatalf("issue survived project delete: %v", err)
	}
	if res, err := srv.store.SearchIssues("one", 10); err != nil || len(res) != 0 {
		t.Fatalf("search index kept deleted issues: %v %v", res, err)
	}
}
