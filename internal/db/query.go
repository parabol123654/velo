package db

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Limits that keep a query well inside SQLite's own (expression depth,
// LIKE pattern length), so an oversized query is a 400, not a 500.
const (
	maxQueryBytes  = 1000
	maxQueryTokens = 50
)

// queryKeys are the keys ParseIssueQuery understands, in the order the
// unknown-key error lists them.
var queryKeys = []string{"assignee", "state", "label", "priority", "cycle", "project", "parent", "is", "has", "no"}

// ParseIssueQuery turns a query such as
//
//	assignee:me state:"In Progress" label:bug priority:>=3 is:open crash
//
// into an IssueFilter. key:value pairs filter; everything else is free text
// for full-text search. Values with spaces go in double quotes, and so does
// free text that contains a colon ("fix: login"). Keys are case-insensitive.
// An unknown key, a missing value or a key given twice is an error, so a
// typo is reported instead of silently matching too much.
func ParseIssueQuery(s string) (IssueFilter, error) {
	var f IssueFilter
	if len(s) > maxQueryBytes {
		return f, fmt.Errorf("%w: query is longer than %d bytes", ErrValidation, maxQueryBytes)
	}
	tokens, err := splitQuery(s)
	if err != nil {
		return f, err
	}
	if len(tokens) > maxQueryTokens {
		return f, fmt.Errorf("%w: query has more than %d words", ErrValidation, maxQueryTokens)
	}
	seen := map[string]bool{}
	once := func(key string) error {
		if seen[key] {
			return fmt.Errorf("%w: %q given twice", ErrValidation, key)
		}
		seen[key] = true
		return nil
	}
	var text []string
	for _, tok := range tokens {
		key, value, isKey := tok.keyValue()
		if !isKey {
			text = append(text, tok.raw)
			continue
		}
		if !slices.Contains(queryKeys, key) {
			return f, fmt.Errorf("%w: unknown filter %q; use %s, or put text with a colon in quotes", ErrValidation, key+":", strings.Join(queryKeys, ": ")+":")
		}
		if value == "" {
			return f, fmt.Errorf("%w: %q needs a value", ErrValidation, key+":")
		}
		if strings.EqualFold(value, "none") { // the special value, in any case
			value = "none"
		}
		switch key {
		case "assignee", "state", "cycle", "project", "priority":
			if err := once(key + ":"); err != nil {
				return f, err
			}
		}
		switch key {
		case "assignee":
			f.AssigneeID = value
		case "state":
			f.StateID = value
		case "cycle":
			f.CycleID = value
		case "project":
			f.ProjectRef = value
		case "label":
			f.Labels = append(f.Labels, value)
		case "priority":
			op, n, err := parsePriority(value)
			if err != nil {
				return f, err
			}
			f.Priority, f.PriorityOp = &n, op
		case "parent", "has", "no":
			v := value
			if key != "parent" {
				if !strings.EqualFold(value, "parent") {
					return f, fmt.Errorf("%w: %s: takes parent (got %q)", ErrValidation, key, value)
				}
				v = map[string]string{"has": "any", "no": "root"}[key]
			}
			if err := once("parent"); err != nil {
				return f, err
			}
			f.ParentID = v
		case "is":
			switch v := strings.ToLower(value); v {
			case "open", "closed":
				if f.Status != "" && f.Status != v {
					return f, fmt.Errorf("%w: is:open and is:closed cannot both hold", ErrValidation)
				}
				f.Status = v
			case "blocked":
				f.Blocked = true
			case "unblocked":
				f.Unblocked = true
			default:
				return f, fmt.Errorf("%w: is: takes open, closed, blocked or unblocked (got %q)", ErrValidation, value)
			}
			if f.Blocked && f.Unblocked {
				return f, fmt.Errorf("%w: is:blocked and is:unblocked cannot both hold", ErrValidation)
			}
		}
	}
	f.Query = strings.Join(text, " ")
	return f, nil
}

