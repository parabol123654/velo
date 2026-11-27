package db

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func priorityLabel(p int) string {
	switch p {
	case 4:
		return "Urgent"
	case 3:
		return "High"
	case 2:
		return "Medium"
	case 1:
		return "Low"
	default:
		return "None"
	}
}

func nullStr(s string) sql.NullString {
	s = strings.TrimSpace(s)
	return sql.NullString{String: s, Valid: s != ""}
}

func buildFTSQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		clean := strings.ReplaceAll(f, `"`, "")
		if clean != "" {
			parts = append(parts, `"`+clean+`"*`)
		}
	}
	return strings.Join(parts, " AND ")
}

func (s *Store) CreateIssue(actorID string, in CreateIssueInput) (*Issue, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title is required", ErrValidation)
	}
	if in.Priority < 0 || in.Priority > 4 {
		return nil, fmt.Errorf("%w: priority must be between 0 and 4", ErrValidation)
	}

	proj, err := s.GetProject(in.ProjectRef)
	if err != nil {
		return nil, fmt.Errorf("%w: project not found", ErrValidation)
	}

	var parentID string
	if strings.TrimSpace(in.ParentRef) != "" {
		parent, err := s.GetIssue(in.ParentRef)
		if err != nil {
			return nil, fmt.Errorf("%w: parent issue not found", ErrValidation)
		}
		if parent.ProjectID != proj.ID {
			return nil, fmt.Errorf("%w: parent issue must belong to same project", ErrValidation)
		}
		parentID = parent.ID
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stateID := strings.TrimSpace(in.StateID)
	if stateID == "" {
		err = tx.QueryRow(
			`SELECT id FROM workflow_states WHERE project_id = ? AND category = 'unstarted' ORDER BY position ASC LIMIT 1`,
			proj.ID,
		).Scan(&stateID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow(
				`SELECT id FROM workflow_states WHERE project_id = ? ORDER BY position ASC LIMIT 1`,
				proj.ID,
			).Scan(&stateID)
		}
		if err != nil {
			return nil, err
		}
	} else if err := tx.QueryRow(
		// Accept either a state ID or a state name (handy for CLI/MCP).
		`SELECT id FROM workflow_states WHERE project_id = ? AND (id = ? OR name = ? COLLATE NOCASE)`,
		proj.ID, stateID, stateID,
	).Scan(&stateID); err != nil {
		return nil, fmt.Errorf("%w: invalid workflow state", ErrValidation)
	}
	assigneeID, _, err := resolveAssignee(tx, actorID, in.AssigneeID)
	if err != nil {
		return nil, err
	}
	cycleID, _, err := resolveCycle(tx, proj.ID, in.CycleID)
	if err != nil {
		return nil, err
	}

	var seq int
	if err := tx.QueryRow(
		`UPDATE projects SET issue_seq = issue_seq + 1 WHERE id = ? RETURNING issue_seq`,
		proj.ID,
	).Scan(&seq); err != nil {
		return nil, err
	}

	var pos float64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1000 FROM issues WHERE project_id = ? AND state_id = ?`, proj.ID, stateID).Scan(&pos)

	issueID := newID()
	ident := fmt.Sprintf("%s-%d", proj.Key, seq)
	now := nowUTC()

	_, err = tx.Exec(`
		INSERT INTO issues (
			id, project_id, number, identifier, parent_id, title, description,
			state_id, priority, assignee_id, creator_id, cycle_id, position, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issueID, proj.ID, seq, ident, nullStr(parentID), title, in.Description,
		stateID, in.Priority, nullStr(assigneeID), actorID, nullStr(cycleID), pos, now, now,
	)
	if err != nil {
		return nil, err
	}

	if err := setLabels(tx, proj.ID, issueID, in.LabelIDs); err != nil {
		return nil, err
	}

	_, err = tx.Exec(
		`INSERT INTO activities (id, issue_id, actor_id, action, via, created_at) VALUES (?, ?, ?, 'created', ?, ?)`,
		newID(), issueID, actorID, s.via, now,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetIssue(issueID)
}

// openBlockersSQL counts unfinished issues that block issue i.
const openBlockersSQL = `(SELECT COUNT(*) FROM issue_links bl
	JOIN issues bi ON bi.id = bl.source_id
	JOIN workflow_states bws ON bws.id = bi.state_id
	WHERE bl.target_id = i.id AND bl.kind = 'blocks' AND bws.category NOT IN ('completed', 'canceled'))`

const baseIssueSelect = `
SELECT
	i.id, i.project_id, p.key, i.number, i.identifier,
	COALESCE(i.parent_id, ''), COALESCE(pi.identifier, ''),
	i.title, i.description,
	i.state_id, ws.name, ws.category, ws.color,
	i.priority,
	COALESCE(i.assignee_id, ''), COALESCE(au.name, ''),
	i.creator_id, cu.name,
	COALESCE(i.cycle_id, ''), COALESCE(cy.name, ''),
	i.position,
	(SELECT COUNT(*) FROM issues sub WHERE sub.parent_id = i.id) AS sub_count,
	(SELECT COUNT(*) FROM issues sub JOIN workflow_states sws ON sws.id = sub.state_id WHERE sub.parent_id = i.id AND sws.category = 'completed') AS sub_done_count,
	(SELECT COUNT(*) FROM comments cm WHERE cm.issue_id = i.id) AS comment_count,
	` + openBlockersSQL + ` AS blocked_by_count,
	i.created_at, i.updated_at
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN workflow_states ws ON ws.id = i.state_id
JOIN users cu ON cu.id = i.creator_id
LEFT JOIN users au ON au.id = i.assignee_id
LEFT JOIN cycles cy ON cy.id = i.cycle_id
LEFT JOIN issues pi ON pi.id = i.parent_id
`

func scanIssue(scanner interface{ Scan(dest ...any) error }) (Issue, error) {
	var it Issue
	err := scanner.Scan(
		&it.ID, &it.ProjectID, &it.ProjectKey, &it.Number, &it.Identifier,
		&it.ParentID, &it.ParentIdentifier,
		&it.Title, &it.Description,
		&it.StateID, &it.StateName, &it.StateCategory, &it.StateColor,
		&it.Priority,
		&it.AssigneeID, &it.AssigneeName,
		&it.CreatorID, &it.CreatorName,
		&it.CycleID, &it.CycleName,
		&it.Position,
		&it.SubIssueCount, &it.SubIssueDoneCount, &it.CommentCount, &it.BlockedByCount,
		&it.CreatedAt, &it.UpdatedAt,
	)
	it.Labels = []Label{}
	return it, err
}

func (s *Store) hydrateLabels(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}
	placeholders := make([]string, len(issues))
	args := make([]any, len(issues))
	idxByID := make(map[string][]int, len(issues))
	for idx, it := range issues {
		placeholders[idx] = "?"
		args[idx] = it.ID
		idxByID[it.ID] = append(idxByID[it.ID], idx)
	}

	q := fmt.Sprintf(`
		SELECT il.issue_id, l.id, l.project_id, l.name, l.color
		FROM issue_labels il
		JOIN labels l ON l.id = il.label_id
		WHERE il.issue_id IN (%s)
		ORDER BY l.name ASC
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var issueID string
		var lb Label
		if err := rows.Scan(&issueID, &lb.ID, &lb.ProjectID, &lb.Name, &lb.Color); err != nil {
			return err
		}
		for _, i := range idxByID[issueID] {
			issues[i].Labels = append(issues[i].Labels, lb)
		}
	}
	return rows.Err()
}

func (s *Store) GetIssue(ref string) (*Issue, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRow(baseIssueSelect+` WHERE i.id = ? OR i.identifier = ? COLLATE NOCASE`, ref, ref)
	it, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	list := []Issue{it}
	if err := s.hydrateLabels(list); err != nil {
		return nil, err
	}
	it = list[0]

	subRows, err := s.db.Query(`
		SELECT i.id, i.identifier, i.title, i.state_id, ws.name, ws.category, ws.color, i.priority,
		       COALESCE(i.assignee_id, ''), COALESCE(au.name, '')
		FROM issues i
		JOIN workflow_states ws ON ws.id = i.state_id
		LEFT JOIN users au ON au.id = i.assignee_id
		WHERE i.parent_id = ?
		ORDER BY i.position ASC, i.number ASC
	`, it.ID)
	if err != nil {
		return nil, err
	}
	defer subRows.Close()

	it.SubIssues = []IssueSummary{}
	for subRows.Next() {
		var sm IssueSummary
		if err := subRows.Scan(
			&sm.ID, &sm.Identifier, &sm.Title, &sm.StateID, &sm.StateName, &sm.StateCategory, &sm.StateColor, &sm.Priority,
			&sm.AssigneeID, &sm.AssigneeName,
		); err != nil {
			return nil, err
		}
		it.SubIssues = append(it.SubIssues, sm)
	}
	if err := subRows.Err(); err != nil {
		return nil, err
	}
	if it.Links, err = s.issueLinks(it.ID); err != nil {
		return nil, err
	}
	return &it, nil
}

func (s *Store) ListIssues(f IssueFilter) ([]Issue, error) {
	var clauses []string
	var args []any

	if strings.TrimSpace(f.ProjectRef) != "" {
		proj, err := s.GetProject(f.ProjectRef)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, "i.project_id = ?")
		args = append(args, proj.ID)
	}
	if f.ProjectIDs != nil {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.ProjectIDs)), ",")
		if marks == "" {
			marks = "NULL"
		}
		clauses = append(clauses, "i.project_id IN ("+marks+")")
		for _, id := range f.ProjectIDs {
			args = append(args, id)
		}
	}
	if f.StateID != "" {
		clauses = append(clauses, "(i.state_id = ? OR ws.name = ? COLLATE NOCASE)")
		args = append(args, f.StateID, f.StateID)
	}
	if f.StateCategory != "" {
		clauses = append(clauses, "ws.category = ?")
		args = append(args, f.StateCategory)
	}
	if f.CycleID == "none" {
		clauses = append(clauses, "i.cycle_id IS NULL")
	} else if f.CycleID != "" {
		clauses = append(clauses, "i.cycle_id IN (SELECT id FROM cycles WHERE id = ? OR name = ? COLLATE NOCASE)")
		args = append(args, f.CycleID, f.CycleID)
	}
	if f.AssigneeID == "none" {
		clauses = append(clauses, "i.assignee_id IS NULL")
	} else if f.AssigneeID != "" {
		clauses = append(clauses, "i.assignee_id IN (SELECT id FROM users WHERE id = ? OR email = ? COLLATE NOCASE)")
		args = append(args, f.AssigneeID, f.AssigneeID)
	}
	if f.Priority != nil {
		clauses = append(clauses, "i.priority = ?")
		args = append(args, *f.Priority)
	}
	if f.ParentID == "root" {
		clauses = append(clauses, "i.parent_id IS NULL")
	} else if f.ParentID != "" {
		clauses = append(clauses, "i.parent_id IN (SELECT id FROM issues WHERE id = ? OR identifier = ? COLLATE NOCASE)")
		args = append(args, f.ParentID, f.ParentID)
	}
	if f.Unblocked {
		clauses = append(clauses, openBlockersSQL+" = 0")
	}
	if f.LabelID != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM issue_labels il JOIN labels lb ON lb.id = il.label_id WHERE il.issue_id = i.id AND (lb.id = ? OR lb.name = ? COLLATE NOCASE))")
		args = append(args, f.LabelID, f.LabelID)
	}
	// NUL would cut the FTS query short; a query of quotes only leaves no FTS
	// terms, and an empty MATCH is a syntax error, so match identifiers only.
	if q := strings.TrimSpace(strings.ReplaceAll(f.Query, "\x00", "")); q != "" {
		if ftsQ := buildFTSQuery(foldD(q)); ftsQ != "" {
			clauses = append(clauses, "(i.rowid IN (SELECT rowid FROM issues_fts WHERE issues_fts MATCH ?) OR i.identifier LIKE ?)")
			args = append(args, ftsQ, "%"+q+"%")
		} else {
			clauses = append(clauses, "i.identifier LIKE ?")
			args = append(args, "%"+q+"%")
		}
	}

	query := baseIssueSelect
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY ws.position ASC, i.position ASC, i.number DESC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Issue{}
	for rows.Next() {
		it, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.hydrateLabels(out); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchIssues runs a full-text search; projectIDs, when non-nil, limits it to those projects.
func (s *Store) SearchIssues(query string, limit int, projectIDs []string) ([]Issue, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Issue{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	return s.ListIssues(IssueFilter{Query: query, Limit: limit, ProjectIDs: projectIDs})
}

func (s *Store) UpdateIssue(actorID, ref string, patch UpdateIssueInput) (*Issue, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cur, err := issueInTx(tx, ref)
	if err != nil {
		return nil, err
	}
	if err := s.applyIssuePatch(tx, actorID, cur, patch); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetIssue(cur.ID)
}

// issueInTx reads an issue through tx, so a change is based on the row as it
// is inside the transaction. (The pool has one connection: reading through
// s.db while tx is open would block.) Labels, sub-issues and links are not loaded.
func issueInTx(tx *sql.Tx, ref string) (*Issue, error) {
	ref = strings.TrimSpace(ref)
	it, err := scanIssue(tx.QueryRow(baseIssueSelect+` WHERE i.id = ? OR i.identifier = ? COLLATE NOCASE`, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// applyIssuePatch writes patch onto cur (read through the same tx) and logs
// an activity per changed field.
func (s *Store) applyIssuePatch(tx *sql.Tx, actorID string, cur *Issue, patch UpdateIssueInput) error {
	var err error
	now := nowUTC()
	logActivity := func(field, oldVal, newVal string) error {
		if oldVal == newVal {
			return nil
		}
		_, err := tx.Exec(
			`INSERT INTO activities (id, issue_id, actor_id, action, field, old_value, new_value, via, created_at)
			 VALUES (?, ?, ?, 'updated', ?, ?, ?, ?, ?)`,
			newID(), cur.ID, actorID, field, oldVal, newVal, s.via, now,
		)
		return err
	}

	title := cur.Title
	if patch.Title != nil {
		t := strings.TrimSpace(*patch.Title)
		if t == "" {
			return fmt.Errorf("%w: title cannot be empty", ErrValidation)
		}
		if err := logActivity("title", cur.Title, t); err != nil {
			return err
		}
		title = t
	}

	desc := cur.Description
	if patch.Description != nil {
		desc = *patch.Description
		// Field only: copying the text into history would double its size.
		if desc != cur.Description {
			if _, err := tx.Exec(
				`INSERT INTO activities (id, issue_id, actor_id, action, field, old_value, new_value, via, created_at)
				 VALUES (?, ?, ?, 'updated', 'description', '', '', ?, ?)`,
				newID(), cur.ID, actorID, s.via, now,
			); err != nil {
				return err
			}
		}
	}

	stateID := cur.StateID
	if patch.StateID != nil {
		rawState := strings.TrimSpace(*patch.StateID)
		var newStateName string
		err := tx.QueryRow(
			`SELECT id, name FROM workflow_states WHERE project_id = ? AND (id = ? OR name = ? COLLATE NOCASE)`,
			cur.ProjectID, rawState, rawState,
		).Scan(&stateID, &newStateName)
		if err != nil {
			return fmt.Errorf("%w: state not found in project", ErrValidation)
		}
		if err := logActivity("state", cur.StateName, newStateName); err != nil {
			return err
		}
	}

	prio := cur.Priority
	if patch.Priority != nil {
		if *patch.Priority < 0 || *patch.Priority > 4 {
			return fmt.Errorf("%w: priority must be 0..4", ErrValidation)
		}
		if err := logActivity("priority", priorityLabel(cur.Priority), priorityLabel(*patch.Priority)); err != nil {
			return err
		}
		prio = *patch.Priority
	}

	assigneeID := cur.AssigneeID
	if patch.AssigneeID != nil {
		var newName string
		if assigneeID, newName, err = resolveAssignee(tx, actorID, *patch.AssigneeID); err != nil {
			return err
		}
		if err := logActivity("assignee", cur.AssigneeName, newName); err != nil {
			return err
		}
	}

	cycleID := cur.CycleID
	if patch.CycleID != nil {
		var newCycleName string
		if cycleID, newCycleName, err = resolveCycle(tx, cur.ProjectID, *patch.CycleID); err != nil {
			return err
		}
		if err := logActivity("cycle", cur.CycleName, newCycleName); err != nil {
			return err
		}
	}

	parentID := cur.ParentID
	if patch.ParentID != nil {
		rawParent := strings.TrimSpace(*patch.ParentID)
		newParentIdent := ""
		if rawParent == "" {
			parentID = ""
		} else {
			var resolvedParent string
			var parentProj, parentIdent string
			if err := tx.QueryRow(`SELECT id, project_id, identifier FROM issues WHERE id = ? OR identifier = ? COLLATE NOCASE`, rawParent, rawParent).Scan(&resolvedParent, &parentProj, &parentIdent); err != nil {
				return fmt.Errorf("%w: parent issue not found", ErrValidation)
			}
			if resolvedParent == cur.ID || parentProj != cur.ProjectID {
				return fmt.Errorf("%w: invalid parent issue", ErrValidation)
			}
			// Walk up from the new parent; meeting this issue means a loop.
			// seen guards against a loop already in older data.
			for up, seen := resolvedParent, map[string]bool{}; up != "" && !seen[up]; {
				if up == cur.ID {
					return fmt.Errorf("%w: parent %s is under %s, which would make a loop", ErrValidation, parentIdent, cur.Identifier)
				}
				seen[up] = true
				if err := tx.QueryRow(`SELECT COALESCE(parent_id, '') FROM issues WHERE id = ?`, up).Scan(&up); err != nil {
					return err
				}
			}
			parentID = resolvedParent
			newParentIdent = parentIdent
		}
		if err := logActivity("parent", cur.ParentIdentifier, newParentIdent); err != nil {
			return err
		}
	}

	pos := cur.Position
	if patch.Position != nil {
		pos = *patch.Position
	} else if stateID != cur.StateID {
		_ = tx.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1000 FROM issues WHERE project_id = ? AND state_id = ?`, cur.ProjectID, stateID).Scan(&pos)
	}

	_, err = tx.Exec(`
		UPDATE issues
		SET title = ?, description = ?, state_id = ?, priority = ?, assignee_id = ?,
		    cycle_id = ?, parent_id = ?, position = ?, updated_at = ?
		WHERE id = ?
	`, title, desc, stateID, prio, nullStr(assigneeID), nullStr(cycleID), nullStr(parentID), pos, now, cur.ID)
	if err != nil {
		return err
	}

	if patch.LabelIDs != nil {
		before, err := labelNames(tx, cur.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM issue_labels WHERE issue_id = ?`, cur.ID); err != nil {
			return err
		}
		if err := setLabels(tx, cur.ProjectID, cur.ID, *patch.LabelIDs); err != nil {
			return err
		}
		after, err := labelNames(tx, cur.ID)
		if err != nil {
			return err
		}
		if err := logActivity("labels", before, after); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) DeleteIssue(ref string) error {
	cur, err := s.GetIssue(ref)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM issues WHERE id = ?`, cur.ID)
	return err
}

