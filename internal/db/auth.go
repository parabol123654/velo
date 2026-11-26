package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Store) HasUsers() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func normalizeCredentials(email, name, password string) (string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if email == "" || !strings.Contains(email, "@") {
		return "", "", fmt.Errorf("%w: valid email is required", ErrValidation)
	}
	if name == "" {
		return "", "", fmt.Errorf("%w: name is required", ErrValidation)
	}
	if err := validatePassword(password); err != nil {
		return "", "", err
	}
	return email, name, nil
}

func (s *Store) CreateFirstAdmin(email, name, password string) (*User, error) {
	email, name, err := normalizeCredentials(email, name, password)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, fmt.Errorf("%w: initial admin already exists", ErrConflict)
	}

	u := &User{
		ID:           newID(),
		Email:        email,
		Name:         name,
		Role:         "admin",
		PasswordHash: string(hash),
		CreatedAt:    nowUTC(),
	}
	_, err = tx.Exec(
		`INSERT INTO users (id, email, name, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.Role, u.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CreateUserDirect(email, name, password, role string) (*User, error) {
	email, name, err := normalizeCredentials(email, name, password)
	if err != nil {
		return nil, err
	}
	if role != "admin" && role != "member" {
		return nil, fmt.Errorf("%w: role must be admin or member", ErrValidation)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &User{
		ID:           newID(),
		Email:        email,
		Name:         name,
		Role:         role,
		PasswordHash: string(hash),
		CreatedAt:    nowUTC(),
	}
	_, err = s.db.Exec(
		`INSERT INTO users (id, email, name, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.Role, u.CreatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%w: email already registered", ErrConflict)
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) AuthenticatePassword(email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	err := s.db.QueryRow(
		`SELECT id, email, name, password_hash, role, created_at FROM users WHERE email = ? AND disabled_at IS NULL`,
		email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrUnauthorized
	}
	return &u, nil
}

func (s *Store) CreateSession(userID string, ttl time.Duration) (string, time.Time, error) {
	raw := "vs_" + randomHex(24)
	now := nowUTC()
	exp := now.Add(ttl)
	_, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashToken(raw), userID, exp, now,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, exp, nil
}

func (s *Store) GetUserBySession(rawToken string) (*User, error) {
	if rawToken == "" {
		return nil, ErrUnauthorized
	}
	var u User
	var exp time.Time
	err := s.db.QueryRow(
		`SELECT u.id, u.email, u.name, u.role, u.created_at, s.expires_at
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND u.disabled_at IS NULL`,
		hashToken(rawToken),
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(exp) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(rawToken))
		return nil, ErrUnauthorized
	}
	return &u, nil
}

func (s *Store) DeleteSession(rawToken string) error {
	if rawToken == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(rawToken))
	return err
}

func (s *Store) CreateAPIToken(userID, name string) (string, *APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "CLI / MCP Token"
	}
	raw := "velo_" + randomHex(20)
	meta := &APIToken{
		ID:          newID(),
		UserID:      userID,
		Name:        name,
		TokenPrefix: raw[:12],
		CreatedAt:   nowUTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		meta.ID, meta.UserID, meta.Name, meta.TokenPrefix, hashToken(raw), meta.CreatedAt,
	)
	if err != nil {
		return "", nil, err
	}
	return raw, meta, nil
}

func (s *Store) GetUserByAPIToken(rawToken string) (*User, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, ErrUnauthorized
	}
	h := hashToken(rawToken)
	var u User
	err := s.db.QueryRow(
		`SELECT u.id, u.email, u.name, u.role, u.created_at
		 FROM api_tokens t
		 JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = ? AND u.disabled_at IS NULL`,
		h,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, nowUTC(), h)
	return &u, nil
}

