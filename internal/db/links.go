package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// linkKinds maps every accepted kind to its stored form and whether the two
// issues swap places ("A blocked_by B" is stored as "B blocks A").
var linkKinds = map[string]struct {
	stored string
	swap   bool
}{
	"blocks":        {"blocks", false},
	"blocked_by":    {"blocks", true},
	"relates":       {"relates", false},
	"duplicates":    {"duplicates", false},
	"duplicated_by": {"duplicates", true},
}

// LinkIssues records "ref <kind> targetRef", e.g. DEV-1 blocks DEV-2. Both
// issues must be in the same project. It returns the two issue IDs.
func (s *Store) LinkIssues(actorID, ref, kind, targetRef string) (string, string, error) {
	k, ok := linkKinds[strings.ToLower(strings.TrimSpace(kind))]
	if !ok {
		return "", "", fmt.Errorf("%w: kind must be blocks, blocked_by, relates, duplicates or duplicated_by", ErrValidation)
	}
	from, err := s.GetIssue(ref)
	if err != nil {
		return "", "", err
	}
	to, err := s.GetIssue(targetRef)
	if err != nil {
		return "", "", fmt.Errorf("%w: issue %s not found", ErrValidation, targetRef)
	}
	if from.ID == to.ID {
		return "", "", fmt.Errorf("%w: an issue cannot link to itself", ErrValidation)
	}
	if from.ProjectID != to.ProjectID {
		return "", "", fmt.Errorf("%w: linked issues must be in the same project", ErrValidation)
	}
	src, dst := from, to
	if k.swap {
		src, dst = to, from
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	// relates is symmetric; blocks and duplicates in both directions make no sense.
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM issue_links WHERE kind = ? AND ((source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?))`,
		k.stored, src.ID, dst.ID, dst.ID, src.ID,
	).Scan(&n); err != nil {
		return "", "", err
	}
	if n > 0 {
		return "", "", fmt.Errorf("%w: %s and %s are already linked that way", ErrConflict, from.Identifier, to.Identifier)
	}
	if k.stored == "blocks" {
		chain, err := blockChain(tx, dst.ID, src.ID)
		if err != nil {
			return "", "", err
		}
		if chain != nil {
			return "", "", fmt.Errorf("%w: %s already blocks %s through %s, so this link would make a loop",
				ErrConflict, dst.Identifier, src.Identifier, strings.Join(chain, " → "))
		}
	}
	now := nowUTC()
	if _, err := tx.Exec(
		`INSERT INTO issue_links (id, source_id, target_id, kind, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), src.ID, dst.ID, k.stored, now,
	); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO activities (id, issue_id, actor_id, action, field, old_value, new_value, via, created_at)
		 VALUES (?, ?, ?, 'updated', 'link', '', ?, ?, ?)`,
		newID(), from.ID, actorID, strings.ToLower(kind)+" "+to.Identifier, s.via, now,
	); err != nil {
		return "", "", err
	}
	return from.ID, to.ID, tx.Commit()
}

// UnlinkIssues removes every link between two issues, in either direction.
func (s *Store) UnlinkIssues(actorID, ref, targetRef string) (string, string, error) {
	from, err := s.GetIssue(ref)
	if err != nil {
		return "", "", err
	}
	to, err := s.GetIssue(targetRef)
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`DELETE FROM issue_links WHERE (source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?)`,
		from.ID, to.ID, to.ID, from.ID,
	)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", "", fmt.Errorf("%w: %s and %s are not linked", ErrNotFound, from.Identifier, to.Identifier)
	}
	if _, err := tx.Exec(
		`INSERT INTO activities (id, issue_id, actor_id, action, field, old_value, new_value, via, created_at)
		 VALUES (?, ?, ?, 'updated', 'link', ?, '', ?, ?)`,
		newID(), from.ID, actorID, to.Identifier, s.via, nowUTC(),
	); err != nil {
		return "", "", err
	}
	return from.ID, to.ID, tx.Commit()
}

// issueLinks lists an issue's links from its own point of view.
func (s *Store) issueLinks(issueID string) ([]IssueLink, error) {
	rows, err := s.db.Query(`
		SELECT CASE
		         WHEN l.source_id = ? THEN l.kind
		         WHEN l.kind = 'blocks' THEN 'blocked_by'
		         WHEN l.kind = 'duplicates' THEN 'duplicated_by'
		         ELSE l.kind END,
		       o.id, o.identifier, o.title, o.state_id, ws.name, ws.category, ws.color, o.priority,
		       COALESCE(o.assignee_id, ''), COALESCE(au.name, '')
		FROM issue_links l
		JOIN issues o ON o.id = CASE WHEN l.source_id = ? THEN l.target_id ELSE l.source_id END
		JOIN workflow_states ws ON ws.id = o.state_id
		LEFT JOIN users au ON au.id = o.assignee_id
		WHERE l.source_id = ? OR l.target_id = ?
		ORDER BY l.created_at ASC
	`, issueID, issueID, issueID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IssueLink{}
	for rows.Next() {
		var l IssueLink
		sm := &l.Issue
		if err := rows.Scan(&l.Kind, &sm.ID, &sm.Identifier, &sm.Title, &sm.StateID, &sm.StateName, &sm.StateCategory,
			&sm.StateColor, &sm.Priority, &sm.AssigneeID, &sm.AssigneeName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// BlockedIssueIDs returns the issues that issueID blocks. Their blocked state
// changes when issueID is finished, reopened or deleted.
func (s *Store) BlockedIssueIDs(issueID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT target_id FROM issue_links WHERE source_id = ? AND kind = 'blocks'`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// blockChain returns the identifiers along a chain of blocks links from
// "from" to "to" (both included), or nil when "from" does not block "to".
func blockChain(tx *sql.Tx, from, to string) ([]string, error) {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			var chain []string
			for id := cur; id != ""; id = prev[id] {
				var ident string
				if err := tx.QueryRow(`SELECT identifier FROM issues WHERE id = ?`, id).Scan(&ident); err != nil {
					return nil, err
				}
				chain = append([]string{ident}, chain...)
			}
			return chain, nil
		}
		rows, err := tx.Query(`SELECT target_id FROM issue_links WHERE kind = 'blocks' AND source_id = ?`, cur)
		if err != nil {
			return nil, err
		}
		var next []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			next = append(next, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, id := range next {
			if _, seen := prev[id]; !seen {
				prev[id] = cur
				queue = append(queue, id)
			}
		}
	}
	return nil, nil
}
