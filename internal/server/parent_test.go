package server

import (
	"net/http"
	"strings"
	"testing"
)

// Known gap 2: a parent change must not create a loop.
func TestParentLoopIsRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	for _, title := range []string{"a", "b", "c", "d"} {
		call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"`+title+`"}`)
	}
	// DEV-3 under DEV-2 under DEV-1.
	for _, p := range [][2]string{{"DEV-2", "DEV-1"}, {"DEV-3", "DEV-2"}} {
		if code, b := call(t, "PATCH", ts.URL+"/api/issues/"+p[0], admin, `{"parent_id":"`+p[1]+`"}`); code != http.StatusOK {
			t.Fatalf("set parent of %s: %d %s", p[0], code, b)
		}
	}

	for _, tc := range []struct{ issue, parent string }{
		{"DEV-1", "DEV-2"}, // two-issue loop
		{"DEV-1", "DEV-3"}, // three-issue loop
		{"DEV-2", "DEV-3"}, // under its own child
	} {
		code, b := call(t, "PATCH", ts.URL+"/api/issues/"+tc.issue, admin, `{"parent_id":"`+strings.ToLower(tc.parent)+`","title":"changed"}`)
		if code != http.StatusBadRequest || !strings.Contains(b, tc.parent+" is under "+tc.issue) {
			t.Fatalf("%s under %s: want 400 naming the loop, got %d %s", tc.issue, tc.parent, code, b)
		}
	}

	// Nothing was written by the refused patches.
	for id, parent := range map[string]string{"DEV-1": "", "DEV-2": "DEV-1", "DEV-3": "DEV-2"} {
		_, b := call(t, "GET", ts.URL+"/api/issues/"+id, admin, "")
		if strings.Contains(b, `"title":"changed"`) {
			t.Fatalf("%s was changed by a refused patch: %s", id, b)
		}
		if parent == "" && strings.Contains(b, `"parent_identifier"`) || parent != "" && !strings.Contains(b, `"parent_identifier":"`+parent+`"`) {
			t.Fatalf("%s parent changed, want %q: %s", id, parent, b)
		}
	}

	// Moving within the tree without a loop still works.
	if code, b := call(t, "PATCH", ts.URL+"/api/issues/DEV-3", admin, `{"parent_id":"DEV-1"}`); code != http.StatusOK {
		t.Fatalf("reparent DEV-3 under DEV-1: %d %s", code, b)
	}
	if code, b := call(t, "PATCH", ts.URL+"/api/issues/DEV-4", admin, `{"parent_id":"DEV-3"}`); code != http.StatusOK {
		t.Fatalf("DEV-4 under DEV-3: %d %s", code, b)
	}
}
