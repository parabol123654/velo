package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"crypto/rand"

	"github.com/parabol123654/velo/internal/db"
)

const (
	sessionCookieName = "velo_session"
	sessionTTL        = 30 * 24 * time.Hour
	maxUploadSize     = 20 << 20 // 20 MB
)

type contextKey int

const userCtxKey contextKey = 1

type Server struct {
	store      *db.Store
	uploadsDir string
	hub        *SSEHub
	staticFS   fs.FS
	mux        *http.ServeMux
	logins     *loginLimiter

	// GitHubWebhookSecret enables POST /api/webhooks/github when non-empty.
	GitHubWebhookSecret string
}

func New(store *db.Store, dataDir string, staticFS fs.FS) (*Server, error) {
	uploadsDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	s := &Server{
		store:      store,
		uploadsDir: uploadsDir,
		hub:        NewSSEHub(),
		staticFS:   staticFS,
		mux:        http.NewServeMux(),
		logins:     newLoginLimiter(),
	}
	s.routes()
	return s, nil
}

func (s *Server) Hub() *SSEHub {
	return s.hub
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func currentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(userCtxKey).(*db.User)
	return u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, db.ErrValidation):
		status = http.StatusBadRequest
	case errors.Is(err, db.ErrUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, db.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, db.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, db.ErrForbidden):
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: invalid JSON body", db.ErrValidation)
	}
	return nil
}

// authenticateRequest returns the caller and, when an API token was used, the token.
func (s *Server) authenticateRequest(r *http.Request) (*db.User, *db.APIToken, error) {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[7:])
		if strings.HasPrefix(tok, "vs_") {
			u, err := s.store.GetUserBySession(tok)
			return u, nil, err
		}
		return s.store.GetUserByAPIToken(tok)
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		u, err := s.store.GetUserBySession(c.Value)
		return u, nil, err
	}
	return nil, nil, db.ErrUnauthorized
}

// requireAuth authenticates the request and applies API token limits: a
// read-only token may only use GET, and rule (see scope.go) covers projects.
func (s *Server) requireAuth(rule tokenRule, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, tok, err := s.authenticateRequest(r)
		if err != nil {
			writeErr(w, db.ErrUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		if tok != nil {
			ctx = context.WithValue(ctx, tokenCtxKey, tok)
		}
		r = r.WithContext(ctx)
		if tok != nil && tok.Access == "read" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, forbidden("this token is read-only"))
			return
		}
		if rule != nil {
			if err := rule(r); err != nil {
				writeErr(w, err)
				return
			}
		}
		next(w, r)
	}
}

// requireAdmin allows signed-in admins only; API tokens never reach admin actions.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(sessionOnly, func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r).Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		next(w, r)
	})
}

func setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(time.Until(exp).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) routes() {
	// Health & Auth
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("POST /api/auth/accept-invite", s.handleAcceptInvite)
	s.mux.HandleFunc("POST /api/webhooks/github", s.handleGitHubWebhook)

	// SSE live stream
	s.mux.HandleFunc("GET /api/events", s.requireAuth(filtered, s.hub.ServeHTTP))

	// Users, Tokens & Invites
	s.mux.HandleFunc("GET /api/users", s.requireAuth(filtered, s.handleListUsers))
	s.mux.HandleFunc("POST /api/users", s.requireAdmin(s.handleCreateUser))
	s.mux.HandleFunc("PATCH /api/users/{id}", s.requireAdmin(s.handleUpdateUser))
	s.mux.HandleFunc("DELETE /api/users/{id}", s.requireAdmin(s.handleDeleteUser))
	s.mux.HandleFunc("POST /api/users/{id}/password", s.requireAdmin(s.handleResetPassword))
	s.mux.HandleFunc("POST /api/auth/password", s.requireAuth(sessionOnly, s.handleChangePassword))
	s.mux.HandleFunc("GET /api/tokens", s.requireAuth(sessionOnly, s.handleListTokens))
	s.mux.HandleFunc("POST /api/tokens", s.requireAuth(sessionOnly, s.handleCreateToken))
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.requireAuth(sessionOnly, s.handleDeleteToken))
	s.mux.HandleFunc("GET /api/invites", s.requireAdmin(s.handleListInvites))
	s.mux.HandleFunc("POST /api/invites", s.requireAdmin(s.handleCreateInvite))
	s.mux.HandleFunc("DELETE /api/invites/{id}", s.requireAdmin(s.handleDeleteInvite))

	// Projects, States, Labels, Cycles & Export/Import
	s.mux.HandleFunc("GET /api/projects", s.requireAuth(filtered, s.handleListProjects))
	s.mux.HandleFunc("POST /api/projects", s.requireAuth(allProjects, s.handleCreateProject))
	s.mux.HandleFunc("GET /api/projects/{ref}", s.requireAuth(s.inProject("project"), s.handleGetProject))
	s.mux.HandleFunc("PATCH /api/projects/{ref}", s.requireAuth(s.inProject("project"), s.handleUpdateProject))
	s.mux.HandleFunc("DELETE /api/projects/{ref}", s.requireAdmin(s.handleDeleteProject))
	s.mux.HandleFunc("GET /api/projects/{ref}/states", s.requireAuth(s.inProject("project"), s.handleListStates))
	s.mux.HandleFunc("POST /api/projects/{ref}/states", s.requireAuth(s.inProject("project"), s.handleCreateState))
	s.mux.HandleFunc("PATCH /api/states/{id}", s.requireAuth(s.inProject("state"), s.handleUpdateState))
	s.mux.HandleFunc("GET /api/projects/{ref}/labels", s.requireAuth(s.inProject("project"), s.handleListLabels))
	s.mux.HandleFunc("POST /api/projects/{ref}/labels", s.requireAuth(s.inProject("project"), s.handleCreateLabel))
	s.mux.HandleFunc("DELETE /api/labels/{id}", s.requireAuth(s.inProject("label"), s.handleDeleteLabel))
	s.mux.HandleFunc("GET /api/projects/{ref}/cycles", s.requireAuth(s.inProject("project"), s.handleListCycles))
	s.mux.HandleFunc("POST /api/projects/{ref}/cycles", s.requireAuth(s.inProject("project"), s.handleCreateCycle))
	s.mux.HandleFunc("PATCH /api/cycles/{id}", s.requireAuth(s.inProject("cycle"), s.handleUpdateCycle))
	s.mux.HandleFunc("DELETE /api/cycles/{id}", s.requireAuth(s.inProject("cycle"), s.handleDeleteCycle))
	s.mux.HandleFunc("GET /api/projects/{ref}/export", s.requireAuth(s.inProject("project"), s.handleExportProject))
	s.mux.HandleFunc("POST /api/projects/import", s.requireAuth(allProjects, s.handleImportProject))

	// Issues, Search, Comments, Activities & Attachments
	s.mux.HandleFunc("GET /api/search", s.requireAuth(filtered, s.handleSearch))
	s.mux.HandleFunc("GET /api/issues", s.requireAuth(filtered, s.handleListIssues))
	s.mux.HandleFunc("POST /api/issues", s.requireAuth(filtered, s.handleCreateIssue))
	s.mux.HandleFunc("GET /api/issues/{ref}", s.requireAuth(s.inProject("issue"), s.handleGetIssue))
	s.mux.HandleFunc("PATCH /api/issues/{ref}", s.requireAuth(s.inProject("issue"), s.handleUpdateIssue))
	s.mux.HandleFunc("DELETE /api/issues/{ref}", s.requireAuth(s.inProject("issue"), s.handleDeleteIssue))
	s.mux.HandleFunc("GET /api/issues/{ref}/comments", s.requireAuth(s.inProject("issue"), s.handleListComments))
	s.mux.HandleFunc("POST /api/issues/{ref}/comments", s.requireAuth(s.inProject("issue"), s.handleAddComment))
	s.mux.HandleFunc("GET /api/issues/{ref}/activities", s.requireAuth(s.inProject("issue"), s.handleListActivities))
	s.mux.HandleFunc("PATCH /api/comments/{id}", s.requireAuth(s.inProject("comment"), s.handleUpdateComment))
	s.mux.HandleFunc("DELETE /api/comments/{id}", s.requireAuth(s.inProject("comment"), s.handleDeleteComment))
	s.mux.HandleFunc("POST /api/attachments", s.requireAuth(filtered, s.handleUploadAttachment))
	s.mux.HandleFunc("GET /api/attachments/{id}/{filename}", s.requireAuth(s.inProject("attachment"), s.handleServeAttachment))

	// Catch-all: 404 JSON for unknown /api/*, SPA static files otherwise
	s.mux.HandleFunc("/", s.handleSPA)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasUsers()
	if err != nil {
		writeErr(w, err)
		return
	}
	u, _, _ := s.authenticateRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"has_users": has,
		"user":      u,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.store.CreateFirstAdmin(body.Email, body.Name, body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	tok, exp, err := s.store.CreateSession(u.ID, sessionTTL)
	if err != nil {
		writeErr(w, err)
		return
	}
	setSessionCookie(w, tok, exp)
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "session_token": tok})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	ipKey, emailKey := loginKeys(r, body.Email)
	if wait := s.logins.retryAfter(ipKey, emailKey); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed logins; try again later"})
		return
	}
	u, err := s.store.AuthenticatePassword(body.Email, body.Password)
	if errors.Is(err, db.ErrUnauthorized) {
		s.logins.fail(ipKey, emailKey)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.logins.reset(emailKey)
	tok, exp, err := s.store.CreateSession(u.ID, sessionTTL)
	if err != nil {
		writeErr(w, err)
		return
	}
	setSessionCookie(w, tok, exp)
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "session_token": tok})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.store.AcceptInvite(body.Code, body.Email, body.Name, body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	tok, exp, err := s.store.CreateSession(u.ID, sessionTTL)
	if err != nil {
		writeErr(w, err)
		return
	}
	setSessionCookie(w, tok, exp)
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "session_token": tok})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	u, err := s.store.CreateUserDirect(body.Email, body.Name, body.Password, body.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in db.UpdateUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.store.UpdateUser(currentUser(r).ID, r.PathValue("id"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if u.DisabledAt != nil {
		s.hub.DisconnectUser(u.ID)
	}
	s.hub.Broadcast("user.updated", "", u)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteUser(currentUser(r).ID, id); err != nil {
		writeErr(w, err)
		return
	}
	s.hub.DisconnectUser(id)
	s.hub.Broadcast("user.deleted", "", map[string]string{"id": id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.ResetPassword(r.PathValue("id"), body.Password); err != nil {
		writeErr(w, err)
		return
	}
	s.hub.DisconnectUser(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChangePassword changes the caller's own password and signs out their
// other sessions, keeping the one used for this request.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	keep := ""
	if c, err := r.Cookie(sessionCookieName); err == nil {
		keep = c.Value
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.HasPrefix(strings.TrimSpace(auth[7:]), "vs_") {
		keep = strings.TrimSpace(auth[7:])
	}
	if err := s.store.ChangePassword(currentUser(r).ID, body.CurrentPassword, body.NewPassword, keep); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAPITokens(currentUser(r).ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		db.TokenScope
	}
	_ = decodeJSON(r, &body)
	raw, meta, err := s.store.CreateAPIToken(currentUser(r).ID, body.Name, body.TokenScope)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      raw,
		"token_meta": meta,
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIToken(currentUser(r).ID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := s.store.ListInvites()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, invites)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	_ = decodeJSON(r, &body)
	inv, err := s.store.CreateInvite(currentUser(r).ID, body.Role, 7*24*time.Hour)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		writeErr(w, err)
		return
	}
	if t := currentToken(r); t != nil && t.Restricted() {
		visible := projects[:0]
		for _, p := range projects {
			if t.AllowsProject(p.ID) {
				visible = append(visible, p)
			}
		}
		projects = visible
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.store.CreateProject(body.Key, body.Name, body.Description)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("project.created", p.ID, p)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	states, err := s.store.ListWorkflowStates(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	labels, err := s.store.ListLabels(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	cycles, err := s.store.ListCycles(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": p,
		"states":  states,
		"labels":  labels,
		"cycles":  cycles,
	})
}

func (s *Server) handleListStates(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	states, err := s.store.ListWorkflowStates(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, states)
}

func (s *Server) handleCreateState(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Name     string  `json:"name"`
		Category string  `json:"category"`
		Color    string  `json:"color"`
		Position float64 `json:"position"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.store.CreateWorkflowState(p.ID, body.Name, body.Category, body.Color, body.Position)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("project.updated", p.ID, p)
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) handleUpdateState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string   `json:"name"`
		Category string   `json:"category"`
		Color    string   `json:"color"`
		Position *float64 `json:"position"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.store.UpdateWorkflowState(r.PathValue("id"), body.Name, body.Category, body.Color, body.Position)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("state.updated", st.ProjectID, st)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleListLabels(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	labels, err := s.store.ListLabels(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, labels)
}

func (s *Server) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	l, err := s.store.CreateLabel(p.ID, body.Name, body.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) handleDeleteLabel(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteLabel(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListCycles(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	cycles, err := s.store.ListCycles(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cycles)
}

func (s *Server) handleCreateCycle(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		StartDate   string `json:"start_date"`
		EndDate     string `json:"end_date"`
		Status      string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.store.CreateCycle(p.ID, body.Name, body.Description, body.StartDate, body.EndDate, body.Status)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("cycle.created", c.ProjectID, c)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateCycle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		StartDate   string `json:"start_date"`
		EndDate     string `json:"end_date"`
		Status      string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.store.UpdateCycle(r.PathValue("id"), body.Name, body.Description, body.StartDate, body.EndDate, body.Status)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("cycle.updated", c.ProjectID, c)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleExportProject(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "md" || format == "markdown" {
		md, err := s.store.ExportProjectMarkdown(ref)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(md))
		return
	}
	bundle, err := s.store.ExportProjectJSON(ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Server) handleImportProject(w http.ResponseWriter, r *http.Request) {
	var bundle db.ExportBundle
	if err := decodeJSON(r, &bundle); err != nil {
		writeErr(w, err)
		return
	}
	proj, err := s.store.ImportProjectJSON(currentUser(r).ID, &bundle)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("project.created", proj.ID, proj)
	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.store.SearchIssues(q, limit, allowedProjects(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	var prio *int
	if pStr := qv.Get("priority"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil {
			prio = &p
		}
	}
	limit, _ := strconv.Atoi(qv.Get("limit"))
	assignee := qv.Get("assignee")
	if strings.EqualFold(assignee, "me") {
		assignee = currentUser(r).ID
	}
	issues, err := s.store.ListIssues(db.IssueFilter{
		ProjectRef:    qv.Get("project"),
		StateID:       qv.Get("state"),
		StateCategory: qv.Get("category"),
		CycleID:       qv.Get("cycle"),
		AssigneeID:    assignee,
		Priority:      prio,
		ParentID:      qv.Get("parent"),
		LabelID:       qv.Get("label"),
		Query:         qv.Get("q"),
		Limit:         limit,
		ProjectIDs:    allowedProjects(r),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issues)
}

func (s *Server) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var in db.CreateIssueInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if p, err := s.store.GetProject(in.ProjectRef); err == nil {
		if err := checkProject(r, p.ID); err != nil {
			writeErr(w, err)
			return
		}
	}
	iss, err := s.storeFor(r).CreateIssue(currentUser(r).ID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("issue.created", iss.ProjectID, iss)
	writeJSON(w, http.StatusCreated, iss)
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	iss, err := s.store.GetIssue(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.URL.Query().Get("detail") == "1" {
		comments, err := s.store.ListComments(iss.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		activities, err := s.store.ListActivities(iss.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"issue":      iss,
			"comments":   comments,
			"activities": activities,
		})
		return
	}
	writeJSON(w, http.StatusOK, iss)
}

func (s *Server) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	var patch db.UpdateIssueInput
	if err := decodeJSON(r, &patch); err != nil {
		writeErr(w, err)
		return
	}
	iss, err := s.storeFor(r).UpdateIssue(currentUser(r).ID, r.PathValue("ref"), patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("issue.updated", iss.ProjectID, iss)
	writeJSON(w, http.StatusOK, iss)
}

func (s *Server) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	iss, err := s.store.GetIssue(ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.DeleteIssue(iss.ID); err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("issue.deleted", iss.ProjectID, map[string]string{"id": iss.ID, "identifier": iss.Identifier})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	iss, err := s.store.GetIssue(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	comments, err := s.store.ListComments(iss.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	cm, err := s.storeFor(r).AddComment(currentUser(r).ID, r.PathValue("ref"), body.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("comment.created", s.issueProject(cm.IssueID), cm)
	writeJSON(w, http.StatusCreated, cm)
}

func (s *Server) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	cm, err := s.store.UpdateComment(currentUser(r), r.PathValue("id"), body.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("comment.updated", s.issueProject(cm.IssueID), cm)
	writeJSON(w, http.StatusOK, cm)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	cm, err := s.store.DeleteComment(currentUser(r), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("comment.deleted", s.issueProject(cm.IssueID), map[string]string{"id": cm.ID, "issue_id": cm.IssueID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.store.UpdateProject(r.PathValue("ref"), body.Name, body.Description)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("project.updated", p.ID, p)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.DeleteProject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("project.deleted", p.ID, map[string]string{"id": p.ID, "key": p.Key})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteCycle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	projectID, _ := s.store.ProjectIDOf("cycle", id)
	if err := s.store.DeleteCycle(id); err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("cycle.deleted", projectID, map[string]string{"id": id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteInvite(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	iss, err := s.store.GetIssue(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	acts, err := s.store.ListActivities(iss.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acts)
}

func sanitizeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == "/" {
		return "upload.bin"
	}
	var sb strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	out := sb.String()
	if out == "" {
		return "upload.bin"
	}
	return out
}

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file exceeds 20MB limit or invalid multipart"})
		return
	}
	// A token may only attach to issues in its projects; check before writing the file.
	if id := strings.TrimSpace(r.FormValue("issue_id")); id != "" {
		iss, err := s.store.GetIssue(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := checkProject(r, iss.ProjectID); err != nil {
			writeErr(w, err)
			return
		}
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
		return
	}
	defer file.Close()

	cleanName := sanitizeFilename(hdr.Filename)
	var randBuf [12]byte
	_, _ = rand.Read(randBuf[:])
	storageName := hex.EncodeToString(randBuf[:]) + "_" + cleanName
	dstPath := filepath.Join(s.uploadsDir, storageName)

	dst, err := os.Create(dstPath)
	if err != nil {
		writeErr(w, err)
		return
	}
	written, copyErr := io.Copy(dst, file)
	_ = dst.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		writeErr(w, copyErr)
		return
	}

	ct := hdr.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(cleanName))); byExt != "" {
			ct = byExt
		}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}

	issueID := strings.TrimSpace(r.FormValue("issue_id"))
	att, err := s.store.CreateAttachment(currentUser(r).ID, issueID, cleanName, ct, storageName, written)
	if err != nil {
		_ = os.Remove(dstPath)
		writeErr(w, err)
		return
	}

	md := fmt.Sprintf("[%s](%s)", att.Filename, att.URL)
	if strings.HasPrefix(ct, "image/") {
		md = "!" + md
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"attachment": att,
		"markdown":   md,
	})
}

func (s *Server) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.store.GetAttachment(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	fullPath := filepath.Join(s.uploadsDir, att.StorageName)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	safeInline := att.ContentType == "image/png" || att.ContentType == "image/jpeg" || att.ContentType == "image/gif" || att.ContentType == "image/webp"
	if !safeInline {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", att.Filename))
	}
	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "api endpoint not found"})
		return
	}
	if s.staticFS == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Velo API Server"))
		return
	}

	cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if cleanPath == "" {
		cleanPath = "index.html"
	}
	if f, err := s.staticFS.Open(cleanPath); err == nil {
		_ = f.Close()
		http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
		return
	}
	// SPA client-side router fallback
	indexBytes, err := fs.ReadFile(s.staticFS, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexBytes)
}

// issueProject returns the project of an issue for scoping SSE events.
func (s *Server) issueProject(issueID string) string {
	iss, err := s.store.GetIssue(issueID)
	if err != nil {
		return hiddenProject
	}
	return iss.ProjectID
}
