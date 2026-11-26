package db

import "time"

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Role         string     `json:"role"` // "admin" | "member"
	PasswordHash string     `json:"-"`
	DisabledAt   *time.Time `json:"disabled_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type APIToken struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

type Invite struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Role      string    `json:"role"`
	CreatedBy string    `json:"created_by"`
	UsedBy    *string   `json:"used_by,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type Project struct {
	ID             string    `json:"id"`
	Key            string    `json:"key"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	IssueSeq       int       `json:"issue_seq"`
	IssueCount     int       `json:"issue_count"`
	OpenIssueCount int       `json:"open_issue_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type WorkflowState struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"project_id"`
	Name      string  `json:"name"`
	Category  string  `json:"category"` // backlog | unstarted | started | completed | canceled
	Color     string  `json:"color"`
	Position  float64 `json:"position"`
}

type Cycle struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	StartDate      string    `json:"start_date"`
	EndDate        string    `json:"end_date"`
	Status         string    `json:"status"` // upcoming | active | completed
	IssueCount     int       `json:"issue_count"`
	CompletedCount int       `json:"completed_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type Label struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
}

type IssueSummary struct {
	ID            string `json:"id"`
	Identifier    string `json:"identifier"`
	Title         string `json:"title"`
	StateID       string `json:"state_id"`
	StateName     string `json:"state_name"`
	StateCategory string `json:"state_category"`
	StateColor    string `json:"state_color"`
	Priority      int    `json:"priority"`
	AssigneeID    string `json:"assignee_id,omitempty"`
	AssigneeName  string `json:"assignee_name,omitempty"`
}

type Issue struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"project_id"`
	ProjectKey        string         `json:"project_key"`
	Number            int            `json:"number"`
	Identifier        string         `json:"identifier"`
	ParentID          string         `json:"parent_id,omitempty"`
	ParentIdentifier  string         `json:"parent_identifier,omitempty"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	StateID           string         `json:"state_id"`
	StateName         string         `json:"state_name"`
	StateCategory     string         `json:"state_category"`
	StateColor        string         `json:"state_color"`
	Priority          int            `json:"priority"` // 0=none, 1=low, 2=medium, 3=high, 4=urgent
	AssigneeID        string         `json:"assignee_id,omitempty"`
	AssigneeName      string         `json:"assignee_name,omitempty"`
	CreatorID         string         `json:"creator_id"`
	CreatorName       string         `json:"creator_name"`
	CycleID           string         `json:"cycle_id,omitempty"`
	CycleName         string         `json:"cycle_name,omitempty"`
	Position          float64        `json:"position"`
	Labels            []Label        `json:"labels"`
	SubIssueCount     int            `json:"sub_issue_count"`
	SubIssueDoneCount int            `json:"sub_issue_done_count"`
	SubIssues         []IssueSummary `json:"sub_issues,omitempty"`
	CommentCount      int            `json:"comment_count"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type Comment struct {
	ID         string    `json:"id"`
	IssueID    string    `json:"issue_id"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Activity struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	ActorID   string    `json:"actor_id"`
	ActorName string    `json:"actor_name"`
	Action    string    `json:"action"`
	Field     string    `json:"field,omitempty"`
	OldValue  string    `json:"old_value,omitempty"`
	NewValue  string    `json:"new_value,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Attachment struct {
	ID          string    `json:"id"`
	IssueID     string    `json:"issue_id,omitempty"`
	UploaderID  string    `json:"uploader_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	StorageName string    `json:"-"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreateIssueInput struct {
	ProjectRef  string   `json:"project_ref"`
	ParentRef   string   `json:"parent_ref,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	StateID     string   `json:"state_id,omitempty"`
	Priority    int      `json:"priority"`
	AssigneeID  string   `json:"assignee_id,omitempty"`
	CycleID     string   `json:"cycle_id,omitempty"`
	LabelIDs    []string `json:"label_ids,omitempty"`
}

type UpdateIssueInput struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	StateID     *string   `json:"state_id,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	AssigneeID  *string   `json:"assignee_id,omitempty"`
	CycleID     *string   `json:"cycle_id,omitempty"`
	ParentID    *string   `json:"parent_id,omitempty"`
	Position    *float64  `json:"position,omitempty"`
	LabelIDs    *[]string `json:"label_ids,omitempty"`
}

type IssueFilter struct {
	ProjectRef    string
	StateID       string
	StateCategory string
	CycleID       string // ID or name, or "none" for issues without a cycle
	AssigneeID    string // ID or email, or "none"; the HTTP layer maps "me" to the caller
	Priority      *int
	ParentID      string // "root" for top-level only, ID or identifier, or "" for all
	LabelID       string
	Query         string
	Limit         int
}

type ExportIssue struct {
	Identifier  string   `json:"identifier"`
	Number      int      `json:"number"`
	ParentIdent string   `json:"parent_identifier,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	StateName   string   `json:"state_name"`
	StateCat    string   `json:"state_category"`
	Priority    int      `json:"priority"`
	CycleName   string   `json:"cycle_name,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Comments    []struct {
		Author    string    `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"comments,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ExportBundle struct {
	Version    int             `json:"version"`
	ExportedAt time.Time       `json:"exported_at"`
	Project    Project         `json:"project"`
	States     []WorkflowState `json:"states"`
	Labels     []Label         `json:"labels"`
	Cycles     []Cycle         `json:"cycles"`
	Issues     []ExportIssue   `json:"issues"`
}
