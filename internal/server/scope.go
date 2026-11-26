package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/parabol123654/velo/internal/db"
)

// API token scopes (see SPEC.md, "Accounts and access").
//
// Every authenticated route declares a tokenRule in routes(), so a new route
// cannot forget how a project-restricted token applies to it. Read-only
// tokens are handled once in requireAuth: they may only use GET.

const tokenCtxKey contextKey = 2

// currentToken returns the API token behind the request, or nil for a browser session.
func currentToken(r *http.Request) *db.APIToken {
	t, _ := r.Context().Value(tokenCtxKey).(*db.APIToken)
	return t
}

// tokenRule rejects a request a token may not make. nil means the route has
// no project data or its handler filters results itself.
type tokenRule func(r *http.Request) error

var filtered tokenRule

func forbidden(msg string) error { return fmt.Errorf("%w: %s", db.ErrForbidden, msg) }

// sessionOnly: token management, password changes. A token must never mint or
// widen other credentials.
func sessionOnly(r *http.Request) error {
	if currentToken(r) != nil {
		return forbidden("API tokens cannot be used for this; sign in instead")
	}
	return nil
}

// allProjects: actions that are not tied to one project (create, import).
func allProjects(r *http.Request) error {
	if t := currentToken(r); t != nil && t.Restricted() {
		return forbidden("this token is limited to some projects")
	}
	return nil
}

// checkProject fails when the request's token may not touch projectID.
func checkProject(r *http.Request, projectID string) error {
	if t := currentToken(r); t != nil && !t.AllowsProject(projectID) {
		return forbidden("this token has no access to that project")
	}
	return nil
}

// allowedProjects returns the project IDs a restricted token may see, or nil
// when the caller is not restricted.
func allowedProjects(r *http.Request) []string {
	if t := currentToken(r); t != nil && t.Restricted() {
		return t.ProjectIDs
	}
	return nil
}

// inProject checks the project of the object named in the URL. kind is
// "project" or "issue" (path value {ref}), or a db.ProjectIDOf kind (path
// value {id}). A missing object passes so the handler can answer 404.
func (s *Server) inProject(kind string) tokenRule {
	return func(r *http.Request) error {
		t := currentToken(r)
		if t == nil || !t.Restricted() {
			return nil
		}
		var projectID string
		var err error
		switch kind {
		case "project":
			var p *db.Project
			if p, err = s.store.GetProject(r.PathValue("ref")); err == nil {
				projectID = p.ID
			}
		case "issue":
			var iss *db.Issue
			if iss, err = s.store.GetIssue(r.PathValue("ref")); err == nil {
				projectID = iss.ProjectID
			}
		default:
			projectID, err = s.store.ProjectIDOf(kind, r.PathValue("id"))
		}
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if projectID == "" { // attachment not linked to an issue
			return nil
		}
		return checkProject(r, projectID)
	}
}

// storeFor returns the store that records the request's API token name on
// comments and activities.
func (s *Server) storeFor(r *http.Request) *db.Store {
	if t := currentToken(r); t != nil {
		return s.store.Via(t.Name)
	}
	return s.store
}
