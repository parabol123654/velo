package db

import (
	"fmt"
	"regexp"
	"strings"
)

// checklistRe matches a Markdown task line, the same rule as the web UI's
// toggleChecklistLine: "- [ ] text", "* [x] text", indented or not.
var checklistRe = regexp.MustCompile(`^(\s*[-*]\s+)\[([ xX])\](.*)$`)

type checklistItem struct {
	line    int
	text    string
	checked bool
}

// checklistItems lists task lines outside fenced code blocks.
func checklistItems(lines []string) []checklistItem {
	var items []checklistItem
	inFence := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := checklistRe.FindStringSubmatch(ln); m != nil {
			items = append(items, checklistItem{line: i, text: strings.TrimSpace(m[3]), checked: m[2] != " "})
		}
	}
	return items
}

// SetChecklistItem checks or unchecks one task in a Markdown description. The
// task is chosen by 1-based index, or by text: an exact match (ignoring case)
// wins, otherwise the text must be part of exactly one task. It returns the
// new description and the task's text.
func SetChecklistItem(description, text string, index int, checked bool) (string, string, error) {
	lines := strings.Split(description, "\n")
	items := checklistItems(lines)
	if len(items) == 0 {
		return "", "", fmt.Errorf("%w: the description has no checklist (- [ ] items)", ErrValidation)
	}

	var pick *checklistItem
	if index > 0 {
		if index > len(items) {
			return "", "", fmt.Errorf("%w: checklist has %d items, no item %d", ErrValidation, len(items), index)
		}
		pick = &items[index-1]
	} else {
		want := strings.ToLower(strings.TrimSpace(text))
		if want == "" {
			return "", "", fmt.Errorf("%w: give the item text or its index", ErrValidation)
		}
		var exact, partial []*checklistItem
		for i := range items {
			got := strings.ToLower(items[i].text)
			if got == want {
				exact = append(exact, &items[i])
			} else if strings.Contains(got, want) {
				partial = append(partial, &items[i])
			}
		}
		matches := exact
		if len(matches) == 0 {
			matches = partial
		}
		switch len(matches) {
		case 0:
			return "", "", fmt.Errorf("%w: no checklist item matches %q", ErrValidation, text)
		case 1:
			pick = matches[0]
		default:
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = fmt.Sprintf("%q", m.text)
			}
			return "", "", fmt.Errorf("%w: %q matches several items (%s); use more text or the index", ErrValidation, text, strings.Join(names, ", "))
		}
	}

	mark := "[ ]"
	if checked {
		mark = "[x]"
	}
	lines[pick.line] = checklistRe.ReplaceAllString(lines[pick.line], "${1}"+mark+"${3}")
	return strings.Join(lines, "\n"), pick.text, nil
}

// CheckItem sets one checklist item of an issue's description and records a
// short activity ("checked: write tests") instead of the whole description.
func (s *Store) CheckItem(actorID, ref, text string, index int, checked bool) (*Issue, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	iss, err := issueInTx(tx, ref) // read inside tx so a concurrent edit is not overwritten
	if err != nil {
		return nil, err
	}
	desc, item, err := SetChecklistItem(iss.Description, text, index, checked)
	if err != nil {
		return nil, err
	}
	if desc != iss.Description { // otherwise already in that state
		verb := "unchecked"
		if checked {
			verb = "checked"
		}
		now := nowUTC()
		if _, err := tx.Exec(`UPDATE issues SET description = ?, updated_at = ? WHERE id = ?`, desc, now, iss.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`INSERT INTO activities (id, issue_id, actor_id, action, field, old_value, new_value, via, created_at)
			 VALUES (?, ?, ?, 'updated', 'checklist', '', ?, ?, ?)`,
			newID(), iss.ID, actorID, verb+": "+item, s.via, now,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetIssue(iss.ID)
}
