package db

import (
	"database/sql"
	"errors"
	"regexp"
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