func (s *Store) ListAPITokens(userID string) ([]APIToken, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, token_prefix, created_at, last_used_at
		 FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var lastUsed sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenPrefix, &t.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			tm := lastUsed.Time
			t.LastUsedAt = &tm
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(userID, tokenID string) error {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateInvite(adminID, role string, ttl time.Duration) (*Invite, error) {
	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "member" {
		return nil, fmt.Errorf("%w: invalid role", ErrValidation)
	}
	now := nowUTC()
	inv := &Invite{
		ID:        newID(),
		Code:      randomHex(12),
		Role:      role,
		CreatedBy: adminID,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	_, err := s.db.Exec(
		`INSERT INTO invites (id, code, role, created_by, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.Code, inv.Role, inv.CreatedBy, inv.ExpiresAt, inv.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *Store) ListInvites() ([]Invite, error) {
	rows, err := s.db.Query(
		`SELECT id, code, role, created_by, used_by, expires_at, created_at
		 FROM invites ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Invite{}
	for rows.Next() {
		var inv Invite
		var usedBy sql.NullString
		if err := rows.Scan(&inv.ID, &inv.Code, &inv.Role, &inv.CreatedBy, &usedBy, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, err
		}
		if usedBy.Valid {
			v := usedBy.String
			inv.UsedBy = &v
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Store) AcceptInvite(code, email, name, password string) (*User, error) {
	email, name, err := normalizeCredentials(email, name, password)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var inv Invite
	var usedBy sql.NullString
	err = tx.QueryRow(
		`SELECT id, role, used_by, expires_at FROM invites WHERE code = ?`,
		strings.TrimSpace(code),
	).Scan(&inv.ID, &inv.Role, &usedBy, &inv.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if usedBy.Valid || time.Now().UTC().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("%w: invite expired or already used", ErrValidation)
	}

	u := &User{
		ID:           newID(),
		Email:        email,
		Name:         name,
		Role:         inv.Role,
		PasswordHash: string(hash),
		CreatedAt:    nowUTC(),
	}
	_, err = tx.Exec(
		`INSERT INTO users (id, email, name, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.Role, u.CreatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%w: email already registered", ErrConflict)
		}
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE invites SET used_by = ? WHERE id = ?`, u.ID, inv.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, email, name, role, disabled_at, created_at FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		var u User
		var disabled sql.NullTime
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &disabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		if disabled.Valid {
			tm := disabled.Time
			u.DisabledAt = &tm
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) getUser(id string) (*User, error) {
	var u User
	var disabled sql.NullTime
	err := s.db.QueryRow(
		`SELECT id, email, name, role, password_hash, disabled_at, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &disabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if disabled.Valid {
		tm := disabled.Time
		u.DisabledAt = &tm
	}
	return &u, nil
}

func validatePassword(password string) error {
	if len(password) < 6 {
		return fmt.Errorf("%w: password must be at least 6 characters", ErrValidation)
	}
	return nil
}

// ChangePassword lets a user change their own password. All of the user's
// sessions except keepSession (the caller's current one, may be empty) are revoked.
func (s *Store) ChangePassword(userID, current, next, keepSession string) error {
	u, err := s.getUser(userID)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
		return fmt.Errorf("%w: current password is incorrect", ErrValidation)
	}
	return s.setPassword(userID, next, keepSession)
}

// ResetPassword is the admin path: set a new password and revoke all sessions.
func (s *Store) ResetPassword(userID, next string) error {
	if _, err := s.getUser(userID); err != nil {
		return err
	}
	return s.setPassword(userID, next, "")
}

func (s *Store) setPassword(userID, next, keepSession string) error {
	if err := validatePassword(next); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, hashToken(keepSession)); err != nil {
		return err
	}
	return tx.Commit()
}

// otherActiveAdmins counts enabled admins other than userID.
func (s *Store) otherActiveAdmins(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL AND id != ?`, userID,
	).Scan(&n)
	return n, err
}

type UpdateUserInput struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// UpdateUser changes a user's role or disabled flag (admin only). An admin
// cannot change themselves, and the last enabled admin cannot be demoted or disabled.
// Disabling revokes the user's sessions; API tokens stop working while disabled.
func (s *Store) UpdateUser(actorID, userID string, in UpdateUserInput) (*User, error) {
	if actorID == userID {
		return nil, fmt.Errorf("%w: you cannot change your own role or status", ErrValidation)
	}
	u, err := s.getUser(userID)
	if err != nil {
		return nil, err
	}
	if in.Role != nil && *in.Role != "admin" && *in.Role != "member" {
		return nil, fmt.Errorf("%w: role must be admin or member", ErrValidation)
	}
	losesAdmin := u.Role == "admin" && ((in.Role != nil && *in.Role != "admin") || (in.Disabled != nil && *in.Disabled))
	if losesAdmin {
		n, err := s.otherActiveAdmins(userID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: cannot remove the last active admin", ErrValidation)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if in.Role != nil {
		if _, err := tx.Exec(`UPDATE users SET role = ? WHERE id = ?`, *in.Role, userID); err != nil {
			return nil, err
		}
	}
	if in.Disabled != nil {
		if *in.Disabled {
			if _, err := tx.Exec(`UPDATE users SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ?`, nowUTC(), userID); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
				return nil, err
			}
		} else if _, err := tx.Exec(`UPDATE users SET disabled_at = NULL WHERE id = ?`, userID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getUser(userID)
}

// DeleteUser removes a user who has no history (issues created, comments,
// activity, uploads). Users with history must be disabled instead.
func (s *Store) DeleteUser(actorID, userID string) error {
	if actorID == userID {
		return fmt.Errorf("%w: you cannot delete yourself", ErrValidation)
	}
	u, err := s.getUser(userID)
	if err != nil {
		return err
	}
	if u.Role == "admin" && u.DisabledAt == nil {
		n, err := s.otherActiveAdmins(userID)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: cannot remove the last active admin", ErrValidation)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
			return fmt.Errorf("%w: user has issues, comments or activity; disable the account instead", ErrConflict)
		}
		return err
	}
	return nil
}
