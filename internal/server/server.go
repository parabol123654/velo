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

	"github.com/velo-dev/velo/internal/db"
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

func (s *Server) authenticateRequest(r *http.Request) (*db.User, error) {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[7:])
		if strings.HasPrefix(tok, "vs_") {
			return s.store.GetUserBySession(tok)
		}
		return s.store.GetUserByAPIToken(tok)
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return s.store.GetUserBySession(c.Value)
	}
	return nil, db.ErrUnauthorized
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticateRequest(r)
		if err != nil {
			writeErr(w, db.ErrUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
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

	// SSE live stream
	s.mux.HandleFunc("GET /api/events", s.requireAuth(s.hub.ServeHTTP))

	// Users, Tokens & Invites
	s.mux.HandleFunc("GET /api/users", s.requireAuth(s.handleListUsers))
	s.mux.HandleFunc("POST /api/users", s.requireAdmin(s.handleCreateUser))
	s.mux.HandleFunc("GET /api/tokens", s.requireAuth(s.handleListTokens))
	s.mux.HandleFunc("POST /api/tokens", s.requireAuth(s.handleCreateToken))
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.requireAuth(s.handleDeleteToken))
	s.mux.HandleFunc("GET /api/invites", s.requireAdmin(s.handleListInvites))
	s.mux.HandleFunc("POST /api/invites", s.requireAdmin(s.handleCreateInvite))

	// Projects, States, Labels, Cycles & Export/Import
	s.mux.HandleFunc("GET /api/projects", s.requireAuth(s.handleListProjects))
	s.mux.HandleFunc("POST /api/projects", s.requireAuth(s.handleCreateProject))
	s.mux.HandleFunc("GET /api/projects/{ref}", s.requireAuth(s.handleGetProject))
	s.mux.HandleFunc("GET /api/projects/{ref}/states", s.requireAuth(s.handleListStates))
	s.mux.HandleFunc("POST /api/projects/{ref}/states", s.requireAuth(s.handleCreateState))
	s.mux.HandleFunc("PATCH /api/states/{id}", s.requireAuth(s.handleUpdateState))
	s.mux.HandleFunc("GET /api/projects/{ref}/labels", s.requireAuth(s.handleListLabels))
	s.mux.HandleFunc("POST /api/projects/{ref}/labels", s.requireAuth(s.handleCreateLabel))
	s.mux.HandleFunc("DELETE /api/labels/{id}", s.requireAuth(s.handleDeleteLabel))
	s.mux.HandleFunc("GET /api/projects/{ref}/cycles", s.requireAuth(s.handleListCycles))
	s.mux.HandleFunc("POST /api/projects/{ref}/cycles", s.requireAuth(s.handleCreateCycle))
	s.mux.HandleFunc("PATCH /api/cycles/{id}", s.requireAuth(s.handleUpdateCycle))
	s.mux.HandleFunc("GET /api/projects/{ref}/export", s.requireAuth(s.handleExportProject))
	s.mux.HandleFunc("POST /api/projects/import", s.requireAuth(s.handleImportProject))

	// Issues, Search, Comments, Activities & Attachments
	s.mux.HandleFunc("GET /api/search", s.requireAuth(s.handleSearch))
	s.mux.HandleFunc("GET /api/issues", s.requireAuth(s.handleListIssues))
	s.mux.HandleFunc("POST /api/issues", s.requireAuth(s.handleCreateIssue))
	s.mux.HandleFunc("GET /api/issues/{ref}", s.requireAuth(s.handleGetIssue))
	s.mux.HandleFunc("PATCH /api/issues/{ref}", s.requireAuth(s.handleUpdateIssue))
	s.mux.HandleFunc("DELETE /api/issues/{ref}", s.requireAuth(s.handleDeleteIssue))
	s.mux.HandleFunc("GET /api/issues/{ref}/comments", s.requireAuth(s.handleListComments))
	s.mux.HandleFunc("POST /api/issues/{ref}/comments", s.requireAuth(s.handleAddComment))
	s.mux.HandleFunc("GET /api/issues/{ref}/activities", s.requireAuth(s.handleListActivities))
	s.mux.HandleFunc("POST /api/attachments", s.requireAuth(s.handleUploadAttachment))
	s.mux.HandleFunc("GET /api/attachments/{id}/{filename}", s.requireAuth(s.handleServeAttachment))

	// Catch-all: 404 JSON for unknown /api/*, SPA static files otherwise
	s.mux.HandleFunc("/", s.handleSPA)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasUsers()
	if err != nil {
		writeErr(w, err)
		return
	}
	u, _ := s.authenticateRequest(r)
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
	u, err := s.store.AuthenticatePassword(body.Email, body.Password)
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
	}
	_ = decodeJSON(r, &body)
	raw, meta, err := s.store.CreateAPIToken(currentUser(r).ID, body.Name)
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
	s.hub.Broadcast("project.created", p)
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
	s.hub.Broadcast("project.updated", p)
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
	s.hub.Broadcast("state.updated", st)
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
	s.hub.Broadcast("cycle.created", c)
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
	s.hub.Broadcast("cycle.updated", c)
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
	s.hub.Broadcast("project.created", proj)
	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.store.SearchIssues(q, limit)
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
	issues, err := s.store.ListIssues(db.IssueFilter{
		ProjectRef:    qv.Get("project"),
		StateID:       qv.Get("state"),
		StateCategory: qv.Get("category"),
		CycleID:       qv.Get("cycle"),
		AssigneeID:    qv.Get("assignee"),
		Priority:      prio,
		ParentID:      qv.Get("parent"),
		LabelID:       qv.Get("label"),
		Query:         qv.Get("q"),
		Limit:         limit,
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
	iss, err := s.store.CreateIssue(currentUser(r).ID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("issue.created", iss)
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
	iss, err := s.store.UpdateIssue(currentUser(r).ID, r.PathValue("ref"), patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("issue.updated", iss)
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
	s.hub.Broadcast("issue.deleted", map[string]string{"id": iss.ID, "identifier": iss.Identifier})
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
	cm, err := s.store.AddComment(currentUser(r).ID, r.PathValue("ref"), body.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Broadcast("comment.created", cm)
	writeJSON(w, http.StatusCreated, cm)
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
