package db

import (
	"database/sql"
	"errors"
	"strings"
)

const gitBotEmail = "github-bot@velo.local"

// UserIDForGitEmail returns the enabled Velo user whose email matches a Git
// author, or a "GitHub" bot user (created on first use, cannot log in).
// Disabled users are treated as unknown so nothing is posted in their name.
func (s *Store) UserIDForGitEmail(email string) (string, error) {
	var id string
	email = strings.ToLower(strings.TrimSpace(email))
	if email != "" {
		err := s.db.QueryRow(`SELECT id FROM users WHERE email = ? AND disabled_at IS NULL`, email).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	// "!" is not a valid bcrypt hash, so password login always fails for the bot.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, name, password_hash, role, created_at) VALUES (?, ?, 'GitHub', '!', 'member', ?)`,
		newID(), gitBotEmail, nowUTC(),
	); err != nil {
		return "", err
	}
	err := s.db.QueryRow(`SELECT id FROM users WHERE email = ?`, gitBotEmail).Scan(&id)
	return id, err
}

// HasCommentContaining reports whether an issue already has a comment containing text.
// Used to make webhook deliveries idempotent.
func (s *Store) HasCommentContaining(issueID, text string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM comments WHERE issue_id = ? AND instr(body, ?) > 0`,
		issueID, text,
	).Scan(&n)
	return n > 0, err
}

// CompletedStateID returns the first "completed" workflow state of a project.
func (s *Store) CompletedStateID(projectID string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM workflow_states WHERE project_id = ? AND category = 'completed' ORDER BY position LIMIT 1`,
		projectID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
