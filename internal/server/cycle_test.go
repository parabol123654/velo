package server

import (
	"net/http"
	"strings"
	"testing"
)

// #2: PATCH /api/cycles/{id} changes only the fields in the body.
func TestCyclePatchIsPartial(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	_, b := call(t, "POST", ts.URL+"/api/projects/DEV/cycles", admin, `{"name":"Sprint 1","description":"Ship search","start_date":"2026-12-01","end_date":"2026-12-14","status":"upcoming"}`)
	id := idOf(b)

	code, b := call(t, "PATCH", ts.URL+"/api/cycles/"+id, admin, `{"status":"completed"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %s", code, b)
	}
	for _, want := range []string{`"status":"completed"`, `"description":"Ship search"`, `"start_date":"2026-12-01"`, `"end_date":"2026-12-14"`} {
		if !strings.Contains(b, want) {
			t.Errorf("status-only PATCH lost %s: %s", want, b)
		}
	}
	// Sending every field, as the web UI does, still replaces them all.
	code, b = call(t, "PATCH", ts.URL+"/api/cycles/"+id, admin, `{"name":"S1","description":"","start_date":"","end_date":"","status":"active"}`)
	if code != http.StatusOK || !strings.Contains(b, `"name":"S1"`) || strings.Contains(b, "Ship search") || strings.Contains(b, "2026-12") {
		t.Fatalf("full PATCH: %d %s", code, b)
	}
	if code, b := call(t, "PATCH", ts.URL+"/api/cycles/"+id, admin, `{"name":""}`); code != http.StatusBadRequest {
		t.Fatalf("empty name: %d %s", code, b)
	}
}