var priorityNames = map[string]int{"none": 0, "low": 1, "medium": 2, "high": 3, "urgent": 4}

func parsePriority(v string) (string, int, error) {
	op := "="
	for _, o := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(v, o) {
			op, v = o, v[len(o):]
			break
		}
	}
	if n, ok := priorityNames[strings.ToLower(v)]; ok {
		return op, n, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 4 {
		return "", 0, fmt.Errorf("%w: priority takes 0-4 or none, low, medium, high, urgent, optionally after >, >=, < or <= (got %q)", ErrValidation, v)
	}
	return op, n, nil
}

// queryToken is one whitespace-separated piece of a query. raw keeps any
// quotes (they matter to free text); value is the part after "key:" without them.
type queryToken struct{ raw string }

// keyValue splits "key:value" or key:"quoted value". A key is ASCII letters
// only, like every real key, so "10:30", "DEV-1" and Vietnamese text such as
// "lỗi:" stay free text; so does a URL ("http://...").
func (t queryToken) keyValue() (key, value string, ok bool) {
	i := strings.IndexByte(t.raw, ':')
	if i <= 0 {
		return "", "", false
	}
	for _, r := range t.raw[:i] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return "", "", false
		}
	}
	if strings.HasPrefix(t.raw[i+1:], "//") {
		return "", "", false
	}
	value = t.raw[i+1:]
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return strings.ToLower(t.raw[:i]), strings.TrimSpace(value), true
}

// splitQuery splits on whitespace outside double quotes.
func splitQuery(s string) ([]queryToken, error) {
	var out []queryToken
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				out = append(out, queryToken{cur.String()})
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("%w: unclosed quote in %q", ErrValidation, s)
	}
	if cur.Len() > 0 {
		out = append(out, queryToken{cur.String()})
	}
	return out, nil
}

// Merge adds the filters of q (from ParseIssueQuery) to f (from separate
// parameters). Setting one field both ways is an error rather than a guess.
func (f *IssueFilter) Merge(q IssueFilter) error {
	conflict := func(name string) error {
		return fmt.Errorf("%w: %s is given both as a parameter and in the query", ErrValidation, name)
	}
	str := func(dst *string, v, name string) error {
		if v == "" {
			return nil
		}
		if *dst != "" {
			return conflict(name)
		}
		*dst = v
		return nil
	}
	for _, e := range []struct {
		dst  *string
		v    string
		name string
	}{
		{&f.ProjectRef, q.ProjectRef, "project"},
		{&f.StateID, q.StateID, "state"},
		{&f.CycleID, q.CycleID, "cycle"},
		{&f.AssigneeID, q.AssigneeID, "assignee"},
		{&f.ParentID, q.ParentID, "parent"},
		{&f.Status, q.Status, "is:open/closed"},
	} {
		if err := str(e.dst, e.v, e.name); err != nil {
			return err
		}
	}
	if q.Priority != nil {
		if f.Priority != nil {
			return conflict("priority")
		}
		f.Priority, f.PriorityOp = q.Priority, q.PriorityOp
	}
	f.Blocked = f.Blocked || q.Blocked
	f.Unblocked = f.Unblocked || q.Unblocked
	if f.Blocked && f.Unblocked {
		return fmt.Errorf("%w: blocked and unblocked cannot both hold", ErrValidation)
	}
	f.Labels = append(f.Labels, q.Labels...)
	if q.Query != "" {
		if f.Query != "" {
			f.Query += " "
		}
		f.Query += q.Query
	}
	return nil
}

// IsEmpty reports whether f filters nothing (Limit and ProjectIDs aside).
func (f IssueFilter) IsEmpty() bool {
	f.Limit, f.ProjectIDs = 0, nil
	return reflect.DeepEqual(f, IssueFilter{})
}

// ResolveMe replaces "assignee:me" with the caller's user ID.
func (f *IssueFilter) ResolveMe(userID string) {
	if strings.EqualFold(f.AssigneeID, "me") {
		f.AssigneeID = userID
	}
}
