package db

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Importing issues from other trackers (#16). A client (velo import) turns
// GitHub Issues, a Jira or Linear CSV, or a file into an ImportBatch and posts
// it to one project. ImportIssues writes the batch in one transaction, so a
// batch is imported completely or not at all, and a dry run is the same work
// rolled back. import_refs remembers which source item became which issue or
// comment, so running an import again skips what is already there.

// ImportBatch is what one import request carries. Source names where the
// items come from ("github", "jira", ...); external IDs are unique per source
// and project.
type ImportBatch struct {
	Source string        `json:"source"`
	Cycles []ImportCycle `json:"cycles,omitempty"`
	Issues []ImportIssue `json:"issues"`
	Links  []ImportLink  `json:"links,omitempty"`
}

// ImportCycle describes a cycle that issues name; it is created if the
// project has no cycle of that name.
type ImportCycle struct {
	Name      string `json:"name"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
	Status    string `json:"status,omitempty"`
}

type ImportIssue struct {
	ExternalID    string          `json:"external_id"`              // e.g. "owner/repo#12", "PROJ-12"
	URL           string          `json:"url,omitempty"`            // link to the original, put in the footer
	Title         string          `json:"title"`                    //
	Description   string          `json:"description,omitempty"`    // Markdown
	Author        string          `json:"author,omitempty"`         // who opened it, as the source names them
	State         string          `json:"state,omitempty"`          // a state name of the project
	StateCategory string          `json:"state_category,omitempty"` // used when State is empty or not a project state
	Priority      int             `json:"priority,omitempty"`       // 0-4
	Labels        []string        `json:"labels,omitempty"`         // names; missing ones are created
	Assignee      string          `json:"assignee,omitempty"`       // email of a Velo user; otherwise left unassigned
	Cycle         string          `json:"cycle,omitempty"`          // name; created if missing
	Parent        string          `json:"parent,omitempty"`         // external ID of the parent
	CreatedAt     time.Time       `json:"created_at,omitzero"`      // kept on the issue
	Comments      []ImportComment `json:"comments,omitempty"`       //
}

type ImportComment struct {
	ExternalID string    `json:"external_id"`
	Author     string    `json:"author,omitempty"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
}

// ImportLink is "Source Kind Target" by external ID, Kind as LinkIssues takes it.
type ImportLink struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

type ImportResult struct {
	DryRun        bool            `json:"dry_run"`
	Created       int             `json:"created"`
	Skipped       int             `json:"skipped"`  // imported by an earlier run
	Comments      int             `json:"comments"` // comments added
	Links         int             `json:"links"`    // links added
	LabelsCreated []string        `json:"labels_created"`
	CyclesCreated []string        `json:"cycles_created"`
	Unassigned    []string        `json:"unassigned"` // "external ID: assignee" with no enabled Velo user
	Issues        []ImportedIssue `json:"issues"`
}

type ImportedIssue struct {
	ExternalID string `json:"external_id"`
	Identifier string `json:"identifier"`
	Created    bool   `json:"created"`
}

const (
	maxImportIssues   = 5000
	maxImportKeyChars = 200 // source and external IDs
)

// ImportIssues imports a batch into a project, or with dryRun reports what it
// would do and writes nothing. Any problem fails the whole batch.
func (s *Store) ImportIssues(actorID, projectRef string, b *ImportBatch, dryRun bool) (*ImportResult, error) {
	if err := checkImportBatch(b); err != nil {
		return nil, err
	}
	proj, err := s.GetProject(projectRef)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	im := &importer{s: s, tx: tx, actorID: actorID, proj: proj, source: strings.TrimSpace(b.Source), res: &ImportResult{DryRun: dryRun}}
	if err := im.run(b); err != nil {
		return nil, err
	}
	if !dryRun {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return im.res, nil
}

// checkImportBatch checks what needs no database.
func checkImportBatch(b *ImportBatch) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
	}
	if b == nil || strings.TrimSpace(b.Source) == "" {
		return bad("source is required")
	}
	if utf8.RuneCountInString(b.Source) > maxImportKeyChars {
		return bad("source is longer than %d characters", maxImportKeyChars)
	}
	if len(b.Issues) == 0 {
		return bad("the batch has no issues")
	}
	if len(b.Issues) > maxImportIssues {
		return bad("the batch has %d issues; send at most %d at a time", len(b.Issues), maxImportIssues)
	}
	issues := map[string]bool{}
	comments := map[string]bool{}
	for i, it := range b.Issues {
		id := strings.TrimSpace(it.ExternalID)
		switch {
		case id == "":
			return bad("issue %d has no external_id", i+1)
		case utf8.RuneCountInString(id) > maxImportKeyChars:
			return bad("issue %d: external_id is longer than %d characters", i+1, maxImportKeyChars)
		case issues[id]:
			return bad("external_id %s appears twice", id)
		case strings.TrimSpace(it.Title) == "":
			return bad("%s: title is required", id)
		case it.Priority < 0 || it.Priority > 4:
			return bad("%s: priority must be between 0 and 4", id)
		case it.StateCategory != "" && !validStateCategories[it.StateCategory]:
			return bad("%s: unknown state category %q", id, it.StateCategory)
		}
		issues[id] = true
		for _, l := range it.Labels {
			if strings.TrimSpace(l) == "" {
				return bad("%s: empty label name", id)
			}
		}
		for _, c := range it.Comments {
			cid := strings.TrimSpace(c.ExternalID)
			switch {
			case cid == "":
				return bad("%s: a comment has no external_id", id)
			case utf8.RuneCountInString(cid) > maxImportKeyChars:
				return bad("%s: comment external_id is longer than %d characters", id, maxImportKeyChars)
			case comments[cid]:
				return bad("%s: comment %s appears twice", id, cid)
			case strings.TrimSpace(c.Body) == "":
				return bad("%s: comment %s has no body", id, cid)
			}
			comments[cid] = true
		}
	}
	for _, c := range b.Cycles {
		if strings.TrimSpace(c.Name) == "" {
			return bad("a cycle has no name")
		}
		if c.Status != "" && c.Status != "upcoming" && c.Status != "active" && c.Status != "completed" {
			return bad("cycle %q: invalid status %q", c.Name, c.Status)
		}
	}
	for _, l := range b.Links {
		if _, ok := linkKinds[strings.ToLower(strings.TrimSpace(l.Kind))]; !ok {
			return bad("link %s %s %s has an unknown kind", l.Source, l.Kind, l.Target)
		}
		if strings.TrimSpace(l.Source) == strings.TrimSpace(l.Target) {
			return bad("link %s %s %s links an issue to itself", l.Source, l.Kind, l.Target)
		}
	}
	return nil
}

