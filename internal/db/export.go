package db

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Store) ExportProjectJSON(projectRef string) (*ExportBundle, error) {
	proj, err := s.GetProject(projectRef)
	if err != nil {
		return nil, err
	}
	states, err := s.ListWorkflowStates(proj.ID)
	if err != nil {
		return nil, err
	}
	labels, err := s.ListLabels(proj.ID)
	if err != nil {
		return nil, err
	}
	cycles, err := s.ListCycles(proj.ID)
	if err != nil {
		return nil, err
	}
	issues, err := s.ListIssues(IssueFilter{ProjectRef: proj.ID})
	if err != nil {
		return nil, err
	}

	expIssues := make([]ExportIssue, 0, len(issues))
	for _, it := range issues {
		lbNames := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			lbNames = append(lbNames, l.Name)
		}
		comments, err := s.ListComments(it.ID)
		if err != nil {
			return nil, err
		}
		ei := ExportIssue{
			Identifier:  it.Identifier,
			Number:      it.Number,
			ParentIdent: it.ParentIdentifier,
			Title:       it.Title,
			Description: it.Description,
			StateName:   it.StateName,
			StateCat:    it.StateCategory,
			Priority:    it.Priority,
			CycleName:   it.CycleName,
			Labels:      lbNames,
			CreatedAt:   it.CreatedAt,
		}
		for _, c := range comments {
			ei.Comments = append(ei.Comments, struct {
				Author    string    `json:"author"`
				Body      string    `json:"body"`
				CreatedAt time.Time `json:"created_at"`
			}{
				Author:    c.AuthorName,
				Body:      c.Body,
				CreatedAt: c.CreatedAt,
			})
		}
		expIssues = append(expIssues, ei)
	}

	return &ExportBundle{
		Version:    1,
		ExportedAt: nowUTC(),
		Project:    *proj,
		States:     states,
		Labels:     labels,
		Cycles:     cycles,
		Issues:     expIssues,
	}, nil
}

func (s *Store) ExportProjectMarkdown(projectRef string) (string, error) {
	b, err := s.ExportProjectJSON(projectRef)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (%s)\n\n", b.Project.Name, b.Project.Key)
	if b.Project.Description != "" {
		fmt.Fprintf(&sb, "%s\n\n", b.Project.Description)
	}
	for _, st := range b.States {
		fmt.Fprintf(&sb, "## %s\n\n", st.Name)
		count := 0
		for _, it := range b.Issues {
			if !strings.EqualFold(it.StateName, st.Name) {
				continue
			}
			count++
			box := "[ ]"
			if st.Category == "completed" {
				box = "[x]"
			}
			fmt.Fprintf(&sb, "- %s **%s**: %s *(Priority: %s)*\n", box, it.Identifier, it.Title, priorityLabel(it.Priority))
			if strings.TrimSpace(it.Description) != "" {
				for _, line := range strings.Split(strings.TrimSpace(it.Description), "\n") {
					fmt.Fprintf(&sb, "  > %s\n", line)
				}
			}
		}
		if count == 0 {
			sb.WriteString("_No issues_\n")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (s *Store) ImportProjectJSON(actorID string, b *ExportBundle) (*Project, error) {
	if b == nil || b.Project.Key == "" || b.Project.Name == "" {
		return nil, fmt.Errorf("%w: invalid export bundle", ErrValidation)
	}
	// Check issue labels before creating anything: import is not one
	// transaction, and CreateIssue refuses unknown labels.
	known := map[string]bool{}
	for _, l := range defaultLabels {
		known[strings.ToLower(l.name)] = true
	}
	for _, l := range b.Labels {
		known[strings.ToLower(l.Name)] = true
	}
	var missing []string
	for _, ei := range b.Issues {
		for _, name := range ei.Labels {
			if !known[strings.ToLower(name)] {
				missing = append(missing, ei.Identifier+": "+strconv.Quote(name))
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: issues use labels the bundle does not define: %s", ErrValidation, strings.Join(missing, ", "))
	}
	proj, err := s.CreateProject(b.Project.Key, b.Project.Name, b.Project.Description)
	if err != nil {
		return nil, err
	}

	for _, l := range b.Labels {
		_, _ = s.CreateLabel(proj.ID, l.Name, l.Color)
	}
	cycleByName := map[string]string{}
	for _, c := range b.Cycles {
		created, err := s.CreateCycle(proj.ID, c.Name, c.Description, c.StartDate, c.EndDate, c.Status)
		if err == nil {
			cycleByName[strings.ToLower(c.Name)] = created.ID
		}
	}

	identMap := map[string]string{} // old identifier -> new identifier
	for _, ei := range b.Issues {
		iss, err := s.CreateIssue(actorID, CreateIssueInput{
			ProjectRef:  proj.ID,
			Title:       ei.Title,
			Description: ei.Description,
			StateID:     ei.StateName,
			Priority:    ei.Priority,
			CycleID:     cycleByName[strings.ToLower(ei.CycleName)],
			LabelIDs:    ei.Labels,
		})
		if err != nil {
			return nil, err
		}
		identMap[ei.Identifier] = iss.Identifier
		for _, cm := range ei.Comments {
			_, _ = s.AddComment(actorID, iss.ID, cm.Body)
		}
	}

	// Second pass to restore parent-child links regardless of ordering
	for _, ei := range b.Issues {
		if ei.ParentIdent != "" {
			newChild := identMap[ei.Identifier]
			newParent := identMap[ei.ParentIdent]
			if newChild != "" && newParent != "" {
				_, _ = s.UpdateIssue(actorID, newChild, UpdateIssueInput{ParentID: &newParent})
			}
		}
	}
	return s.GetProject(proj.ID)
}
