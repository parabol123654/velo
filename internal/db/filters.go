package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxFilterName = 80

// checkSavedFilter trims and validates a name and a query.
func checkSavedFilter(name, query string) (string, string, error) {
	name, query = strings.TrimSpace(name), strings.TrimSpace(query)
	if name == "" || utf8.RuneCountInString(name) > maxFilterName {
		return "", "", fmt.Errorf("%w: a filter needs a name of 1-%d characters", ErrValidation, maxFilterName)
	}
	if query == "" {
		return "", "", fmt.Errorf("%w: a filter needs a query", ErrValidation)
	}
	if _, err := ParseIssueQuery(query); err != nil {
		return "", "", err
	}
	return name, query, nil
}

// nameTaken reports whether userID has another filter named name in any case.
// SQLite's NOCASE folds ASCII only, so "Lỗi" and "LỖI" are compared here.
func nameTaken(tx *sql.Tx, userID, exceptID, name string) error {
	rows, err := tx.Query(`SELECT name FROM saved_filters WHERE user_id = ? AND id != ?`, userID, exceptID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return err
		}
		if strings.EqualFold(other, name) {
			return fmt.Errorf("%w: you already have a filter named %q", ErrConflict, other)
		}
	}
	return rows.Err()
}

func uniqueName(err error, name string) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("%w: you already have a filter named %q", ErrConflict, name)
	}
	return err
}

// ListSavedFilters returns userID's filters in position order.
func (s *Store) ListSavedFilters(userID string) ([]SavedFilter, error) {
	rows, err := s.db.Query(`SELECT id, name, query, position, created_at FROM saved_filters
		WHERE user_id = ? ORDER BY position, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedFilter{}
	for rows.Next() {
		var f SavedFilter
		if err := rows.Scan(&f.ID, &f.Name, &f.Query, &f.Position, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CreateSavedFilter adds a filter at the end of userID's list. Names are
// unique per user, ignoring case; the query must parse.
func (s *Store) CreateSavedFilter(userID, name, query string) (*SavedFilter, error) {
	name, query, err := checkSavedFilter(name, query)
	if err != nil {
		return nil, err
	}
	f := &SavedFilter{ID: newID(), Name: name, Query: query, CreatedAt: nowUTC()}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := nameTaken(tx, userID, "", name); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO saved_filters (id, user_id, name, query, position, created_at)
		VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1000 FROM saved_filters WHERE user_id = ?), ?)`,
		f.ID, userID, f.Name, f.Query, userID, f.CreatedAt)
	if err != nil {
		return nil, uniqueName(err, name)
	}
	if f, err = s.savedFilter(tx, userID, f.ID); err != nil {
		return nil, err
	}
	return f, tx.Commit()
}

// UpdateSavedFilter changes the fields set in in. Another user's filter is
// ErrNotFound, as if it did not exist.
func (s *Store) UpdateSavedFilter(userID, id string, in SavedFilterInput) (*SavedFilter, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	f, err := s.savedFilter(tx, userID, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		f.Name = *in.Name
	}
	if in.Query != nil {
		f.Query = *in.Query
	}
	if in.Position != nil {
		f.Position = *in.Position
	}
	if f.Name, f.Query, err = checkSavedFilter(f.Name, f.Query); err != nil {
		return nil, err
	}
	if err := nameTaken(tx, userID, id, f.Name); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE saved_filters SET name = ?, query = ?, position = ? WHERE id = ? AND user_id = ?`,
		f.Name, f.Query, f.Position, id, userID); err != nil {
		return nil, uniqueName(err, f.Name)
	}
	return f, tx.Commit()
}

// DeleteSavedFilter removes one of userID's filters.
func (s *Store) DeleteSavedFilter(userID, id string) error {
	res, err := s.db.Exec(`DELETE FROM saved_filters WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) savedFilter(q rowQuerier, userID, id string) (*SavedFilter, error) {
	var f SavedFilter
	err := q.QueryRow(`SELECT id, name, query, position, created_at FROM saved_filters WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&f.ID, &f.Name, &f.Query, &f.Position, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}
