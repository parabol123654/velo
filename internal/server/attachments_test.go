package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// upload posts a small PNG and returns its Markdown and its file on disk.
func upload(t *testing.T, ts string, srv *Server, auth, issueID string) (markdown, url, file string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if issueID != "" {
		_ = mw.WriteField("issue_id", issueID)
	}
	fw, _ := mw.CreateFormFile("file", "shot.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts+"/api/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Markdown   string        `json:"markdown"`
		Attachment db.Attachment `json:"attachment"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %v", resp.StatusCode, err)
	}
	att, err := srv.store.GetAttachment(out.Attachment.ID) // storage name is not in the JSON
	if err != nil {
		t.Fatal(err)
	}
	return out.Markdown, out.Attachment.URL, filepath.Join(srv.uploadsDir, att.StorageName)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// Known gap 9: attachments follow their issue, and an attachment that no
// issue owns is not served to other users or restricted tokens.
func TestAttachmentsFollowTheirIssue(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", ts.URL+"/api/users", admin, `{"email":"member@velo.dev","name":"Member","password":"password123"}`)
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"member@velo.dev","password":"password123"}`)
	member := sessionOf(t, body)
	users, _ := srv.store.ListUsers()
	var adminID string
	for _, u := range users {
		if u.Role == "admin" {
			adminID = u.ID
		}
	}
	opsTok, _, err := srv.store.CreateAPIToken(adminID, "ops only", db.TokenScope{Projects: []string{"OPS"}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(url, auth string) int { c, _ := call(t, "GET", ts.URL+url, auth, ""); return c }

	// Pasted into the new-issue form: uploaded before the issue exists.
	md, url, file := upload(t, ts.URL, srv, admin, "")
	if get(url, admin) != http.StatusOK {
		t.Fatal("uploader cannot see their own unlinked upload")
	}
	if c := get(url, member); c != http.StatusNotFound {
		t.Fatalf("another user sees an unlinked upload: %d", c)
	}
	if c := get(url, opsTok); c != http.StatusNotFound {
		t.Fatalf("a restricted token sees an unlinked upload: %d", c)
	}
	// Saving the issue links the upload to it; project rules apply from then on.
	descJSON, _ := json.Marshal("Steps:\n\n" + md)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"with shot","description":`+string(descJSON)+`}`)
	if get(url, member) != http.StatusOK {
		t.Fatal("member cannot see an upload linked to an issue")
	}
	if c := get(url, opsTok); c == http.StatusOK {
		t.Fatal("OPS-only token sees a DEV upload")
	}

	// Deleting the issue deletes the upload and its file.
	if c, b := call(t, "DELETE", ts.URL+"/api/issues/DEV-1", admin, ""); c != http.StatusOK {
		t.Fatalf("delete issue: %d %s", c, b)
	}
	if exists(file) || get(url, admin) != http.StatusNotFound {
		t.Fatalf("upload of a deleted issue survives: file=%v get=%d", exists(file), get(url, admin))
	}

	// An upload still used by another issue moves to that issue instead.
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"owner"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"quoter"}`)
	md, url, file = upload(t, ts.URL, srv, admin, idOfIssue(t, ts.URL, admin, "DEV-2"))
	commentJSON, _ := json.Marshal("same as " + md)
	call(t, "POST", ts.URL+"/api/issues/DEV-3/comments", admin, `{"body":`+string(commentJSON)+`}`)
	call(t, "DELETE", ts.URL+"/api/issues/DEV-2", admin, "")
	if !exists(file) || get(url, member) != http.StatusOK {
		t.Fatal("upload still quoted by DEV-3 was removed")
	}
	call(t, "DELETE", ts.URL+"/api/issues/DEV-3", admin, "")
	if exists(file) {
		t.Fatal("upload survives once nothing uses it")
	}

	// Deleting a project deletes its uploads; other projects keep theirs.
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"OPS","title":"ops"}`)
	call(t, "POST", ts.URL+"/api/issues", admin, `{"project_ref":"DEV","title":"keep"}`)
	_, _, opsFile := upload(t, ts.URL, srv, admin, idOfIssue(t, ts.URL, admin, "OPS-1"))
	_, _, devFile := upload(t, ts.URL, srv, admin, idOfIssue(t, ts.URL, admin, "DEV-4"))
	if c, b := call(t, "DELETE", ts.URL+"/api/projects/OPS", admin, ""); c != http.StatusOK {
		t.Fatalf("delete project: %d %s", c, b)
	}
	if exists(opsFile) || !exists(devFile) {
		t.Fatalf("after deleting OPS: ops file kept=%v, dev file kept=%v", exists(opsFile), exists(devFile))
	}
}

func idOfIssue(t *testing.T, ts, auth, ref string) string {
	t.Helper()
	_, b := call(t, "GET", ts+"/api/issues/"+ref, auth, "")
	return idOf(b)
}

// Linking is limited to the uploader, and an upload never moves to another
// project when its issue is deleted.
func TestAttachmentLinkingCannotGrantAccess(t *testing.T) {
	ts, srv := newTestServer(t)
	_, body := call(t, "POST", ts.URL+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	admin := sessionOf(t, body)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", ts.URL+"/api/projects", admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", ts.URL+"/api/users", admin, `{"email":"member@velo.dev","name":"Member","password":"password123"}`)
	_, body = call(t, "POST", ts.URL+"/api/auth/login", "", `{"email":"member@velo.dev","password":"password123"}`)
	member := sessionOf(t, body)
	var memberID string
	users, _ := srv.store.ListUsers()
	for _, u := range users {
		if u.Role == "member" {
			memberID = u.ID
		}
	}
	opsTok, _, err := srv.store.CreateAPIToken(memberID, "member ops only", db.TokenScope{Projects: []string{"OPS"}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(url, auth string) int { c, _ := call(t, "GET", ts.URL+url, auth, ""); return c }
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }

	// The member quotes the admin's unlinked upload in their own issue,
	// in a new description, a PATCH, a comment and a comment edit.
	adminMD, adminURL, _ := upload(t, ts.URL, srv, admin, "")
	call(t, "POST", ts.URL+"/api/issues", member, `{"project_ref":"DEV","title":"claim","description":`+quote(adminMD)+`}`)
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", member, `{"description":`+quote("again "+adminMD)+`}`)
	_, b := call(t, "POST", ts.URL+"/api/issues/DEV-1/comments", member, `{"body":"x"}`)
	call(t, "PATCH", ts.URL+"/api/comments/"+idOf(b), member, `{"body":`+quote(adminMD)+`}`)
	if c := get(adminURL, member); c != http.StatusNotFound {
		t.Fatalf("member claimed the admin's upload by quoting it: %d", c)
	}
	// The uploader's own PATCH and comment edit do link.
	ownMD, ownURL, _ := upload(t, ts.URL, srv, member, "")
	call(t, "PATCH", ts.URL+"/api/issues/DEV-1", member, `{"description":`+quote(ownMD)+`}`)
	if linkedIssue(t, srv, ownURL) == "" {
		t.Fatal("PATCH by the uploader did not link the upload")
	}
	ownMD2, ownURL2, _ := upload(t, ts.URL, srv, member, "")
	call(t, "PATCH", ts.URL+"/api/comments/"+idOf(b), member, `{"body":`+quote(ownMD2)+`}`)
	if linkedIssue(t, srv, ownURL2) == "" {
		t.Fatal("comment edit by the uploader did not link the upload")
	}

	// A linked upload stays with its issue when quoted elsewhere.
	call(t, "POST", ts.URL+"/api/issues", member, `{"project_ref":"OPS","title":"ops","description":`+quote(ownMD)+`}`)
	if c := get(ownURL, opsTok); c == http.StatusOK {
		t.Fatal("quoting a DEV upload in OPS gave OPS access")
	}
	// Deleting its issue deletes it even though OPS quotes it: it never moves
	// to another project.
	call(t, "DELETE", ts.URL+"/api/issues/DEV-1", member, "")
	if c := get(ownURL, opsTok); c == http.StatusOK {
		t.Fatalf("DEV upload moved to OPS on delete: %d", c)
	}
	if c := get(ownURL, member); c != http.StatusNotFound {
		t.Fatalf("DEV upload quoted only from OPS survives: %d", c)
	}
}

// linkedIssue returns the issue an upload URL (/api/attachments/<id>/name)
// belongs to, or "".
func linkedIssue(t *testing.T, srv *Server, url string) string {
	t.Helper()
	id := strings.Split(strings.TrimPrefix(url, "/api/attachments/"), "/")[0]
	att, err := srv.store.GetAttachment(id)
	if err != nil {
		t.Fatal(err)
	}
	return att.IssueID
}