func (s *Store) AddComment(authorID, issueRef, body string) (*Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("%w: comment body is required", ErrValidation)
	}
	iss, err := s.GetIssue(issueRef)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	id := newID()
	_, err = s.db.Exec(
		`INSERT INTO comments (id, issue_id, author_id, body, via, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, iss.ID, authorID, body, s.via, now, now,
	)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`UPDATE issues SET updated_at = ? WHERE id = ?`, now, iss.ID)

	var cm Comment
	err = s.db.QueryRow(`
		SELECT c.id, c.issue_id, c.author_id, u.name, c.body, c.via, c.created_at, c.updated_at
		FROM comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?
	`, id).Scan(&cm.ID, &cm.IssueID, &cm.AuthorID, &cm.AuthorName, &cm.Body, &cm.Via, &cm.CreatedAt, &cm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cm, nil
}

func (s *Store) ListComments(issueID string) ([]Comment, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.issue_id, c.author_id, u.name, c.body, c.via, c.created_at, c.updated_at
		FROM comments c JOIN users u ON u.id = c.author_id
		WHERE c.issue_id = ? ORDER BY c.created_at ASC
	`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Comment{}
	for rows.Next() {
		var cm Comment
		if err := rows.Scan(&cm.ID, &cm.IssueID, &cm.AuthorID, &cm.AuthorName, &cm.Body, &cm.Via, &cm.CreatedAt, &cm.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, cm)
	}
	return out, rows.Err()
}

