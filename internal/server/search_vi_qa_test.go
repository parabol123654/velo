package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// QA: accent-folded search through the HTTP API, including project-scoped
// tokens and the q filter of GET /api/issues.
func TestQASearchVietnameseHTTP(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"Đăng nhập bằng Google"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"OPS","title":"Đăng nhập máy chủ"}`)
	code, b := call(t, "POST", ts.URL+"/api/tokens", admin, `{"name":"dev agent","projects":["DEV"]}`)
	if code != http.StatusCreated {
		t.Fatalf("token: %d %s", code, b)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(b), &tok)

	idents := func(path, bearer string) []string {
		t.Helper()
		code, b := call(t, "GET", ts.URL+path, bearer, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, b)
		}
		var hits []struct {
			Identifier string `json:"identifier"`
		}
		if err := json.Unmarshal([]byte(b), &hits); err != nil {
			t.Fatalf("decode %s: %v", b, err)
		}
		out := []string{}
		for _, h := range hits {
			out = append(out, h.Identifier)
		}
		return out
	}

	for _, q := range []string{"dang nhap", "ĐĂNG NHẬP", "Dang Nhap", `"dang" (nhap)`, "đăng"} {
		if got := idents("/api/search?q="+url.QueryEscape(q), admin); len(got) != 2 {
			t.Errorf("admin search %q: %v", q, got)
		}
		if got := idents("/api/search?q="+url.QueryEscape(q), tok.Token); len(got) != 1 || got[0] != "DEV-1" {
			t.Errorf("DEV token search %q: %v", q, got)
		}
	}
	if got := idents("/api/issues?project=OPS&q="+url.QueryEscape("may chu"), admin); len(got) != 1 || got[0] != "OPS-1" {
		t.Errorf("list issues q: %v", got)
	}
	if got := idents("/api/search?q="+url.QueryEscape("dev-1"), admin); len(got) != 1 || got[0] != "DEV-1" {
		t.Errorf("identifier search: %v", got)
	}
	// After a title edit through the API, the old words are gone.
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", admin, `{"title":"Đổi mật khẩu"}`)
	if got := idents("/api/search?q="+url.QueryEscape("doi mat khau"), admin); len(got) != 1 {
		t.Errorf("after edit: %v", got)
	}
	if got := idents("/api/search?q="+url.QueryEscape("bang google"), admin); len(got) != 0 {
		t.Errorf("old title still found: %v", got)
	}
	if code, b := call(t, "GET", ts.URL+"/api/search?q=dang", "", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated search: %d %s", code, b)
	}
}

// A lone double quote in the search box is a 400 naming the unclosed quote
// (query syntax, #17), not a 500; a closed pair returns an empty result.
func TestQASearchQuoteOnlyHTTP(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"đăng nhập"}`)
	if code, b := call(t, "GET", ts.URL+"/api/search?q="+url.QueryEscape(`"`), admin, ""); code != http.StatusBadRequest || !strings.Contains(b, "unclosed quote") {
		t.Errorf(`GET /api/search?q=": %d %s`, code, b)
	}
	if code, b := call(t, "GET", ts.URL+"/api/search?q="+url.QueryEscape(`""`), admin, ""); code != http.StatusOK {
		t.Errorf(`GET /api/search?q="": %d %s`, code, b)
	}
}