type importer struct {
	s       *Store
	tx      *sql.Tx
	actorID string
	proj    *Project
	source  string
	res     *ImportResult

	issues   map[string]*Issue // external ID -> issue (ID and identifier), new or from an earlier run
	labels   map[string]string // lower-cased name -> label ID
	cycles   map[string]string // lower-cased name -> cycle ID
	states   map[string]string // lower-cased name -> state ID
	category map[string]string // category -> first state ID
}

func (im *importer) run(b *ImportBatch) error {
	if err := im.load(); err != nil {
		return err
	}
	var created []ImportIssue
	for _, it := range b.Issues {
		id := strings.TrimSpace(it.ExternalID)
		issueID, err := im.ref(id)
		if err != nil {
			return err
		}
		if issueID != "" {
			ident, err := im.identifier(issueID)
			if err != nil {
				return err
			}
			im.issues[id] = &Issue{ID: issueID, Identifier: ident}
			im.res.Skipped++
			im.res.Issues = append(im.res.Issues, ImportedIssue{ExternalID: id, Identifier: ident})
			continue
		}
		iss, err := im.createIssue(b, it)
		if err != nil {
			return err
		}
		im.issues[id] = iss
		im.res.Created++
		im.res.Issues = append(im.res.Issues, ImportedIssue{ExternalID: id, Identifier: iss.Identifier, Created: true})
		created = append(created, it)
	}
	if err := im.setParents(created); err != nil {
		return err
	}
	for _, it := range b.Issues {
		if err := im.addComments(it); err != nil {
			return err
		}
	}
	for _, l := range b.Links {
		if err := im.link(l); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) load() error {
	im.issues = map[string]*Issue{}
	im.labels = map[string]string{}
	im.cycles = map[string]string{}
	im.states = map[string]string{}
	im.category = map[string]string{}
	rows, err := im.tx.Query(`SELECT id, name, category FROM workflow_states WHERE project_id = ? ORDER BY position ASC`, im.proj.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, cat string
		if err := rows.Scan(&id, &name, &cat); err != nil {
			rows.Close()
			return err
		}
		if _, ok := im.states[foldKey(name)]; !ok {
			im.states[foldKey(name)] = id
		}
		if _, ok := im.category[cat]; !ok {
			im.category[cat] = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for table, m := range map[string]map[string]string{"labels": im.labels, "cycles": im.cycles} {
		rows, err := im.tx.Query(`SELECT id, name FROM `+table+` WHERE project_id = ?`, im.proj.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			m[foldKey(name)] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// ref returns the issue or comment an earlier run made from an external ID.
func (im *importer) ref(externalID string) (string, error) {
	var id string
	err := im.tx.QueryRow(`SELECT COALESCE(issue_id, comment_id) FROM import_refs WHERE project_id = ? AND source = ? AND external_id = ?`,
		im.proj.ID, im.source, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (im *importer) identifier(issueID string) (string, error) {
	var ident string
	return ident, im.tx.QueryRow(`SELECT identifier FROM issues WHERE id = ?`, issueID).Scan(&ident)
}

func (im *importer) state(it ImportIssue) (string, error) {
	if name := strings.TrimSpace(it.State); name != "" {
		if id, ok := im.states[foldKey(name)]; ok {
			return id, nil
		}
		if it.StateCategory == "" {
			return "", fmt.Errorf("%w: %s: state %q is not a state of %s (create it, or send state_category)", ErrValidation, it.ExternalID, name, im.proj.Key)
		}
	}
	cat := it.StateCategory
	if cat == "" {
		cat = "unstarted"
	}
	if id, ok := im.category[cat]; ok {
		return id, nil
	}
	if it.StateCategory == "" { // no unstarted state: take the first one, as CreateIssue does
		for _, c := range []string{"backlog", "started", "completed", "canceled"} {
			if id, ok := im.category[c]; ok {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s: %s has no %s state", ErrValidation, it.ExternalID, im.proj.Key, cat)
}

func (im *importer) label(name string) (string, error) {
	name = strings.TrimSpace(name)
	if id, ok := im.labels[foldKey(name)]; ok {
		return id, nil
	}
	id := newID()
	if _, err := im.tx.Exec(`INSERT INTO labels (id, project_id, name, color) VALUES (?, ?, ?, '#6366f1')`, id, im.proj.ID, name); err != nil {
		return "", err
	}
	im.labels[foldKey(name)] = id
	im.res.LabelsCreated = append(im.res.LabelsCreated, name)
	return id, nil
}

func (im *importer) cycle(b *ImportBatch, name string) (string, error) {
	name = strings.TrimSpace(name)
	if id, ok := im.cycles[foldKey(name)]; ok {
		return id, nil
	}
	c := ImportCycle{Name: name, Status: "upcoming"}
	for _, bc := range b.Cycles {
		if foldKey(bc.Name) == foldKey(name) {
			c = bc
			if c.Status == "" {
				c.Status = "upcoming"
			}
		}
	}
	id := newID()
	if _, err := im.tx.Exec(`INSERT INTO cycles (id, project_id, name, start_date, end_date, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, im.proj.ID, name, strings.TrimSpace(c.StartDate), strings.TrimSpace(c.EndDate), c.Status, nowUTC()); err != nil {
		return "", err
	}
	im.cycles[foldKey(name)] = id
	im.res.CyclesCreated = append(im.res.CyclesCreated, name)
	return id, nil
}

func (im *importer) createIssue(b *ImportBatch, it ImportIssue) (*Issue, error) {
	id := strings.TrimSpace(it.ExternalID)
	stateID, err := im.state(it)
	if err != nil {
		return nil, err
	}
	var labelIDs []string
	for _, name := range it.Labels {
		lid, err := im.label(name)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(labelIDs, lid) {
			labelIDs = append(labelIDs, lid)
		}
	}
	var cycleID string
	if strings.TrimSpace(it.Cycle) != "" {
		if cycleID, err = im.cycle(b, it.Cycle); err != nil {
			return nil, err
		}
	}
	var assigneeID, unassigned string
	if a := strings.TrimSpace(it.Assignee); a != "" && !strings.EqualFold(a, "me") {
		if assigneeID, _, err = resolveAssignee(im.tx, im.actorID, a); err != nil {
			assigneeID, unassigned = "", a
			im.res.Unassigned = append(im.res.Unassigned, id+": "+a)
		}
	}

	var seq int
	if err := im.tx.QueryRow(`UPDATE projects SET issue_seq = issue_seq + 1 WHERE id = ? RETURNING issue_seq`, im.proj.ID).Scan(&seq); err != nil {
		return nil, err
	}
	var pos float64
	_ = im.tx.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1000 FROM issues WHERE project_id = ? AND state_id = ?`, im.proj.ID, stateID).Scan(&pos)
	iss := &Issue{ID: newID(), Identifier: fmt.Sprintf("%s-%d", im.proj.Key, seq)}
	now := nowUTC()
	created := now
	if !it.CreatedAt.IsZero() {
		created = it.CreatedAt.UTC()
	}
	if _, err := im.tx.Exec(`
		INSERT INTO issues (id, project_id, number, identifier, title, description,
			state_id, priority, assignee_id, creator_id, cycle_id, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		iss.ID, im.proj.ID, seq, iss.Identifier, strings.TrimSpace(it.Title), importDescription(it, unassigned),
		stateID, it.Priority, nullStr(assigneeID), im.actorID, nullStr(cycleID), pos, created, now,
	); err != nil {
		return nil, err
	}
	if err := setLabels(im.tx, im.proj.ID, iss.ID, labelIDs); err != nil {
		return nil, err
	}
	if _, err := im.tx.Exec(`INSERT INTO activities (id, issue_id, actor_id, action, via, created_at) VALUES (?, ?, ?, 'created', ?, ?)`,
		newID(), iss.ID, im.actorID, im.s.via, now); err != nil {
		return nil, err
	}
	if _, err := im.tx.Exec(`INSERT INTO import_refs (project_id, source, external_id, issue_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		im.proj.ID, im.source, id, iss.ID, now); err != nil {
		return nil, err
	}
	return iss, nil
}

// importDescription appends a footer naming the original, who opened it and,
// when no Velo user matched, who it was assigned to.
func importDescription(it ImportIssue, unassigned string) string {
	origin := strings.TrimSpace(it.ExternalID)
	if u := strings.TrimSpace(it.URL); u != "" {
		origin = "[" + origin + "](" + u + ")"
	}
	footer := "Imported from " + origin
	if a := strings.TrimSpace(it.Author); a != "" {
		footer += ", opened by " + a
	}
	if unassigned != "" {
		footer += "; assigned to " + unassigned
	}
	desc := strings.TrimSpace(it.Description)
	if desc != "" {
		desc += "\n\n"
	}
	return desc + "---\n_" + footer + "_"
}

// setParents links new issues to their parents, which may be in this batch or
// from an earlier run. Issues from an earlier run keep their parent.
func (im *importer) setParents(created []ImportIssue) error {
	parent := map[string]string{}
	for _, it := range created {
		if p := strings.TrimSpace(it.Parent); p != "" {
			id := strings.TrimSpace(it.ExternalID)
			if im.issues[p] == nil {
				ref, err := im.ref(p)
				if err != nil {
					return err
				}
				if ref == "" {
					return fmt.Errorf("%w: %s: parent %s is not in this import", ErrValidation, id, p)
				}
				ident, err := im.identifier(ref)
				if err != nil {
					return err
				}
				im.issues[p] = &Issue{ID: ref, Identifier: ident}
			}
			parent[id] = p
		}
	}
	for id := range parent {
		seen := map[string]bool{id: true}
		for p := parent[id]; p != ""; p = parent[p] {
			if seen[p] {
				return fmt.Errorf("%w: %s: parents form a loop", ErrValidation, id)
			}
			seen[p] = true
		}
	}
	for id, p := range parent {
		if _, err := im.tx.Exec(`UPDATE issues SET parent_id = ? WHERE id = ?`, im.issues[p].ID, im.issues[id].ID); err != nil {
			return err
		}
	}
	return nil
}

// addComments adds the comments an earlier run has not, with a first line
// naming the original author and date.
func (im *importer) addComments(it ImportIssue) error {
	iss := im.issues[strings.TrimSpace(it.ExternalID)]
	for _, c := range it.Comments {
		key := "comment:" + strings.TrimSpace(c.ExternalID)
		if ref, err := im.ref(key); err != nil {
			return err
		} else if ref != "" {
			continue
		}
		now := nowUTC()
		created := now
		if !c.CreatedAt.IsZero() {
			created = c.CreatedAt.UTC()
		}
		body := strings.TrimSpace(c.Body)
		if a := strings.TrimSpace(c.Author); a != "" {
			body = "_" + a + ", " + created.Format("2006-01-02") + ":_\n\n" + body
		}
		cid := newID()
		if _, err := im.tx.Exec(`INSERT INTO comments (id, issue_id, author_id, body, via, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			cid, iss.ID, im.actorID, body, im.s.via, created, now); err != nil {
			return err
		}
		if _, err := im.tx.Exec(`INSERT INTO import_refs (project_id, source, external_id, comment_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			im.proj.ID, im.source, key, cid, now); err != nil {
			return err
		}
		im.res.Comments++
	}
	return nil
}

// link adds a link between two imported issues unless it exists already.
func (im *importer) link(l ImportLink) error {
	name := l.Source + " " + l.Kind + " " + l.Target
	ends := [2]*Issue{}
	for i, ext := range []string{strings.TrimSpace(l.Source), strings.TrimSpace(l.Target)} {
		if ends[i] = im.issues[ext]; ends[i] == nil {
			ref, err := im.ref(ext)
			if err != nil {
				return err
			}
			if ref == "" {
				return fmt.Errorf("%w: link %s: %s is not in this import", ErrValidation, name, ext)
			}
			ident, err := im.identifier(ref)
			if err != nil {
				return err
			}
			ends[i] = &Issue{ID: ref, Identifier: ident}
			im.issues[ext] = ends[i]
		}
	}
	kind := strings.ToLower(strings.TrimSpace(l.Kind))
	k := linkKinds[kind]
	src, dst := ends[0], ends[1]
	if k.swap {
		src, dst = dst, src
	}
	var n int
	if err := im.tx.QueryRow(`SELECT COUNT(*) FROM issue_links WHERE kind = ? AND ((source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?))`,
		k.stored, src.ID, dst.ID, dst.ID, src.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if k.stored == "blocks" {
		chain, err := blockChain(im.tx, dst.ID, src.ID)
		if err != nil {
			return err
		}
		if chain != nil {
			return fmt.Errorf("%w: link %s would make a blocking loop: %s → %s", ErrValidation, name, strings.Join(chain, " → "), dst.Identifier)
		}
	}
	if _, err := im.tx.Exec(`INSERT INTO issue_links (id, source_id, target_id, kind, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), src.ID, dst.ID, k.stored, nowUTC()); err != nil {
		return err
	}
	if err := im.s.logLink(im.tx, im.actorID, ends[0], ends[1], "", kind); err != nil {
		return err
	}
	im.res.Links++
	return nil
}

// Summary is the one-line CLI summary of a result.
func (r *ImportResult) Summary() string {
	verb := "Imported"
	if r.DryRun {
		verb = "Would import"
	}
	return verb + " " + strconv.Itoa(r.Created) + " issues (" + strconv.Itoa(r.Skipped) + " already imported), " +
		strconv.Itoa(r.Comments) + " comments, " + strconv.Itoa(r.Links) + " links"
}
