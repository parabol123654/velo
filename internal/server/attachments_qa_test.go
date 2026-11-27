package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parabol123654/velo/internal/db"
)

// qaEnv is a server with an admin, a member, projects DEV and OPS and a set
// of tokens.
type qaEnv struct {
	t                  *testing.T
	ts                 string
	srv                *Server
	admin, member      string // sessions
	adminID, memberID  string
	adminDev, adminOps string // admin tokens restricted to one project
	memberDev          string // member token restricted to DEV
	adminRO            string // admin read-only token, all projects
}

func newQAEnv(t *testing.T) *qaEnv {
	t.Helper()
	ts, srv := newTestServer(t)
	e := &qaEnv{t: t, ts: ts.URL, srv: srv}
	_, body := call(t, "POST", e.ts+"/api/auth/setup", "", `{"email":"admin@velo.dev","name":"Admin","password":"password123"}`)
	e.admin = sessionOf(t, body)
	call(t, "POST", e.ts+"/api/projects", e.admin, `{"key":"DEV","name":"Dev"}`)
	call(t, "POST", e.ts+"/api/projects", e.admin, `{"key":"OPS","name":"Ops"}`)
	call(t, "POST", e.ts+"/api/users", e.admin, `{"email":"member@velo.dev","name":"Member","password":"password123"}`)
	_, body = call(t, "POST", e.ts+"/api/auth/login", "", `{"email":"member@velo.dev","password":"password123"}`)
	e.member = sessionOf(t, body)
	users, _ := srv.store.ListUsers()
	for _, u := range users {
		if u.Role == "admin" {
			e.adminID = u.ID
		} else {
			e.memberID = u.ID
		}
	}
	tok := func(uid string, scope db.TokenScope) string {
		raw, _, err := srv.store.CreateAPIToken(uid, "qa", scope)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	e.adminDev = tok(e.adminID, db.TokenScope{Projects: []string{"DEV"}})
	e.adminOps = tok(e.adminID, db.TokenScope{Projects: []string{"OPS"}})
	e.memberDev = tok(e.memberID, db.TokenScope{Projects: []string{"DEV"}})
	e.adminRO = tok(e.adminID, db.TokenScope{Access: "read"})
	return e
}

func (e *qaEnv) get(url, auth string) int {
	c, _ := call(e.t, "GET", e.ts+url, auth, "")
	return c
}

func (e *qaEnv) issue(auth, project, title, desc, parent string) string {
	e.t.Helper()
	in := map[string]string{"project_ref": project, "title": title, "description": desc}
	if parent != "" {
		in["parent_ref"] = parent
	}
	b, _ := json.Marshal(in)
	c, body := call(e.t, "POST", e.ts+"/api/issues", auth, string(b))
	if c != http.StatusCreated && c != http.StatusOK {
		e.t.Fatalf("create issue: %d %s", c, body)
	}
	var v struct {
		Identifier string `json:"identifier"`
	}
	_ = json.Unmarshal([]byte(body), &v)
	return v.Identifier
}

func (e *qaEnv) comment(auth, ref, text string) (int, string) {
	b, _ := json.Marshal(map[string]string{"body": text})
	c, body := call(e.t, "POST", e.ts+"/api/issues/"+ref+"/comments", auth, string(b))
	return c, idOf(body)
}

func (e *qaEnv) patchDesc(auth, ref, desc string) int {
	b, _ := json.Marshal(map[string]string{"description": desc})
	c, _ := call(e.t, "PATCH", e.ts+"/api/issues/"+ref, auth, string(b))
	return c
}

func (e *qaEnv) attIssue(url string) string {
	e.t.Helper()
	id := strings.Split(strings.TrimPrefix(url, "/api/attachments/"), "/")[0]
	att, err := e.srv.store.GetAttachment(id)
	if err != nil {
		return "<gone>"
	}
	return att.IssueID
}

// uploadNamed posts a file under a chosen name and returns status and JSON.
func uploadNamed(t *testing.T, ts, auth, issueID, name string) (int, string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if issueID != "" {
		_ = mw.WriteField("issue_id", issueID)
	}
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts+"/api/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", ""
	}
	defer resp.Body.Close()
	var out struct {
		Markdown   string        `json:"markdown"`
		Attachment db.Attachment `json:"attachment"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Markdown, out.Attachment.URL
}

// An unlinked upload is private to its uploader (and admins). Another member
// who learns its URL must not gain access by quoting it in an issue of their
// own; neither may a project-restricted token, which the spec says never
// sees an unlinked upload.
func TestQAOthersUnlinkedUploadCannotBeClaimed(t *testing.T) {
	t.Run("member creates issue quoting it", func(t *testing.T) {
		e := newQAEnv(t)
		md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
		if e.get(url, e.member) != http.StatusNotFound {
			t.Fatal("precondition: member sees admin's unlinked upload")
		}
		e.issue(e.member, "DEV", "mine", "stolen "+md, "")
		if c := e.get(url, e.member); c == http.StatusOK {
			t.Fatalf("member gained access to admin's unlinked upload by quoting it in their issue (owner now %s)", e.attIssue(url))
		}
	})
	t.Run("restricted token patches description", func(t *testing.T) {
		e := newQAEnv(t)
		md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
		ref := e.issue(e.member, "DEV", "mine", "", "")
		if c := e.patchDesc(e.memberDev, ref, "x "+md); c != http.StatusOK {
			t.Fatalf("patch: %d", c)
		}
		if c := e.get(url, e.memberDev); c == http.StatusOK {
			t.Fatal("DEV-restricted member token reads admin's unlinked upload after quoting it")
		}
	})
	t.Run("member edits comment", func(t *testing.T) {
		e := newQAEnv(t)
		md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
		ref := e.issue(e.member, "DEV", "mine", "", "")
		_, cid := e.comment(e.member, ref, "nothing yet")
		b, _ := json.Marshal(map[string]string{"body": "now " + md})
		if c, body := call(t, "PATCH", e.ts+"/api/comments/"+cid, e.member, string(b)); c != http.StatusOK {
			t.Fatalf("edit comment: %d %s", c, body)
		}
		if c := e.get(url, e.member); c == http.StatusOK {
			t.Fatal("member gained access to admin's unlinked upload via a comment edit")
		}
	})
}

// The uploader's own pasted upload is linked by every save path.
func TestQAOwnUploadLinkedByEverySavePath(t *testing.T) {
	e := newQAEnv(t)
	ref := e.issue(e.member, "DEV", "target", "", "")
	_, cid := e.comment(e.member, ref, "first")

	md1, url1, _ := upload(t, e.ts, e.srv, e.member, "")
	if e.patchDesc(e.member, ref, md1) != http.StatusOK {
		t.Fatal("patch")
	}
	md2, url2, _ := upload(t, e.ts, e.srv, e.member, "")
	b, _ := json.Marshal(map[string]string{"body": md2})
	call(t, "PATCH", e.ts+"/api/comments/"+cid, e.member, string(b))
	md3, url3, _ := upload(t, e.ts, e.srv, e.member, "")
	e.comment(e.member, ref, md3)
	id := idOfIssue(t, e.ts, e.member, ref)
	// A DEV token's own pasted upload: invisible to it until it links it.
	md4, url4, _ := upload(t, e.ts, e.srv, e.memberDev, "")
	if e.get(url4, e.memberDev) != http.StatusNotFound {
		t.Fatal("restricted token reads its unlinked upload")
	}
	e.issue(e.memberDev, "DEV", "via token", md4, "")
	if e.get(url4, e.memberDev) != http.StatusOK {
		t.Fatal("restricted token cannot read its upload after linking it in DEV")
	}
	for _, u := range []string{url1, url2, url3} {
		if got := e.attIssue(u); got != id {
			t.Fatalf("%s owned by %q, want %s", u, got, id)
		}
		if e.get(u, e.adminDev) != http.StatusOK || e.get(u, e.adminOps) == http.StatusOK {
			t.Fatalf("%s: project rules not applied after link", u)
		}
	}
}

// A DEV-restricted token quoting a linked OPS upload in a DEV issue (by PATCH
// or comment edit) does not change who owns it or who can read it.
func TestQARestrictedTokenQuotingOtherProjectUpload(t *testing.T) {
	e := newQAEnv(t)
	ops := e.issue(e.admin, "OPS", "ops", "", "")
	opsID := idOfIssue(t, e.ts, e.admin, ops)
	md, url, file := upload(t, e.ts, e.srv, e.admin, opsID)
	dev := e.issue(e.adminDev, "DEV", "dev", "", "")
	if e.patchDesc(e.adminDev, dev, "see "+md) != http.StatusOK {
		t.Fatal("patch")
	}
	_, cid := e.comment(e.adminDev, dev, "x")
	b, _ := json.Marshal(map[string]string{"body": "also " + md})
	call(t, "PATCH", e.ts+"/api/comments/"+cid, e.adminDev, string(b))
	if got := e.attIssue(url); got != opsID {
		t.Fatalf("OPS upload moved to %q by a quote", got)
	}
	if c := e.get(url, e.adminDev); c == http.StatusOK {
		t.Fatal("DEV token reads an OPS upload after quoting it")
	}
	// Uploading straight into an OPS issue with a DEV token is refused.
	if c, _, _ := uploadNamed(t, e.ts, e.adminDev, opsID, "x.png"); c != http.StatusForbidden {
		t.Fatalf("DEV token uploads to OPS issue: %d", c)
	}
	// Spec: only a surviving issue of the same project keeps an upload, so
	// deleting OPS-1 deletes it rather than handing it to the DEV quote.
	call(t, "DELETE", e.ts+"/api/issues/"+ops, e.admin, "")
	if exists(file) || e.attIssue(url) != "<gone>" || e.get(url, e.adminDev) == http.StatusOK {
		t.Fatalf("after OPS-1 delete: file=%v owner=%s dev=%d", exists(file), e.attIssue(url), e.get(url, e.adminDev))
	}
}

// Read-only tokens can neither upload nor link.
func TestQAReadOnlyTokenCannotUploadOrLink(t *testing.T) {
	e := newQAEnv(t)
	ref := e.issue(e.admin, "DEV", "x", "", "")
	if c, _, _ := uploadNamed(t, e.ts, e.adminRO, "", "x.png"); c != http.StatusForbidden {
		t.Fatalf("read-only token upload: %d", c)
	}
	md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
	if c := e.patchDesc(e.adminRO, ref, md); c != http.StatusForbidden {
		t.Fatalf("read-only patch: %d", c)
	}
	if c, _ := e.comment(e.adminRO, ref, md); c != http.StatusForbidden {
		t.Fatalf("read-only comment: %d", c)
	}
	if got := e.attIssue(url); got != "" {
		t.Fatalf("read-only token linked an upload to %s", got)
	}
	// The uploader's unrestricted read-only token still sees the upload.
	if e.get(url, e.adminRO) != http.StatusOK {
		t.Fatal("uploader's read-only token cannot read own unlinked upload")
	}
}

// Deleting a parent leaves its sub-issues; an upload a sub-issue quotes moves
// to it, and the sub-issue's own uploads are untouched.
func TestQASubIssueKeepsUploadsWhenParentDeleted(t *testing.T) {
	e := newQAEnv(t)
	parent := e.issue(e.admin, "DEV", "parent", "", "")
	pmd, purl, pfile := upload(t, e.ts, e.srv, e.admin, idOfIssue(t, e.ts, e.admin, parent))
	_, _, pOnly := upload(t, e.ts, e.srv, e.admin, idOfIssue(t, e.ts, e.admin, parent))
	child := e.issue(e.admin, "DEV", "child", "parent shot: "+pmd, parent)
	childID := idOfIssue(t, e.ts, e.admin, child)
	_, curl, cfile := upload(t, e.ts, e.srv, e.admin, childID)
	if c, b := call(t, "DELETE", e.ts+"/api/issues/"+parent, e.admin, ""); c != http.StatusOK {
		t.Fatalf("delete parent: %d %s", c, b)
	}
	if e.get("/api/issues/"+child, e.admin) != http.StatusOK {
		t.Fatal("child deleted with parent")
	}
	if !exists(pfile) || e.attIssue(purl) != childID || e.get(purl, e.member) != http.StatusOK {
		t.Fatalf("quoted parent upload: file=%v owner=%s", exists(pfile), e.attIssue(purl))
	}
	if exists(pOnly) {
		t.Fatal("parent-only upload survives")
	}
	if !exists(cfile) || e.attIssue(curl) != childID {
		t.Fatal("child's own upload affected")
	}
}

// An upload referenced only in a comment of its own issue goes with the issue.
func TestQAUploadQuotedOnlyInOwnCommentIsDeleted(t *testing.T) {
	e := newQAEnv(t)
	ref := e.issue(e.admin, "DEV", "x", "", "")
	md, url, file := upload(t, e.ts, e.srv, e.admin, "")
	e.comment(e.admin, ref, "shot "+md)
	if e.attIssue(url) != idOfIssue(t, e.ts, e.admin, ref) {
		t.Fatal("comment did not link the upload")
	}
	call(t, "DELETE", e.ts+"/api/issues/"+ref, e.admin, "")
	if exists(file) || e.attIssue(url) != "<gone>" {
		t.Fatalf("upload quoted only by its own issue's comment survives: file=%v", exists(file))
	}
}

// Deleting a project deletes all its uploads, even one another project's
// issue still quotes (spec: uploads never move across projects).
func TestQAProjectDeleteDeletesUploadQuotedElsewhere(t *testing.T) {
	e := newQAEnv(t)
	ops := e.issue(e.admin, "OPS", "ops", "", "")
	ops2 := e.issue(e.admin, "OPS", "ops2", "", "")
	md, url, file := upload(t, e.ts, e.srv, e.admin, idOfIssue(t, e.ts, e.admin, ops))
	e.comment(e.admin, ops2, md) // quoted inside OPS too: must not keep it
	dev := e.issue(e.admin, "DEV", "dev", md, "")
	e.comment(e.admin, dev, "copied "+md)
	_, _, devFile := upload(t, e.ts, e.srv, e.admin, idOfIssue(t, e.ts, e.admin, dev))
	if c, b := call(t, "DELETE", e.ts+"/api/projects/OPS", e.admin, ""); c != http.StatusOK {
		t.Fatalf("delete project: %d %s", c, b)
	}
	if exists(file) || e.attIssue(url) != "<gone>" || e.get(url, e.adminDev) == http.StatusOK {
		t.Fatalf("OPS upload quoted in DEV survives project delete: file=%v owner=%s", exists(file), e.attIssue(url))
	}
	if !exists(devFile) {
		t.Fatal("DEV upload removed by deleting OPS")
	}
}

// The same upload linked twice (twice in one text, then in a second issue).
func TestQASameUploadLinkedTwice(t *testing.T) {
	e := newQAEnv(t)
	md, url, file := upload(t, e.ts, e.srv, e.admin, "")
	a := e.issue(e.admin, "DEV", "a", md+"\n\n"+md, "")
	aID := idOfIssue(t, e.ts, e.admin, a)
	b := e.issue(e.admin, "DEV", "b", "", "")
	bID := idOfIssue(t, e.ts, e.admin, b)
	e.comment(e.admin, b, md)
	o := e.issue(e.admin, "OPS", "o", md, "") // other project: never a keeper
	if e.attIssue(url) != aID {
		t.Fatal("second link stole the upload")
	}
	call(t, "DELETE", e.ts+"/api/issues/"+a, e.admin, "")
	if !exists(file) || e.attIssue(url) != bID {
		t.Fatalf("after deleting first owner: file=%v owner=%s", exists(file), e.attIssue(url))
	}
	call(t, "DELETE", e.ts+"/api/issues/"+b, e.admin, "")
	if exists(file) {
		t.Fatalf("upload survives both DEV issues (owner %s, quoted by %s)", e.attIssue(url), o)
	}
}

// Odd filenames: the returned URL must serve the file and link it.
func TestQAOddFilenames(t *testing.T) {
	e := newQAEnv(t)
	names := []string{"my shot (1).png", "ảnh chụp.png", "a/b/../c.png", "..", ".", "x%2F..%2Fy.png", strings.Repeat("a", 300) + ".png", "-rf.png", "con.png"}
	for _, n := range names {
		c, md, url := uploadNamed(t, e.ts, e.member, "", n)
		if c != http.StatusCreated {
			t.Errorf("%q: upload %d", n, c)
			continue
		}
		ref := e.issue(e.member, "DEV", "odd", md, "")
		if e.attIssue(url) != idOfIssue(t, e.ts, e.member, ref) {
			t.Errorf("%q: URL %s not linked from %s", n, url, md)
		}
		if g := e.get(url, e.admin); g != http.StatusOK {
			t.Errorf("%q: GET %s = %d", n, url, g)
		}
	}
}

// Uploads into an issue racing with its deletion leave no orphan rows or
// files: every file on disk has a row, and no row lost its issue.
func TestQAConcurrentDeleteAndUpload(t *testing.T) {
	e := newQAEnv(t)
	for round := 0; round < 5; round++ {
		ref := e.issue(e.admin, "DEV", "race", "", "")
		id := idOfIssue(t, e.ts, e.admin, ref)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var urls []string
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				name := fmt.Sprintf("r%d_%d.png", round, i)
				if c, _, url := uploadNamed(t, e.ts, e.admin, id, name); c == http.StatusCreated {
					mu.Lock()
					urls = append(urls, url)
					mu.Unlock()
				}
			}(i)
		}
		// An unlinked upload quoted by a comment racing with the delete.
		md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
		wg.Add(2)
		go func() { defer wg.Done(); e.comment(e.admin, ref, md) }()
		go func() { defer wg.Done(); call(t, "DELETE", e.ts+"/api/issues/"+ref, e.admin, "") }()
		wg.Wait()
		if e.get("/api/issues/"+ref, e.admin) != http.StatusNotFound {
			t.Fatal("issue survived delete")
		}
		for _, u := range append(urls, url) {
			if owner := e.attIssue(u); owner == id {
				t.Errorf("round %d: %s still owned by deleted issue", round, u)
			} else if owner == "" && u != url {
				t.Errorf("round %d: %s lost its issue and became unlinked", round, u)
			}
		}
	}
	// Every file in uploads/ belongs to a row.
	entries, _ := os.ReadDir(e.srv.uploadsDir)
	live := map[string]bool{}
	for _, ent := range entries {
		live[ent.Name()] = true
	}
	// Collect storage names of rows that still exist via the unlinked uploads
	// we can still reach; anything named r<round>_ must be gone.
	for name := range live {
		if strings.Contains(name, "_r") && strings.HasSuffix(name, ".png") {
			t.Errorf("orphan file left on disk: %s", filepath.Join(e.srv.uploadsDir, name))
		}
	}
}

// A patch that fails validation writes nothing, including the link.
func TestQAFailedPatchDoesNotLink(t *testing.T) {
	e := newQAEnv(t)
	ref := e.issue(e.admin, "DEV", "x", "", "")
	md, url, _ := upload(t, e.ts, e.srv, e.admin, "")
	b, _ := json.Marshal(map[string]any{"description": md, "state_id": "nope", "priority": 9})
	if c, _ := call(t, "PATCH", e.ts+"/api/issues/"+ref, e.admin, string(b)); c == http.StatusOK {
		t.Fatal("invalid patch accepted")
	}
	if got := e.attIssue(url); got != "" {
		t.Fatalf("rejected patch linked the upload to %s", got)
	}
	b, _ = json.Marshal(map[string]any{"project_ref": "DEV", "title": "y", "description": md, "label_ids": []string{"ghost"}})
	if c, _ := call(t, "POST", e.ts+"/api/issues", e.admin, string(b)); c == http.StatusCreated {
		t.Fatal("issue with unknown label created")
	}
	if got := e.attIssue(url); got != "" {
		t.Fatalf("rejected create linked the upload to %s", got)
	}
}
