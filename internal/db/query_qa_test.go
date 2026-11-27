package db

import (
	"errors"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// qaQueryFixture is a hand-built dataset in project DEV (plus one OPS issue):
//
//	DEV-1 "đăng nhập lỗi"   Todo        p4 admin  bug,ui,infra  cycle "Sprint 1"
//	DEV-2 "login slow"      In Progress p2 -      bug           cycle literally named "none"
//	DEV-3 "docs"            Done        p3 bob    -             -
//	DEV-4 "child task"      Canceled    p0 -      bug           parent DEV-1
//	DEV-5 "blocked thing"   Todo        p1 -      -             blocked by DEV-2 (open)
//	DEV-6 "free thing"      Backlog     p2 -      -             blocked by DEV-3 (done)
//	OPS-1 "ops login"       Todo        p4 admin  -
type qaQueryFixture struct {
	st         *Store
	admin, bob *User
}

func newQAQueryFixture(t *testing.T) *qaQueryFixture {
	t.Helper()
	st, admin := viStore(t)
	bob, err := st.CreateUserDirect("bob@velo.dev", "Bob", "secret123", "member")
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := st.GetProject("DEV")
	if _, err := st.CreateProject("OPS", "Ops", ""); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"ui", "infra"} {
		if _, err := st.CreateLabel(dev.ID, l, "#000000"); err != nil {
			t.Fatal(err)
		}
	}
	s1, err := st.CreateCycle(dev.ID, "Sprint 1", "", "", "", "active")
	if err != nil {
		t.Fatal(err)
	}
	none, err := st.CreateCycle(dev.ID, "none", "", "", "", "upcoming")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []CreateIssueInput{
		{ProjectRef: "DEV", Title: "đăng nhập lỗi", StateID: "Todo", Priority: 4, AssigneeID: admin.ID, LabelIDs: []string{"bug", "ui", "infra"}, CycleID: s1.ID},
		{ProjectRef: "DEV", Title: "login slow", StateID: "In Progress", Priority: 2, LabelIDs: []string{"bug"}, CycleID: none.ID},
		{ProjectRef: "DEV", Title: "docs", StateID: "Done", Priority: 3, AssigneeID: bob.ID},
		{ProjectRef: "DEV", Title: "child task", StateID: "Canceled", Priority: 0, LabelIDs: []string{"bug"}, ParentRef: "DEV-1"},
		{ProjectRef: "DEV", Title: "blocked thing", StateID: "Todo", Priority: 1},
		{ProjectRef: "DEV", Title: "free thing", StateID: "Backlog", Priority: 2},
		{ProjectRef: "OPS", Title: "ops login", StateID: "Todo", Priority: 4, AssigneeID: admin.ID},
	} {
		if _, err := st.CreateIssue(admin.ID, in); err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
	}
	for _, l := range [][2]string{{"DEV-5", "DEV-2"}, {"DEV-6", "DEV-3"}} {
		if _, _, err := st.LinkIssues(admin.ID, l[0], "blocked_by", l[1]); err != nil {
			t.Fatalf("link %v: %v", l, err)
		}
	}
	return &qaQueryFixture{st: st, admin: admin, bob: bob}
}

