package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// attachmentRef finds attachment IDs in Markdown links such as
// ![shot.png](/api/attachments/<id>/shot.png).
var attachmentRef = regexp.MustCompile(`/api/attachments/([0-9a-f]+)/`)

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// linkAttachments gives issueID every unlinked attachment that texts refer to
// and that actorID uploaded. Uploads made before their issue existed (pasted
// into the new-issue form) are linked when the issue or a comment is saved, so
// access to them follows the issue's project from then on. Only the uploader
// can link: otherwise anyone who learned an unlinked upload's URL could claim
// it into their own project and read it.
func linkAttachments(ex execer, actorID, issueID string, texts ...string) error {
	for _, text := range texts {
		for _, m := range attachmentRef.FindAllStringSubmatch(text, -1) {
			if _, err := ex.Exec(`UPDATE attachments SET issue_id = ? WHERE id = ? AND issue_id IS NULL AND uploader_id = ?`, issueID, m[1], actorID); err != nil {
				return err
			}
		}
	}
	return nil
}

// releaseAttachments runs inside the transaction that deletes the issues
// matching issueScope (an SQL condition on the issues table, with args) and
// deals with their attachments first. An attachment that a surviving issue of
// the same project still refers to (in its description or a comment) moves to
// that issue; the others are deleted, and their storage names are returned so
// the caller can remove the files after the commit. Never moving across
// projects keeps a link quoted elsewhere from granting another project access.
func releaseAttachments(tx *sql.Tx, projectID, issueScope string, args ...any) ([]string, error) {
	rows, err := tx.Query(`SELECT id, storage_name FROM attachments
		WHERE issue_id IN (SELECT id FROM issues WHERE `+issueScope+`)`, args...)
	if err != nil {
		return nil, err
	}
	type att struct{ id, storage string }
	var atts []att
	for rows.Next() {
		var a att
		if err := rows.Scan(&a.id, &a.storage); err != nil {
			rows.Close()
			return nil, err
		}
		atts = append(atts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var gone []string
	for _, a := range atts {
		pattern := "%/api/attachments/" + a.id + "/%"
		var keeper string
		qargs := append(append([]any{projectID}, args...), pattern, projectID)
		qargs = append(append(qargs, args...), pattern)
		err := tx.QueryRow(`
			SELECT id FROM issues WHERE project_id = ? AND NOT (`+issueScope+`) AND description LIKE ?
			UNION ALL
			SELECT issue_id FROM comments
			WHERE issue_id IN (SELECT id FROM issues WHERE project_id = ? AND NOT (`+issueScope+`)) AND body LIKE ?
			LIMIT 1`, qargs...).Scan(&keeper)
		switch {
		case err == nil:
			if _, err := tx.Exec(`UPDATE attachments SET issue_id = ? WHERE id = ?`, keeper, a.id); err != nil {
				return nil, err
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`DELETE FROM attachments WHERE id = ?`, a.id); err != nil {
				return nil, err
			}
			gone = append(gone, a.storage)
		default:
			return nil, err
		}
	}
	return gone, nil
}

// pruneGrace keeps recent uploads: a file may be written before its row, and
// an upload pasted into an unsaved form has no issue yet.
const pruneGrace = 24 * time.Hour

// PruneReport lists what PruneUploads removed, or would remove.
type PruneReport struct {
	Files []PrunedFile
	Bytes int64
}

// PrunedFile is one file in uploads/. Reason is "no row" (a file no
// attachment points to) or "unused" (an attachment no issue owns or links).
type PrunedFile struct {
	Name   string
	Bytes  int64
	Reason string
}

// PruneUploads finds uploads left behind by older versions, which kept
// attachments and files when their issue or project was deleted: files in
// uploadsDir with no attachment row, and attachments with no issue that no
// description or comment links. Both must be older than a day. With apply,
// the rows are deleted in one transaction and then the files are removed;
// otherwise nothing changes.
func (s *Store) PruneUploads(uploadsDir string, apply bool) (*PruneReport, error) {
	cutoff := time.Now().Add(-pruneGrace)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rep := &PruneReport{}
	add := func(name, reason string) {
		var size int64
		if fi, err := os.Stat(filepath.Join(uploadsDir, name)); err == nil {
			size = fi.Size()
		}
		rep.Files = append(rep.Files, PrunedFile{Name: name, Bytes: size, Reason: reason})
		rep.Bytes += size
	}

	// Unused attachments.
	rows, err := tx.Query(`
		SELECT a.id, a.storage_name FROM attachments a
		WHERE a.issue_id IS NULL AND a.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM issues i WHERE i.description LIKE '%/api/attachments/' || a.id || '/%')
		  AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.body LIKE '%/api/attachments/' || a.id || '/%')
		ORDER BY a.storage_name`, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	var unusedIDs []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		unusedIDs = append(unusedIDs, id)
		add(name, "unused")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Files no row points to (rows deleted above still count as known).
	known := map[string]bool{}
	rows, err = tx.Query(`SELECT storage_name FROM attachments`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		known[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(uploadsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || known[e.Name()] {
			continue
		}
		if fi, err := e.Info(); err != nil || fi.ModTime().After(cutoff) {
			continue
		}
		add(e.Name(), "no row")
	}

	if !apply {
		return rep, nil
	}
	// Deleting by id alone is safe because the reads above and these deletes
	// share one transaction (one snapshot): if the server links one of these
	// uploads meanwhile, the commit fails with SQLITE_BUSY and nothing goes.
	for _, id := range unusedIDs {
		if _, err := tx.Exec(`DELETE FROM attachments WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The rows are gone; a file that cannot be removed now has no row, so the
	// next run lists it again. Report only what was removed.
	removed := &PruneReport{}
	var failed []string
	for _, f := range rep.Files {
		if err := os.Remove(filepath.Join(uploadsDir, f.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		removed.Files = append(removed.Files, f)
		removed.Bytes += f.Bytes
	}
	if len(failed) > 0 {
		return removed, fmt.Errorf("could not remove %d files (run again to retry): %s", len(failed), strings.Join(failed, "; "))
	}
	return removed, nil
}
