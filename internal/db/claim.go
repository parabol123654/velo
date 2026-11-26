package db

import (
	"fmt"
	"sort"
	"sync"
)

// NextIssue picks the issue an agent (or person) should start next: waiting
// (unstarted), unassigned and not blocked, most urgent first, then board
// order. f narrows it (ProjectRef, LabelID, ProjectIDs). nil means none.
func (s *Store) NextIssue(f IssueFilter) (*Issue, error) {
	f.StateCategory = "unstarted"
	f.AssigneeID = "none"
	f.Unblocked = true
	f.Limit = 0
	issues, err := s.ListIssues(f)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	sort.SliceStable(issues, func(a, b int) bool {
		pa, pb := issues[a].Priority, issues[b].Priority
		if (pa == 0) != (pb == 0) {
			return pb == 0 // "no priority" goes last
		}
		if pa != pb {
			return pa > pb
		}
		if issues[a].Position != issues[b].Position {
			return issues[a].Position < issues[b].Position
		}
		return issues[a].Number < issues[b].Number
	})
	return s.GetIssue(issues[0].ID)
}

// claimMu serializes claims so two agents cannot take the same issue.
var claimMu sync.Mutex

// ClaimIssue assigns the issue to the actor and moves it to the project's
// first started state. It fails with ErrConflict when someone else holds it,
// it is finished, or it is blocked. Claiming an issue you already hold is fine.
func (s *Store) ClaimIssue(actorID, ref string) (*Issue, error) {
	claimMu.Lock()
	defer claimMu.Unlock()

	iss, err := s.GetIssue(ref)
	if err != nil {
		return nil, err
	}
	switch {
	case iss.AssigneeID != "" && iss.AssigneeID != actorID:
		return nil, fmt.Errorf("%w: %s is already assigned to %s", ErrConflict, iss.Identifier, iss.AssigneeName)
	case iss.StateCategory == "completed" || iss.StateCategory == "canceled":
		return nil, fmt.Errorf("%w: %s is already %s", ErrConflict, iss.Identifier, iss.StateName)
	case iss.BlockedByCount > 0:
		return nil, fmt.Errorf("%w: %s is blocked by %d unfinished issue(s)", ErrConflict, iss.Identifier, iss.BlockedByCount)
	}
	patch := UpdateIssueInput{AssigneeID: &actorID}
	if iss.StateCategory != "started" {
		var started string
		if err := s.db.QueryRow(
			`SELECT id FROM workflow_states WHERE project_id = ? AND category = 'started' ORDER BY position ASC LIMIT 1`,
			iss.ProjectID,
		).Scan(&started); err == nil {
			patch.StateID = &started
		}
	}
	return s.UpdateIssue(actorID, iss.ID, patch)
}
