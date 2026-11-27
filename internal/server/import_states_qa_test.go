package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Through the HTTP API: export a project with a custom state, import it
// under another key (201, issue in the custom state); a bundle with an
// undefined state is a 400 naming the issue, and creates nothing; no login
// and a read-only token are refused.
func TestQAImportStatesHTTP(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"SRC","name":"Source"}`)
	if code, b := call(t, "POST", ts.URL+"/api/projects/SRC/states", admin, `{"name":"Kiểm thử","category":"started","color":"#123456"}`); code != http.StatusCreated {
		t.Fatalf("create state: %d %s", code, b)
	}
	if code, b := call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"SRC","title":"in QA","state_id":"Kiểm thử"}`); code != http.StatusCreated {
		t.Fatalf("create issue: %d %s", code, b)
	}
	code, exported := call(t, "GET", ts.URL+"/api/projects/SRC/export", admin, "")
	if code != http.StatusOK {
		t.Fatalf("export: %d %s", code, exported)
	}
	var bundle map[string]any
	if err := json.Unmarshal([]byte(exported), &bundle); err != nil {
		t.Fatal(err)
	}
	bundle["project"].(map[string]any)["key"] = "DST"
	raw, _ := json.Marshal(bundle)

	if code, _ := call(t, "POST", ts.URL+"/api/projects/import", "", string(raw)); code != http.StatusUnauthorized {
		t.Errorf("import without login: %d, want 401", code)
	}
	_, tb := call(t, "POST", ts.URL+"/api/tokens", admin, `{"name":"reader","access":"read"}`)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tb), &tok)
	if code, _ := call(t, "POST", ts.URL+"/api/projects/import", tok.Token, string(raw)); code != http.StatusForbidden {
		t.Errorf("import with read-only token: %d, want 403", code)
	}
	if code, _ := call(t, "GET", ts.URL+"/api/projects/DST", admin, ""); code != http.StatusNotFound {
		t.Fatalf("refused imports created DST: %d", code)
	}

	if code, b := call(t, "POST", ts.URL+"/api/projects/import", admin, string(raw)); code != http.StatusCreated {
		t.Fatalf("import: %d %s", code, b)
	}
	code, b := call(t, "GET", ts.URL+"/api/issues/DST-1", admin, "")
	if code != http.StatusOK || !strings.Contains(b, `"state_name":"Kiểm thử"`) {
		t.Fatalf("DST-1: %d %s", code, b)
	}
	code, b = call(t, "GET", ts.URL+"/api/projects/DST/states", admin, "")
	if code != http.StatusOK || strings.Count(b, `"id"`) != 7 || !strings.Contains(b, `"color":"#123456"`) {
		t.Fatalf("DST states: %d %s", code, b)
	}

	// Undefined state: 400 naming the issue, nothing created.
	bad := `{"project":{"key":"BAD","name":"Bad"},"states":[],"issues":[{"identifier":"OLD-1","title":"x","state_name":"Nowhere"}]}`
	code, b = call(t, "POST", ts.URL+"/api/projects/import", admin, bad)
	if code != http.StatusBadRequest || !strings.Contains(b, `OLD-1`) || !strings.Contains(b, `Nowhere`) {
		t.Fatalf("undefined state: %d %s", code, b)
	}
	// Wrong type for a state field: 400.
	code, b = call(t, "POST", ts.URL+"/api/projects/import", admin, `{"project":{"key":"BAD","name":"Bad"},"states":[{"name":"QA","category":5}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("category of wrong type: %d %s", code, b)
	}
	// Unknown category: 400.
	code, b = call(t, "POST", ts.URL+"/api/projects/import", admin, `{"project":{"key":"BAD","name":"Bad"},"states":[{"name":"QA","category":"review"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(b, "review") {
		t.Fatalf("unknown category: %d %s", code, b)
	}
	if code, _ := call(t, "GET", ts.URL+"/api/projects/BAD", admin, ""); code != http.StatusNotFound {
		t.Fatalf("refused imports created BAD: %d", code)
	}
}