// ids parses q, resolves "me" to the admin, runs it and returns the sorted identifiers.
func (f *qaQueryFixture) ids(t *testing.T, q string) (string, error) {
	t.Helper()
	flt, err := ParseIssueQuery(q)
	if err != nil {
		return "", err
	}
	flt.ResolveMe(f.admin.ID)
	res, err := f.st.ListIssues(flt)
	if err != nil {
		return "", err
	}
	var out []string
	for _, it := range res {
		out = append(out, it.Identifier)
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

// QA: every key against a hand-built dataset; results must be exact.
func TestQAQueryResultsExact(t *testing.T) {
	f := newQAQueryFixture(t)
	for _, tc := range []struct{ q, want string }{
		{"project:DEV", "DEV-1,DEV-2,DEV-3,DEV-4,DEV-5,DEV-6"},
		{"project:dev", "DEV-1,DEV-2,DEV-3,DEV-4,DEV-5,DEV-6"},
		{"label:bug label:ui label:infra", "DEV-1"},
		{"label:bug label:bug label:bug", "DEV-1,DEV-2,DEV-4"},
		{"label:BUG", "DEV-1,DEV-2,DEV-4"},
		{"label:bug label:nope", ""},
		{"priority:>=3 project:DEV", "DEV-1,DEV-3"},
		{"priority:>3", "DEV-1,OPS-1"},
		{"priority:<=0", "DEV-4"},
		{"priority:<1", "DEV-4"},
		{"priority:=2", "DEV-2,DEV-6"},
		{"priority:MEDIUM", "DEV-2,DEV-6"},
		{"priority:<low", "DEV-4"},
		{"priority:>=urgent project:DEV", "DEV-1"},
		{"is:open project:DEV", "DEV-1,DEV-2,DEV-5,DEV-6"},
		{"is:closed", "DEV-3,DEV-4"},
		{"IS:CLOSED is:closed", "DEV-3,DEV-4"},
		{"is:blocked", "DEV-5"},
		{"is:unblocked is:open project:DEV", "DEV-1,DEV-2,DEV-6"},
		{"is:blocked is:closed", ""},
		{"has:parent", "DEV-4"},
		{"no:parent is:closed", "DEV-3"},
		{"HAS:PARENT", "DEV-4"},
		{"has:Parent is:open", ""},
		{"parent:DEV-1", "DEV-4"},
		{"parent:dev-1", "DEV-4"},
		{"cycle:none project:DEV", "DEV-3,DEV-4,DEV-5,DEV-6"},
		{`cycle:"sprint 1"`, "DEV-1"},
		{"assignee:me", "DEV-1,OPS-1"},
		{"assignee:ME project:OPS", "OPS-1"},
		{"assignee:none project:DEV", "DEV-2,DEV-4,DEV-5,DEV-6"},
		{"assignee:BOB@velo.dev", "DEV-3"},
		{`state:"in progress"`, "DEV-2"},
		{"state:todo project:DEV", "DEV-1,DEV-5"},
		{`state:In"Progress"`, ""},
		{"dang nhap is:open", "DEV-1"},
		{"ĐĂNG NHẬP label:bug", "DEV-1"},
		{"đăng nhập is:closed", ""},
		{"login is:open", "DEV-2,OPS-1"},
		{"login project:DEV", "DEV-2"},
		{"DEV-1 is:open", "DEV-1"},
		{"dev-1 label:ui", "DEV-1"},
		{"DEV-1 is:closed", ""},
		{"DEV-3 assignee:bob@velo.dev", "DEV-3"},
		{"assignee:me\tis:open\nlabel:bug\r\nproject:DEV", "DEV-1"},
		{"assignee:me project:OPS", "OPS-1"}, // non-breaking space separates
		{`"login" is:open project:DEV`, "DEV-2"},
		{`"fix: login"`, ""},
		{"🐛 is:open", ""},
		{"🐛:x", ""}, // emoji key is not a key: free text
	} {
		got, err := f.ids(t, tc.q)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.q, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got [%s] want [%s]", tc.q, got, tc.want)
		}
	}
}

// QA: special values ("me", "none") should be as case-insensitive as the
// other enumerated values (is:OPEN, priority:HIGH, has:PARENT all work).
func TestQAQuerySpecialValuesCaseInsensitive(t *testing.T) {
	f := newQAQueryFixture(t)
	for _, pair := range [][2]string{
		{"assignee:NONE project:DEV", "assignee:none project:DEV"},
		{"assignee:None project:DEV", "assignee:none project:DEV"},
		{"cycle:NONE project:DEV", "cycle:none project:DEV"},
	} {
		got, err1 := f.ids(t, pair[0])
		want, err2 := f.ids(t, pair[1])
		if err1 != nil || err2 != nil {
			t.Errorf("%q / %q: %v %v", pair[0], pair[1], err1, err2)
			continue
		}
		if got != want {
			t.Errorf("%q gives [%s] but %q gives [%s]", pair[0], got, pair[1], want)
		}
	}
}

// QA: odd values for every key are a validation error, never a panic or a
// non-validation error.
func TestQAQueryOddValuesAreValidationErrors(t *testing.T) {
	f := newQAQueryFixture(t)
	for _, q := range []string{
		"priority:>=5", "priority:=-1", "priority:>>3", "priority:=>3", "priority:<>2",
		"priority:3.0", "priority:0x3", "priority:-0x1", "priority:99999999999999999999",
		"priority:>", "priority:=", "priority:high!", "priority:3 priority:3",
		`label:""`, `label:" "`, "label:", `state:"In "Progress"`, `state:""`,
		"cycle:", "project:", "parent:", "assignee:", "is:", "has:", "no:",
		"is:OPENED", "is:open is:closed", "is:blocked is:unblocked", "IS:BLOCKED is:Unblocked",
		"has:label", "no:assignee", "has:parent parent:DEV-1", "no:parent has:parent",
		"assignee:me ASSIGNEE:me", "state:Todo State:Todo", "cycle:none cycle:none", "project:DEV project:DEV",
		"iss:x", "ASSIGNE:me",
		`"unclosed`, `label:"bug`, `a "b c`,
		"is:open\x00", "priority:3\x00",
	} {
		_, err := f.ids(t, q)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%q: want ErrValidation, got %v", q, err)
		}
	}
}

