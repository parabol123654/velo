package server

import (
	"net/http"
	"strings"
	"testing"
)

// Known gap 3: a blocks link must not close a loop of any length.
func TestBlockingLoopIsRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	for _, title := range []string{"a", "b", "c", "d"} {
		call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"`+title+`"}`)
	}
	link := func(from, kind, to string) (int, string) {
		return call(t, "POST", ts.URL+"/api/issues/"+from+"/links", admin, `{"kind":"`+kind+`","target":"`+to+`"}`)
	}
	for _, l := range [][2]string{{"DEV-1", "DEV-2"}, {"DEV-2", "DEV-3"}} {
		if code, b := link(l[0], "blocks", l[1]); code != http.StatusCreated {
			t.Fatalf("%s blocks %s: %d %s", l[0], l[1], code, b)
		}
	}

	// DEV-3 blocks DEV-1 closes DEV-1 → DEV-2 → DEV-3 → DEV-1.
	// blocked_by states the same link from the other side.
	for _, tc := range []struct{ from, kind, to string }{
		{"DEV-3", "blocks", "DEV-1"},
		{"DEV-1", "blocked_by", "DEV-3"},
	} {
		code, b := link(tc.from, tc.kind, tc.to)
		if code != http.StatusConflict || !strings.Contains(b, "DEV-1 → DEV-2 → DEV-3") {
			t.Fatalf("%s %s %s: want 409 naming the chain, got %d %s", tc.from, tc.kind, tc.to, code, b)
		}
	}
	if _, b := call(t, "GET", ts.URL+"/api/issues/DEV-1", admin, ""); !strings.Contains(b, `"blocked_by_count":0`) {
		t.Fatalf("refused link was stored: %s", b)
	}

	// Links that close no loop still work: relates back, and a side branch.
	if code, b := link("DEV-3", "relates", "DEV-1"); code != http.StatusCreated {
		t.Fatalf("DEV-3 relates DEV-1: %d %s", code, b)
	}
	if code, b := link("DEV-4", "blocks", "DEV-1"); code != http.StatusCreated {
		t.Fatalf("DEV-4 blocks DEV-1: %d %s", code, b)
	}
}
