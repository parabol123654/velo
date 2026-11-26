package server

import (
	"net/http"
	"testing"
	"time"
)

func TestLoginRateLimit(t *testing.T) {
	ts, srv := newTestServer(t)
	call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	srv.logins.now = func() time.Time { return now }

	login := func(pw string) int {
		t.Helper()
		code, _ := call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"admin@velo.dev","password":"`+pw+`"}`)
		return code
	}
	if login("password123") != http.StatusOK {
		t.Fatal("normal login failed")
	}
	for i := 1; i <= 5; i++ {
		if code := login("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
	if code := login("wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("6th wrong attempt: %d, want 429", code)
	}
	if code := login("password123"); code != http.StatusTooManyRequests {
		t.Fatalf("correct password while locked: %d, want 429", code)
	}
	// Another account from the same IP is locked too (per-IP key).
	if code, _ := call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"other@velo.dev","password":"x"}`); code != http.StatusTooManyRequests {
		t.Fatalf("other email from locked IP: %d, want 429", code)
	}

	now = now.Add(15 * time.Minute)
	if login("password123") != http.StatusOK {
		t.Fatal("lock did not lift after the window")
	}
	// A success resets the email counter; failures spread over time do not lock.
	for i := 0; i < 4; i++ {
		login("wrong")
	}
	if login("password123") != http.StatusOK {
		t.Fatal("4 failures should not lock")
	}
}
