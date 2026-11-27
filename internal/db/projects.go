package db

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

var validStateCategories = map[string]bool{
	"backlog":   true,
	"unstarted": true,
	"started":   true,
	"completed": true,
	"canceled":  true,
}

func (s *Store) CreateProject(key, name, description string) (*Project, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if !projectKeyRe.MatchString(key) {
		return nil, fmt.Errorf("%w: project key must be 2-10 uppercase letters/numbers starting with a letter (e.g. DEV)", ErrValidation)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidation)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	p := &Project{
		ID:          newID(),
		Key:         key,
		Name:        name,
		Description: description,
		IssueSeq:    0,
		CreatedAt:   nowUTC(),
	}
	_, err = tx.Exec(
		`INSERT INTO projects (id, key, name, description, issue_seq, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		p.ID, p.Key, p.Name, p.Description, p.CreatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%w: project key %s already exists", ErrConflict, p.Key)
		}
		return nil, err
	}

	defaultStates := []struct {
		name     string
		category string
		color    string
		pos      float64
	}{
		{"Backlog", "backlog", "#94a3b8", 1000},
		{"Todo", "unstarted", "#64748b", 2000},
		{"In Progress", "started", "#f59e0b", 3000},
		{"In Review", "started", "#8b5cf6", 4000},
		{"Done", "completed", "#10b981", 5000},
		{"Canceled", "canceled", "#ef4444", 6000},
	}
	for _, st := range defaultStates {
		if _, err := tx.Exec(
			`INSERT INTO workflow_states (id, project_id, name, category, color, position) VALUES (?, ?, ?, ?, ?, ?)`,
			newID(), p.ID, st.name, st.category, st.color, st.pos,
		); err != nil {
			return nil, err
		}
	}

	for _, lb := range defaultLabels {
		if _, err := tx.Exec(
			`INSERT INTO labels (id, project_id, name, color) VALUES (?, ?, ?, ?)`,
			newID(), p.ID, lb.name, lb.color,
		); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.key, p.name, p.description, p.issue_seq, p.created_at,
		       COUNT(i.id) AS issue_count,
		       COALESCE(SUM(CASE WHEN ws.category NOT IN ('completed', 'canceled') AND i.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS open_count
		FROM projects p
		LEFT JOIN issues i ON i.project_id = p.id
		LEFT JOIN workflow_states ws ON ws.id = i.state_id
		GROUP BY p.id
		ORDER BY p.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.IssueSeq, &p.CreatedAt, &p.IssueCount, &p.OpenIssueCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ref string) (*Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrNotFound
	}
	var p Project
	err := s.db.QueryRow(`
		SELECT p.id, p.key, p.name, p.description, p.issue_seq, p.created_at,
		       COUNT(i.id) AS issue_count,
		       COALESCE(SUM(CASE WHEN ws.category NOT IN ('completed', 'canceled') AND i.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS open_count
		FROM projects p
		LEFT JOIN issues i ON i.project_id = p.id
		LEFT JOIN workflow_states ws ON ws.id = i.state_id
		WHERE p.id = ? OR p.key = ? COLLATE NOCASE
		GROUP BY p.id
	`, ref, ref).Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.IssueSeq, &p.CreatedAt, &p.IssueCount, &p.OpenIssueCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListWorkflowStates(projectID string) ([]WorkflowState, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, category, color, position
		 FROM workflow_states WHERE project_id = ? ORDER BY position ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []WorkflowState{}
	for rows.Next() {
		var st WorkflowState
		if err := rows.Scan(&st.ID, &st.ProjectID, &st.Name, &st.Category, &st.Color, &st.Position); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) CreateWorkflowState(projectID, name, category, color string, position float64) (*WorkflowState, error) {
	name = strings.TrimSpace(name)
	if name == "" || !validStateCategories[category] {
		return nil, fmt.Errorf("%w: valid state name and category required", ErrValidation)
	}
	if color == "" {
		color = "#64748b"
	}
	if position <= 0 {
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1000 FROM workflow_states WHERE project_id = ?`, projectID).Scan(&position)
	}
	st := &WorkflowState{
		ID:        newID(),
		ProjectID: projectID,
		Name:      name,
		Category:  category,
		Color:     color,
		Position:  position,
	}
	_, err := s.db.Exec(
		`INSERT INTO workflow_states (id, project_id, name, category, color, position) VALUES (?, ?, ?, ?, ?, ?)`,
		st.ID, st.ProjectID, st.Name, st.Category, st.Color, st.Position,
	)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) UpdateWorkflowState(id, name, category, color string, position *float64) (*WorkflowState, error) {
	var st WorkflowState
	err := s.db.QueryRow(`SELECT id, project_id, name, category, color, position FROM workflow_states WHERE id = ?`, id).
		Scan(&st.ID, &st.ProjectID, &st.Name, &st.Category, &st.Color, &st.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) != "" {
		st.Name = strings.TrimSpace(name)
	}
	if category != "" {
		if !validStateCategories[category] {
			return nil, fmt.Errorf("%w: invalid state category", ErrValidation)
		}
		st.Category = category
	}
	if color != "" {
		st.Color = color
	}
	if position != nil {
		st.Position = *position
	}
	_, err = s.db.Exec(
		`UPDATE workflow_states SET name = ?, category = ?, color = ?, position = ? WHERE id = ?`,
		st.Name, st.Category, st.Color, st.Position, st.ID,
	)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) ListLabels(projectID string) ([]Label, error) {
	rows, err := s.db.Query(`SELECT id, project_id, name, color FROM labels WHERE project_id = ? ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Label{}
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Color); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) CreateLabel(projectID, name, color string) (*Label, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: label name required", ErrValidation)
	}
	if color == "" {
		color = "#6366f1"
	}
	l := &Label{ID: newID(), ProjectID: projectID, Name: name, Color: color}
	_, err := s.db.Exec(`INSERT INTO labels (id, project_id, name, color) VALUES (?, ?, ?, ?)`, l.ID, l.ProjectID, l.Name, l.Color)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%w: label already exists", ErrConflict)
		}
		return nil, err
	}
	return l, nil
}

func (s *Store) DeleteLabel(id string) error {
	res, err := s.db.Exec(`DELETE FROM labels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListCycles(projectID string) ([]Cycle, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.project_id, c.name, c.description, c.start_date, c.end_date, c.status, c.created_at,
		       COUNT(i.id) AS issue_count,
		       COALESCE(SUM(CASE WHEN ws.category = 'completed' THEN 1 ELSE 0 END), 0) AS completed_count
		FROM cycles c
		LEFT JOIN issues i ON i.cycle_id = c.id
		LEFT JOIN workflow_states ws ON ws.id = i.state_id
		WHERE c.project_id = ?
		GROUP BY c.id
		ORDER BY c.created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Cycle{}
	for rows.Next() {
		var c Cycle
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Description, &c.StartDate, &c.EndDate, &c.Status, &c.CreatedAt, &c.IssueCount, &c.CompletedCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateCycle(projectID, name, description, startDate, endDate, status string) (*Cycle, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: cycle name required", ErrValidation)
	}
	if status == "" {
		status = "active"
	}
	if status != "upcoming" && status != "active" && status != "completed" {
		return nil, fmt.Errorf("%w: invalid cycle status", ErrValidation)
	}
	c := &Cycle{
		ID:          newID(),
		ProjectID:   projectID,
		Name:        name,
		Description: strings.TrimSpace(description),
		StartDate:   strings.TrimSpace(startDate),
		EndDate:     strings.TrimSpace(endDate),
		Status:      status,
		CreatedAt:   nowUTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO cycles (id, project_id, name, description, start_date, end_date, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.Name, c.Description, c.StartDate, c.EndDate, c.Status, c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateCycle changes the fields set in in and keeps the others. It reads
// and writes in one transaction, so concurrent updates cannot undo each other.
func (s *Store) UpdateCycle(id string, in UpdateCycleInput) (*Cycle, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var c Cycle
	err = tx.QueryRow(
		`SELECT id, project_id, name, description, start_date, end_date, status, created_at FROM cycles WHERE id = ?`, id,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.Description, &c.StartDate, &c.EndDate, &c.Status, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		if c.Name = strings.TrimSpace(*in.Name); c.Name == "" {
			return nil, fmt.Errorf("%w: cycle name cannot be empty", ErrValidation)
		}
	}
	if in.Description != nil {
		c.Description = strings.TrimSpace(*in.Description)
	}
	if in.StartDate != nil {
		c.StartDate = strings.TrimSpace(*in.StartDate)
	}
	if in.EndDate != nil {
		c.EndDate = strings.TrimSpace(*in.EndDate)
	}
	if in.Status != nil {
		if st := *in.Status; st != "upcoming" && st != "active" && st != "completed" {
			return nil, fmt.Errorf("%w: invalid cycle status %q (upcoming, active or completed)", ErrValidation, st)
		}
		c.Status = *in.Status
	}
	if _, err := tx.Exec(
		`UPDATE cycles SET name = ?, description = ?, start_date = ?, end_date = ?, status = ? WHERE id = ?`,
		c.Name, c.Description, c.StartDate, c.EndDate, c.Status, c.ID,
	); err != nil {
		return nil, err
	}
	// The same counts ListCycles reports, so the response is complete.
	if err := tx.QueryRow(`
		SELECT COUNT(i.id), COALESCE(SUM(CASE WHEN ws.category = 'completed' THEN 1 ELSE 0 END), 0)
		FROM issues i JOIN workflow_states ws ON ws.id = i.state_id
		WHERE i.cycle_id = ?`, c.ID).Scan(&c.IssueCount, &c.CompletedCount); err != nil {
		return nil, err
	}
	return &c, tx.Commit()
}

// UpdateProject renames a project or changes its description. The key is
// fixed because it is part of every issue identifier.
func (s *Store) UpdateProject(ref, name, description string) (*Project, error) {
	p, err := s.GetProject(ref)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidation)
	}
	if _, err := s.db.Exec(`UPDATE projects SET name = ?, description = ? WHERE id = ?`, name, description, p.ID); err != nil {
		return nil, err
	}
	return s.GetProject(p.ID)
}

// DeleteProject deletes a project with all its issues, comments, states,
// labels, cycles and attachments. It returns the storage names of the deleted
// attachments; the caller removes the files.
func (s *Store) DeleteProject(ref string) (*Project, []string, error) {
	p, err := s.GetProject(ref)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	gone, err := releaseAttachments(tx, p.ID, "project_id = ?", p.ID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, p.ID); err != nil {
		return nil, nil, err
	}
	return p, gone, tx.Commit()
}

// DeleteCycle removes a cycle; its issues stay, with no cycle.
func (s *Store) DeleteCycle(id string) error {
	res, err := s.db.Exec(`DELETE FROM cycles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// defaultLabels are created with every new project.
var defaultLabels = []struct {
	name  string
	color string
}{
	{"bug", "#ef4444"},
	{"feature", "#6366f1"},
	{"improvement", "#0ea5e9"},
	{"chore", "#64748b"},
}