// QA: these look odd but are not keys, so they must not error.
func TestQAQueryNonKeysAreText(t *testing.T) {
	f := newQAQueryFixture(t)
	for _, q := range []string{
		"label", "10:30", ":foo", "a1:b", "dev-1:x", "_x:y", "🐛:x", "\x00", "\x00is:open",
		"assignee：me", // fullwidth colon
		"ñame:x", "日本:x", "lỗi:đăng nhập", "ñ:x", "http://example.com/a:b", "https://x.y/?q=assignee:me",
		`""`, `"" ""`, `" "`, "\t\n\r", strings.Repeat(" ", 1000),
	} {
		if _, err := f.ids(t, q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

// QA: project:OTHER combined with a ProjectIDs restriction (a project-scoped
// token) must not return the other project's issues.
func TestQAQueryProjectRestriction(t *testing.T) {
	f := newQAQueryFixture(t)
	dev, _ := f.st.GetProject("DEV")
	for _, q := range []string{"project:OPS", "project:ops login", "OPS-1", "assignee:me", "login", "is:open priority:>=4"} {
		flt, err := ParseIssueQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		flt.ResolveMe(f.admin.ID)
		flt.ProjectIDs = []string{dev.ID}
		res, err := f.st.ListIssues(flt)
		if err != nil {
			t.Errorf("%q: %v", q, err)
			continue
		}
		for _, it := range res {
			if it.ProjectKey != "DEV" {
				t.Errorf("%q leaked %s", q, it.Identifier)
			}
		}
	}
}

// QA: Merge of separate parameters with a query.
func TestQAQueryMergeConflicts(t *testing.T) {
	p := 3
	for _, tc := range []struct {
		base IssueFilter
		q    string
		ok   bool
	}{
		{IssueFilter{ParentID: "root"}, "has:parent", false},
		{IssueFilter{ParentID: "DEV-1"}, "no:parent", false},
		{IssueFilter{ParentID: "DEV-1"}, "parent:DEV-1", false},
		{IssueFilter{Unblocked: true}, "is:blocked", false},
		{IssueFilter{Unblocked: true}, "is:unblocked", true},
		{IssueFilter{Priority: &p}, "priority:>=1", false},
		{IssueFilter{AssigneeID: "me"}, "assignee:me", false},
		{IssueFilter{ProjectRef: "DEV"}, "project:OPS", false},
		{IssueFilter{CycleID: "none"}, "cycle:none", false},
		{IssueFilter{LabelID: "bug"}, "label:ui label:infra", true},
		{IssueFilter{StateCategory: "completed"}, "is:open", true},
		{IssueFilter{Query: "crash"}, "login is:open", true},
	} {
		f := tc.base
		q, err := ParseIssueQuery(tc.q)
		if err != nil {
			t.Fatal(err)
		}
		err = f.Merge(q)
		if tc.ok && err != nil {
			t.Errorf("%+v + %q: %v", tc.base, tc.q, err)
		}
		if !tc.ok && !errors.Is(err, ErrValidation) {
			t.Errorf("%+v + %q: want ErrValidation, got %v", tc.base, tc.q, err)
		}
	}
}

// QA: huge queries. Every outcome must be results or ErrValidation; a raw
// SQLite error would be a 500 at the HTTP layer.
func TestQAQueryHugeInputs(t *testing.T) {
	f := newQAQueryFixture(t)
	words := func(n int, format func(i int) string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(format(i))
			b.WriteByte(' ')
		}
		return b.String()
	}
	for name, q := range map[string]string{
		"5000 free words":         words(5000, func(i int) string { return "word" }),
		"5000 distinct words":     words(5000, func(i int) string { return "w" + strings.Repeat("x", i%7) }),
		"1MB single token":        strings.Repeat("a", 1<<20),
		"1MB label value":         "label:" + strings.Repeat("b", 1<<20),
		"3000 is:open":            words(3000, func(int) string { return "is:open" }),
		"1500 is:blocked":         words(1500, func(int) string { return "is:blocked" }),
		"1200 label tokens":       words(1200, func(int) string { return "label:bug" }),
		"20000 label tokens":      words(20000, func(int) string { return "label:bug" }),
		"emoji soup":              strings.Repeat("🐛💥 ", 3000),
		"vietnamese 3000 words":   words(3000, func(int) string { return "đăng" }),
		"label tokens plus words": words(600, func(int) string { return "label:bug login" }),
	} {
		_, err := f.ids(t, q)
		if err != nil && !errors.Is(err, ErrValidation) {
			t.Errorf("%s: non-validation error (would be HTTP 500): %.200v", name, err)
		}
	}
}

// QA: random token soup never panics and never yields a non-validation,
// non-not-found error.
func TestQAQueryFuzz(t *testing.T) {
	f := newQAQueryFixture(t)
	pieces := []string{
		"assignee:", "state:", "label:", "priority:", "cycle:", "project:", "parent:", "is:", "has:", "no:",
		"me", "none", "open", "closed", "blocked", "unblocked", "parent", ">=", "<", ">", "=", "3", "-1", "high",
		`"`, `""`, ":", " ", "\t", "\n", "\x00", "DEV-1", "DEV", "đăng", "nhập", "🐛", "ñ", "*", "(", ")", "^", "-",
		"AND", "OR", "NOT", "NEAR", "'", "%", "_", "\\", "In Progress", "Todo", "bug",
	}
	r := rand.New(rand.NewSource(17))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for j := r.Intn(12); j >= 0; j-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		q := b.String()
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("%q: panic %v", q, p)
				}
			}()
			_, err := f.ids(t, q)
			if err != nil && !errors.Is(err, ErrValidation) && !errors.Is(err, ErrNotFound) {
				t.Errorf("%q: %v", q, err)
			}
		}()
	}
}

