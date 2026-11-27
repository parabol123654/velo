package server

import (
	"net/http"
	"strings"
	"testing"
)

// The Docker HEALTHCHECK calls GET /api/health without credentials (#1).
func TestHealthEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	code, b := call(t, "GET", ts.URL+"/api/health", "", "")
	if code != http.StatusOK || !strings.Contains(b, `"status":"ok"`) {
		t.Fatalf("GET /api/health: %d %s", code, b)
	}
}
