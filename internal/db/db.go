package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrValidation   = errors.New("validation error")
	ErrForbidden    = errors.New("forbidden")
)

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		filepath.ToSlash(dbPath),
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	s := &Store{db: sqlDB}
	if err := s.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return s, nil
}

// BackupTo writes a consistent, compacted copy of the database to path
// (which must not exist). Safe while the server is running.
func (s *Store) BackupTo(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrConflict, path)
	}
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func newID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Second)
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name          TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	disabled_at   DATETIME,
	created_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	token_prefix TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	last_used_at DATETIME,
	created_at   DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

CREATE TABLE IF NOT EXISTS invites (
	id         TEXT PRIMARY KEY,
	code       TEXT NOT NULL UNIQUE,
	role       TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	used_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
	id          TEXT PRIMARY KEY,
	key         TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	issue_seq   INTEGER NOT NULL DEFAULT 0,
	created_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_states (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	category   TEXT NOT NULL CHECK (category IN ('backlog', 'unstarted', 'started', 'completed', 'canceled')),
	color      TEXT NOT NULL DEFAULT '#64748b',
	position   REAL NOT NULL DEFAULT 1000
);
CREATE INDEX IF NOT EXISTS idx_workflow_states_project ON workflow_states(project_id, position);

CREATE TABLE IF NOT EXISTS cycles (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	start_date  TEXT NOT NULL DEFAULT '',
	end_date    TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'upcoming' CHECK (status IN ('upcoming', 'active', 'completed')),
	created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cycles_project ON cycles(project_id);

CREATE TABLE IF NOT EXISTS labels (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	color      TEXT NOT NULL DEFAULT '#6366f1',
	UNIQUE(project_id, name COLLATE NOCASE)
);

CREATE TABLE IF NOT EXISTS issues (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	number      INTEGER NOT NULL,
	identifier  TEXT NOT NULL UNIQUE COLLATE NOCASE,
	parent_id   TEXT REFERENCES issues(id) ON DELETE SET NULL,
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	state_id    TEXT NOT NULL REFERENCES workflow_states(id),
	priority    INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 4),
	assignee_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	creator_id  TEXT NOT NULL REFERENCES users(id),
	cycle_id    TEXT REFERENCES cycles(id) ON DELETE SET NULL,
	position    REAL NOT NULL DEFAULT 1000,
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL,
	UNIQUE(project_id, number)
);
CREATE INDEX IF NOT EXISTS idx_issues_project_state ON issues(project_id, state_id, position);
CREATE INDEX IF NOT EXISTS idx_issues_parent ON issues(parent_id);
CREATE INDEX IF NOT EXISTS idx_issues_cycle ON issues(cycle_id);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee_id);

CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
	identifier,
	title,
	description,
	content='issues',
	content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS issues_ai AFTER INSERT ON issues BEGIN
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier, new.title, new.description);
END;

CREATE TRIGGER IF NOT EXISTS issues_ad AFTER DELETE ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier, old.title, old.description);
END;

CREATE TRIGGER IF NOT EXISTS issues_au AFTER UPDATE OF identifier, title, description ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier, old.title, old.description);
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier, new.title, new.description);
END;

CREATE TABLE IF NOT EXISTS issue_labels (
	issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	label_id TEXT NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
	PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE IF NOT EXISTS comments (
	id         TEXT PRIMARY KEY,
	issue_id   TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	author_id  TEXT NOT NULL REFERENCES users(id),
	body       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at);

CREATE TABLE IF NOT EXISTS activities (
	id         TEXT PRIMARY KEY,
	issue_id   TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	actor_id   TEXT NOT NULL REFERENCES users(id),
	action     TEXT NOT NULL,
	field      TEXT NOT NULL DEFAULT '',
	old_value  TEXT NOT NULL DEFAULT '',
	new_value  TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activities_issue ON activities(issue_id, created_at);

CREATE TABLE IF NOT EXISTS attachments (
	id           TEXT PRIMARY KEY,
	issue_id     TEXT REFERENCES issues(id) ON DELETE SET NULL,
	uploader_id  TEXT NOT NULL REFERENCES users(id),
	filename     TEXT NOT NULL,
	content_type TEXT NOT NULL,
	size         INTEGER NOT NULL,
	storage_name TEXT NOT NULL UNIQUE,
	created_at   DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attachments_issue ON attachments(issue_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return err
	}
	// v0.2: users.disabled_at (databases created by v0.1 lack it).
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name = 'disabled_at'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE users ADD COLUMN disabled_at DATETIME`); err != nil {
			return err
		}
	}
	return nil
}
