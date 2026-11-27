package server

import (
	"net/http"
	"strings"
	"testing"
)

// Known gap 4: unknown labels are an error, not silently dropped.
func TestUnknownLabelsAreRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)

	code, b := call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"x","label_ids":["bug","nope","zzz"]}`)
	if code != http.StatusBadRequest || !strings.Contains(b, `\"nope\", \"zzz\"`) {
		t.Fatalf("create with unknown labels: want 400 naming them, got %d %s", code, b)
	}
	if _, b := call(t, "GET", ts.URL+"/api/issues?project=DEV", admin, ""); strings.Contains(b, `"title":"x"`) {
		t.Fatalf("refused create stored an issue: %s", b)
	}

	// The next issue still gets number 1, and known labels match by name in any case.
	if code, b := call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"y","label_ids":["BUG"]}`); code != http.StatusCreated || !strings.Contains(b, `"identifier":"DEV-1"`) || !strings.Contains(b, `"name":"bug"`) {
		t.Fatalf("create with known label: %d %s", code, b)
	}

	code, b = call(t, "PATCH", ts.URL+"/api/issues/DEV-1", admin, `{"title":"changed","label_ids":["feature","nope"]}`)
	if code != http.StatusBadRequest || !strings.Contains(b, "nope") {
		t.Fatalf("update with unknown label: want 400 naming it, got %d %s", code, b)
	}
	_, b = call(t, "GET", ts.URL+"/api/issues/DEV-1", admin, "")
	if strings.Contains(b, `"title":"changed"`) || !strings.Contains(b, `"name":"bug"`) || strings.Contains(b, `"name":"feature"`) {
		t.Fatalf("refused update changed the issue: %s", b)
	}

	if code, b := call(t, "PATCH", ts.URL+"/api/issues/DEV-1", admin, `{"label_ids":[]}`); code != http.StatusOK || strings.Contains(b, `"name":"bug"`) {
		t.Fatalf("clearing labels: %d %s", code, b)
	}
}
