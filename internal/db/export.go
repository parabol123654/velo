package db

import (
	"fmt"
	"maps"
	"slices"
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

	links, err := s.projectLinks(proj.ID)
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
		Links:      links,
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
	inBundle := map[string]bool{}
	for _, ei := range b.Issues {
		inBundle[ei.Identifier] = true
	}
	// Refuse here what LinkIssues would refuse later, after the project exists.
	pairs := map[[3]string]bool{}   // "kind a b" with a < b: one link per kind and pair
	blocks := map[string][]string{} // source -> targets of blocks links
	for _, l := range b.Links {
		bad := func(why string) error {
			return fmt.Errorf("%w: link %s %s %s %s", ErrValidation, l.Source, l.Kind, l.Target, why)
		}
		k, ok := linkKinds[strings.ToLower(strings.TrimSpace(l.Kind))]
		switch {
		case !ok:
			return nil, bad("has an unknown kind")
		case !inBundle[l.Source] || !inBundle[l.Target]:
			return nil, bad("names an issue the bundle does not contain")
		case l.Source == l.Target:
			return nil, bad("links an issue to itself")
		}
		src, dst := l.Source, l.Target
		if k.swap {
			src, dst = dst, src
		}
		a, z := min(src, dst), max(src, dst)
		if pairs[[3]string{k.stored, a, z}] {
			return nil, bad("repeats another link")
		}
		pairs[[3]string{k.stored, a, z}] = true
		if k.stored == "blocks" {
			blocks[src] = append(blocks[src], dst)
		}
	}
	if loop := blocksLoop(blocks); loop != nil {
		return nil, fmt.Errorf("%w: links form a blocking loop: %s", ErrValidation, strings.Join(loop, " → "))
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

	// Links last, once every issue exists.
	for _, l := range b.Links {
		if _, _, err := s.LinkIssues(actorID, identMap[l.Source], l.Kind, identMap[l.Target]); err != nil {
			return nil, fmt.Errorf("link %s %s %s: %w", l.Source, l.Kind, l.Target, err)
		}
	}
	return s.GetProject(proj.ID)
}

// projectLinks lists a project's links in stored form, by identifier.
func (s *Store) projectLinks(projectID string) ([]ExportLink, error) {
	rows, err := s.db.Query(`
		SELECT src.identifier, l.kind, dst.identifier
		FROM issue_links l
		JOIN issues src ON src.id = l.source_id
		JOIN issues dst ON dst.id = l.target_id
		WHERE src.project_id = ?
		ORDER BY src.number, dst.number, l.kind`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportLink
	for rows.Next() {
		var l ExportLink
		if err := rows.Scan(&l.Source, &l.Kind, &l.Target); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// blocksLoop returns one loop in a blocks graph, first issue repeated at the
// end, or nil when there is none.
func blocksLoop(edges map[string][]string) []string {
	const visiting, done = 1, 2
	state := map[string]int{}
	var path []string
	var walk func(n string) []string
	walk = func(n string) []string {
		state[n] = visiting
		path = append(path, n)
		for _, m := range edges[n] {
			if state[m] == visiting {
				i := slices.Index(path, m)
				return append(slices.Clone(path[i:]), m)
			}
			if state[m] == 0 {
				if loop := walk(m); loop != nil {
					return loop
				}
			}
		}
		path = path[:len(path)-1]
		state[n] = done
		return nil
	}
	starts := slices.Sorted(maps.Keys(edges))
	for _, n := range starts {
		if state[n] == 0 {
			if loop := walk(n); loop != nil {
				return loop
			}
		}
	}
	return nil
}