func (s *Store) ListActivities(issueID string) ([]Activity, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.issue_id, a.actor_id, u.name, a.action, a.field, a.old_value, a.new_value, a.via, a.created_at
		FROM activities a JOIN users u ON u.id = a.actor_id
		WHERE a.issue_id = ? ORDER BY a.created_at ASC, a.rowid ASC
	`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Activity{}
	for rows.Next() {
		var ac Activity
		if err := rows.Scan(&ac.ID, &ac.IssueID, &ac.ActorID, &ac.ActorName, &ac.Action, &ac.Field, &ac.OldValue, &ac.NewValue, &ac.Via, &ac.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ac)
	}
	return out, rows.Err()
}

func (s *Store) CreateAttachment(uploaderID, issueID, filename, contentType, storageName string, size int64) (*Attachment, error) {
	att := &Attachment{
		ID:          newID(),
		IssueID:     issueID,
		UploaderID:  uploaderID,
		Filename:    filename,
		ContentType: contentType,
		Size:        size,
		StorageName: storageName,
		CreatedAt:   nowUTC(),
	}
	att.URL = "/api/attachments/" + att.ID + "/" + att.Filename
	_, err := s.db.Exec(
		`INSERT INTO attachments (id, issue_id, uploader_id, filename, content_type, size, storage_name, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		att.ID, nullStr(issueID), att.UploaderID, att.Filename, att.ContentType, att.Size, att.StorageName, att.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return att, nil
}

func (s *Store) GetAttachment(id string) (*Attachment, error) {
	var att Attachment
	var issueID sql.NullString
	err := s.db.QueryRow(
		`SELECT id, issue_id, uploader_id, filename, content_type, size, storage_name, created_at FROM attachments WHERE id = ?`,
		id,
	).Scan(&att.ID, &issueID, &att.UploaderID, &att.Filename, &att.ContentType, &att.Size, &att.StorageName, &att.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if issueID.Valid {
		att.IssueID = issueID.String
	}
	att.URL = "/api/attachments/" + att.ID + "/" + att.Filename
	return &att, nil
}

func (s *Store) getComment(id string) (*Comment, error) {
	var cm Comment
	err := s.db.QueryRow(`
		SELECT c.id, c.issue_id, c.author_id, u.name, c.body, c.via, c.created_at, c.updated_at
		FROM comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?
	`, id).Scan(&cm.ID, &cm.IssueID, &cm.AuthorID, &cm.AuthorName, &cm.Body, &cm.Via, &cm.CreatedAt, &cm.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &cm, nil
}

// ownComment loads a comment the actor may change: their own, or any for admins.
func (s *Store) ownComment(actor *User, id string) (*Comment, error) {
	cm, err := s.getComment(id)
	if err != nil {
		return nil, err
	}
	if cm.AuthorID != actor.ID && actor.Role != "admin" {
		return nil, fmt.Errorf("%w: only the author or an admin can change this comment", ErrForbidden)
	}
	return cm, nil
}

func (s *Store) UpdateComment(actor *User, id, body string) (*Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("%w: comment body is required", ErrValidation)
	}
	if _, err := s.ownComment(actor, id); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE comments SET body = ?, updated_at = ? WHERE id = ?`, body, nowUTC(), id); err != nil {
		return nil, err
	}
	return s.getComment(id)
}

func (s *Store) DeleteComment(actor *User, id string) (*Comment, error) {
	cm, err := s.ownComment(actor, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM comments WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return cm, nil
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// resolveAssignee accepts a user ID, an email, or "me" (the actor). Empty
// means unassigned. Disabled users cannot be assigned.
func resolveAssignee(q rowQuerier, actorID, raw string) (id, name string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	if strings.EqualFold(raw, "me") {
		raw = actorID
	}
	err = q.QueryRow(
		`SELECT id, name FROM users WHERE (id = ? OR email = ? COLLATE NOCASE) AND disabled_at IS NULL`, raw, raw,
	).Scan(&id, &name)
	if err != nil {
		return "", "", fmt.Errorf("%w: assignee not found or disabled", ErrValidation)
	}
	return id, name, nil
}

// resolveCycle accepts a cycle ID or name within the project. Empty means no cycle.
func resolveCycle(q rowQuerier, projectID, raw string) (id, name string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	err = q.QueryRow(
		`SELECT id, name FROM cycles WHERE project_id = ? AND (id = ? OR name = ? COLLATE NOCASE)`, projectID, raw, raw,
	).Scan(&id, &name)
	if err != nil {
		return "", "", fmt.Errorf("%w: cycle not found in project", ErrValidation)
	}
	return id, name, nil
}

// ProjectIDOf returns the project that owns an object, for API token checks.
// kind is one of state, label, cycle, comment, attachment. An attachment not
// yet linked to an issue returns "" (its unguessable URL is the capability).
func (s *Store) ProjectIDOf(kind, id string) (string, error) {
	queries := map[string]string{
		"state":      `SELECT project_id FROM workflow_states WHERE id = ?`,
		"label":      `SELECT project_id FROM labels WHERE id = ?`,
		"cycle":      `SELECT project_id FROM cycles WHERE id = ?`,
		"comment":    `SELECT i.project_id FROM comments c JOIN issues i ON i.id = c.issue_id WHERE c.id = ?`,
		"attachment": `SELECT COALESCE(i.project_id, '') FROM attachments a LEFT JOIN issues i ON i.id = a.issue_id WHERE a.id = ?`,
	}
	q, ok := queries[kind]
	if !ok {
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	var pid string
	err := s.db.QueryRow(q, id).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return pid, err
}

// setLabels adds labels, by ID or case-insensitive name, to an issue. Any
// reference that is not a label of the project fails the whole call, so the
// caller's transaction writes nothing.
func setLabels(tx *sql.Tx, projectID, issueID string, refs []string) error {
	var unknown []string
	for _, ref := range refs {
		var id string
		err := tx.QueryRow(
			`SELECT id FROM labels WHERE project_id = ? AND (id = ? OR name = ? COLLATE NOCASE)`,
			projectID, ref, ref,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if !slices.Contains(unknown, strconv.Quote(ref)) {
				unknown = append(unknown, strconv.Quote(ref))
			}
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO issue_labels (issue_id, label_id) VALUES (?, ?)`, issueID, id); err != nil {
			return err
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: unknown labels: %s (create them first)", ErrValidation, strings.Join(unknown, ", "))
	}
	return nil
}

// labelNames lists an issue's label names, sorted, as "bug, feature".
func labelNames(tx *sql.Tx, issueID string) (string, error) {
	rows, err := tx.Query(`SELECT l.name FROM issue_labels il JOIN labels l ON l.id = il.label_id
		WHERE il.issue_id = ? ORDER BY l.name COLLATE NOCASE`, issueID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		names = append(names, n)
	}
	return strings.Join(names, ", "), rows.Err()
}