// QA: SearchIssues (the store entry point) with filters only, and its cap.
func TestQASearchIssuesWithFilters(t *testing.T) {
	f := newQAQueryFixture(t)
	for i := 0; i < 60; i++ {
		mustIssue(t, f.st, f.admin.ID, "bulk", "")
	}
	res, err := f.st.SearchIssues("is:open", 0, nil)
	if err != nil || len(res) != 20 {
		t.Fatalf("default limit: %d %v", len(res), err)
	}
	if res, _ = f.st.SearchIssues("is:open", 500, nil); len(res) != 20 {
		t.Errorf("limit 500 should fall back to 20, got %d", len(res))
	}
	if res, _ = f.st.SearchIssues("bulk", 50, nil); len(res) != 50 {
		t.Errorf("limit 50: got %d", len(res))
	}
	if res, err = f.st.SearchIssues("  \t\n ", 20, nil); err != nil || len(res) != 0 {
		t.Errorf("blank search: %d %v", len(res), err)
	}
	if _, err = f.st.SearchIssues("asignee:me", 20, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("typo: %v", err)
	}
}

// QA: the size limits are exact and reported as validation errors.
func TestQAQueryLimitsBoundaries(t *testing.T) {
	f := newQAQueryFixture(t)
	words := func(n int, w string) string { return strings.TrimSpace(strings.Repeat(w+" ", n)) }
	for _, tc := range []struct {
		name, q string
		ok      bool
	}{
		{"50 words", words(50, "x"), true},
		{"51 words", words(51, "x"), false},
		{"50 label tokens", words(50, "label:bug"), true},
		{"50 is:open", words(50, "is:open"), true},
		{"1000 bytes", strings.Repeat("a", 1000), true},
		{"1001 bytes", strings.Repeat("a", 1001), false},
		{"1000 bytes of 2-byte runes", strings.Repeat("đ", 500), true},
		{"1002 bytes of 3-byte runes", strings.Repeat("ạ", 334), false},
		{"quoted phrase counts as one word", `state:"` + words(60, "x") + `"`, true},
		{"50 words plus whitespace padding", "  \t" + words(50, "y") + "\n\n  ", true},
	} {
		_, err := f.ids(t, tc.q)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", tc.name, err)
		}
	}
}
