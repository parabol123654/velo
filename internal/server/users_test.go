package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// call sends a JSON request with a bearer token and returns status and body.
func call(t *testing.T, method, url, bearer, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func sessionOf(t *testing.T, body string) string {
	t.Helper()
	var v struct {
		SessionToken string `json:"session_token"`
		User         struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.SessionToken == "" {
		t.Fatalf("no session in %s", body)
	}
	return v.SessionToken
}

func TestUserManagementHTTP(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)

	dev, err := srv.store.CreateUserDirect("dev@velo.dev", "Dev", "devpass1", "member")
	if err != nil {
		t.Fatal(err)
	}
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"dev@velo.dev","password":"devpass1"}`)
	devSess := sessionOf(t, body)

	// Members cannot manage users.
	if code, _ := call(t, "PATCH", ts.URL+"/api/users/"+dev.ID, devSess, `{"disabled":true}`); code != http.StatusForbidden {
		t.Fatalf("member PATCH users: %d", code)
	}

	// Change own password.
	if code, b := call(t, "POST", ts.URL+"/api/auth/password", devSess, `{"current_password":"bad","new_password":"newpass1"}`); code != http.StatusBadRequest {
		t.Fatalf("wrong current password: %d %s", code, b)
	}
	if code, b := call(t, "POST", ts.URL+"/api/auth/password", devSess, `{"current_password":"devpass1","new_password":"newpass1"}`); code != http.StatusOK {
		t.Fatalf("change password: %d %s", code, b)
	}
	if code, _ := call(t, "GET", ts.URL+"/api/projects", devSess, ""); code != http.StatusOK {
		t.Fatalf("current session should survive password change: %d", code)
	}

	// Admin disables the member: their session stops working at once.
	if code, b := call(t, "PATCH", ts.URL+"/api/users/"+dev.ID, admin, `{"disabled":true}`); code != http.StatusOK || !strings.Contains(b, "disabled_at") {
		t.Fatalf("disable: %d %s", code, b)
	}
	if code, _ := call(t, "GET", ts.URL+"/api/projects", devSess, ""); code != http.StatusUnauthorized {
		t.Fatalf("disabled user still authorized: %d", code)
	}
	if code, _ := call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"dev@velo.dev","password":"newpass1"}`); code != http.StatusUnauthorized {
		t.Fatalf("disabled user can log in: %d", code)
	}

	// Admin resets password, enables, then deletes (no history yet).
	if code, _ := call(t, "POST", ts.URL+"/api/users/"+dev.ID+"/password", admin, `{"password":"fresh123"}`); code != http.StatusOK {
		t.Fatalf("reset password: %d", code)
	}
	if code, _ := call(t, "PATCH", ts.URL+"/api/users/"+dev.ID, admin, `{"disabled":false}`); code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}
	if code, _ := call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"dev@velo.dev","password":"fresh123"}`); code != http.StatusOK {
		t.Fatalf("login after reset: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/users/"+dev.ID, admin, ""); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(t, "DELETE", ts.URL+"/api/users/"+dev.ID, admin, ""); code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", code)
	}
}
