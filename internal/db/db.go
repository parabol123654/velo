package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// via names the API token behind the current writes ("" for browser
	// sessions); recorded on comments and activities. Set with Via.
	via string
}

// Via returns a Store whose comments and activities record the given API
// token name, so people can tell which changes an agent made.
func (s *Store) Via(tokenName string) *Store {
	c := *s
	c.via = tokenName
	return &c
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
	access       TEXT NOT NULL DEFAULT 'write',
	project_ids  TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS issue_labels (
	issue_id TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	label_id TEXT NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
	PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE IF NOT EXISTS issue_links (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL CHECK (kind IN ('blocks', 'relates', 'duplicates')),
	created_at DATETIME NOT NULL,
	UNIQUE (source_id, target_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_issue_links_target ON issue_links(target_id);

CREATE TABLE IF NOT EXISTS comments (
	id         TEXT PRIMARY KEY,
	issue_id   TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	author_id  TEXT NOT NULL REFERENCES users(id),
	body       TEXT NOT NULL,
	via        TEXT NOT NULL DEFAULT '',
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
	via        TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activities_issue ON activities(issue_id, created_at);

CREATE TABLE IF NOT EXISTS saved_filters (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	query      TEXT NOT NULL,
	position   REAL NOT NULL,
	created_at DATETIME NOT NULL,
	UNIQUE (user_id, name COLLATE NOCASE)
);

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

-- What velo import made from which source item (#16), so a rerun skips it.
-- Comments use the external ID "comment:<id>". Rows go with their issue or comment.
CREATE TABLE IF NOT EXISTS import_refs (
	project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source      TEXT NOT NULL,
	external_id TEXT NOT NULL,
	issue_id    TEXT REFERENCES issues(id) ON DELETE CASCADE,
	comment_id  TEXT REFERENCES comments(id) ON DELETE CASCADE,
	created_at  DATETIME NOT NULL,
	PRIMARY KEY (project_id, source, external_id),
	CHECK ((issue_id IS NULL) <> (comment_id IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_import_refs_issue ON import_refs(issue_id);
CREATE INDEX IF NOT EXISTS idx_import_refs_comment ON import_refs(comment_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return err
	}
	// Columns added after v0.1; older databases get them here.
	for _, c := range []struct{ table, column, def string }{
		{"users", "disabled_at", "DATETIME"},                      // v0.2
		{"api_tokens", "access", "TEXT NOT NULL DEFAULT 'write'"}, // v0.3
		{"api_tokens", "project_ids", "TEXT NOT NULL DEFAULT ''"}, // v0.3
		{"comments", "via", "TEXT NOT NULL DEFAULT ''"},           // v0.3
		{"activities", "via", "TEXT NOT NULL DEFAULT ''"},         // v0.3
	} {
		if err := s.addColumn(c.table, c.column, c.def); err != nil {
			return err
		}
	}
	return s.migrateSearch()
}

// The search index folds Vietnamese accents: the tokenizer strips every
// diacritic (remove_diacritics 2, so ậ and ố fold too), and issues_fts_src
// turns đ/Đ, a separate letter rather than an accented d, into d. Queries go
// through foldD as well. The index is derived data, rebuilt from issues.
//
// Every copy of the đ expression below (view and triggers) and foldD must
// stay identical: with external content, a 'delete' must pass exactly the
// values that were indexed, or the index silently goes wrong.
const searchSQL = `
CREATE VIEW issues_fts_src AS
SELECT rowid AS rid, identifier,
       replace(replace(title, 'đ', 'd'), 'Đ', 'D') AS title,
       replace(replace(description, 'đ', 'd'), 'Đ', 'D') AS description
FROM issues;

CREATE VIRTUAL TABLE issues_fts USING fts5(
	identifier,
	title,
	description,
	content='issues_fts_src',
	content_rowid='rid',
	tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER issues_ai AFTER INSERT ON issues BEGIN
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier,
	        replace(replace(new.title, 'đ', 'd'), 'Đ', 'D'),
	        replace(replace(new.description, 'đ', 'd'), 'Đ', 'D'));
END;

CREATE TRIGGER issues_ad AFTER DELETE ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier,
	        replace(replace(old.title, 'đ', 'd'), 'Đ', 'D'),
	        replace(replace(old.description, 'đ', 'd'), 'Đ', 'D'));
END;

CREATE TRIGGER issues_au AFTER UPDATE OF identifier, title, description ON issues BEGIN
	INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
	VALUES ('delete', old.rowid, old.identifier,
	        replace(replace(old.title, 'đ', 'd'), 'Đ', 'D'),
	        replace(replace(old.description, 'đ', 'd'), 'Đ', 'D'));
	INSERT INTO issues_fts(rowid, identifier, title, description)
	VALUES (new.rowid, new.identifier,
	        replace(replace(new.title, 'đ', 'd'), 'Đ', 'D'),
	        replace(replace(new.description, 'đ', 'd'), 'Đ', 'D'));
END;

INSERT INTO issues_fts(issues_fts) VALUES ('rebuild');
`

// migrateSearch (re)creates the search index when it is missing or was made
// by a release before accent folding. Runs once; later opens skip it.
//
// The check runs under the write lock (BEGIN IMMEDIATE): velo backup may open
// the file while the server runs, and only one of them should rebuild.
func (s *Store) migrateSearch() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed { // also on panic: never hand the pool an open transaction
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	var def string
	err = conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'issues_fts'`).Scan(&def)
	switch {
	case err == nil && strings.Contains(def, "remove_diacritics 2"):
		_, err = conn.ExecContext(ctx, `COMMIT`)
		committed = err == nil
		return err
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err = conn.ExecContext(ctx, `
		DROP TRIGGER IF EXISTS issues_ai;
		DROP TRIGGER IF EXISTS issues_ad;
		DROP TRIGGER IF EXISTS issues_au;
		DROP TABLE IF EXISTS issues_fts;
		DROP VIEW IF EXISTS issues_fts_src;`+searchSQL); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	committed = err == nil
	return err
}

// foldD turns đ/Đ into d/D, as the search index does.
func foldD(s string) string { return dFolder.Replace(s) }

var dFolder = strings.NewReplacer("đ", "d", "Đ", "D")

// addColumn adds a column unless it already exists.
func (s *Store) addColumn(table, column, def string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, def))
	return err
}
