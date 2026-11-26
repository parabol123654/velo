package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parabol123654/velo/internal/db"
)

type Config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "velo", "config.json")
}

func LoadConfig(flagURL, flagToken string) Config {
	cfg := Config{
		URL:   "http://127.0.0.1:8080",
		Token: "",
	}
	if b, err := os.ReadFile(ConfigPath()); err == nil {
		var saved Config
		if json.Unmarshal(b, &saved) == nil {
			if saved.URL != "" {
				cfg.URL = saved.URL
			}
			if saved.Token != "" {
				cfg.Token = saved.Token
			}
		}
	}
	if envURL := strings.TrimSpace(os.Getenv("VELO_URL")); envURL != "" {
		cfg.URL = envURL
	}
	if envTok := strings.TrimSpace(os.Getenv("VELO_TOKEN")); envTok != "" {
		cfg.Token = envTok
	}
	if strings.TrimSpace(flagURL) != "" {
		cfg.URL = strings.TrimSpace(flagURL)
	}
	if strings.TrimSpace(flagToken) != "" {
		cfg.Token = strings.TrimSpace(flagToken)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return cfg
}

func SaveConfig(cfg Config) error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) do(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	raw, err := c.doRaw(method, path, body)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// doRaw sends an optional JSON body and returns the raw response body.
func (c *Client) doRaw(method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("API %d: %s", resp.StatusCode, apiErr.Error)
		}
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func (c *Client) ListProjects() ([]db.Project, error) {
	var out []db.Project
	return out, c.do("GET", "/api/projects", nil, &out)
}

func (c *Client) CreateProject(key, name, description string) (*db.Project, error) {
	var out db.Project
	err := c.do("POST", "/api/projects", map[string]string{
		"key":         key,
		"name":        name,
		"description": description,
	}, &out)
	return &out, err
}

// IssueQuery filters ListIssues. Empty fields are ignored. Assignee takes an
// ID, email, "me" or "none"; Cycle an ID, name or "none"; Parent an ID,
// identifier or "root"; Label an ID or name.
type IssueQuery struct {
	Project, State, Category, Query string
	Assignee, Cycle, Label, Parent  string
	Priority                        *int
	Limit                           int
	Unblocked                       bool // only issues with no unfinished blockers
}

func (c *Client) ListIssues(f IssueQuery) ([]db.Issue, error) {
	q := url.Values{}
	for k, v := range map[string]string{
		"project": f.Project, "state": f.State, "category": f.Category, "q": f.Query,
		"assignee": f.Assignee, "cycle": f.Cycle, "label": f.Label, "parent": f.Parent,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Priority != nil {
		q.Set("priority", strconv.Itoa(*f.Priority))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Unblocked {
		q.Set("unblocked", "true")
	}
	path := "/api/issues"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out []db.Issue
	return out, c.do("GET", path, nil, &out)
}

type IssueDetail struct {
	Issue      db.Issue      `json:"issue"`
	Comments   []db.Comment  `json:"comments"`
	Activities []db.Activity `json:"activities"`
}

func (c *Client) GetIssueDetail(ref string) (*IssueDetail, error) {
	var out IssueDetail
	err := c.do("GET", "/api/issues/"+url.PathEscape(ref)+"?detail=1", nil, &out)
	return &out, err
}

func (c *Client) CreateIssue(in db.CreateIssueInput) (*db.Issue, error) {
	var out db.Issue
	err := c.do("POST", "/api/issues", in, &out)
	return &out, err
}

func (c *Client) UpdateIssue(ref string, patch db.UpdateIssueInput) (*db.Issue, error) {
	var out db.Issue
	err := c.do("PATCH", "/api/issues/"+url.PathEscape(ref), patch, &out)
	return &out, err
}

func (c *Client) AddComment(ref, body string) (*db.Comment, error) {
	var out db.Comment
	err := c.do("POST", "/api/issues/"+url.PathEscape(ref)+"/comments", map[string]string{"body": body}, &out)
	return &out, err
}

func (c *Client) SearchIssues(query string, limit int) ([]db.Issue, error) {
	q := url.Values{}
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []db.Issue
	return out, c.do("GET", "/api/search?"+q.Encode(), nil, &out)
}

func (c *Client) DeleteIssue(ref string) error {
	return c.do("DELETE", "/api/issues/"+url.PathEscape(ref), nil, nil)
}

func (c *Client) ListUsers() ([]db.User, error) {
	var out []db.User
	return out, c.do("GET", "/api/users", nil, &out)
}

// ProjectBundle is a project with its workflow states, labels and cycles.
type ProjectBundle struct {
	Project db.Project         `json:"project"`
	States  []db.WorkflowState `json:"states"`
	Labels  []db.Label         `json:"labels"`
	Cycles  []db.Cycle         `json:"cycles"`
}

func (c *Client) GetProject(ref string) (*ProjectBundle, error) {
	var out ProjectBundle
	return &out, c.do("GET", "/api/projects/"+url.PathEscape(ref), nil, &out)
}

func (c *Client) CreateLabel(project, name, color string) (*db.Label, error) {
	var out db.Label
	err := c.do("POST", "/api/projects/"+url.PathEscape(project)+"/labels",
		map[string]string{"name": name, "color": color}, &out)
	return &out, err
}

func (c *Client) CreateCycle(project, name, startDate, endDate, status string) (*db.Cycle, error) {
	var out db.Cycle
	err := c.do("POST", "/api/projects/"+url.PathEscape(project)+"/cycles", map[string]string{
		"name": name, "start_date": startDate, "end_date": endDate, "status": status,
	}, &out)
	return &out, err
}

// ExportProject returns the project as Markdown ("markdown") or a JSON bundle ("json").
func (c *Client) ExportProject(ref, format string) ([]byte, error) {
	return c.doRaw("GET", "/api/projects/"+url.PathEscape(ref)+"/export?format="+url.QueryEscape(format), nil)
}

// ImportProject creates a project from a JSON bundle made by ExportProject.
func (c *Client) ImportProject(bundle []byte) (*db.Project, error) {
	raw, err := c.doRaw("POST", "/api/projects/import", bytes.NewReader(bundle))
	if err != nil {
		return nil, err
	}
	var out db.Project
	return &out, json.Unmarshal(raw, &out)
}

// LinkIssues records "ref kind target", kind being blocks, blocked_by,
// relates, duplicates or duplicated_by.
func (c *Client) LinkIssues(ref, kind, target string) (*db.Issue, error) {
	var out db.Issue
	err := c.do("POST", "/api/issues/"+url.PathEscape(ref)+"/links", map[string]string{"kind": kind, "target": target}, &out)
	return &out, err
}

// UnlinkIssues removes every link between two issues.
func (c *Client) UnlinkIssues(ref, target string) (*db.Issue, error) {
	var out db.Issue
	err := c.do("DELETE", "/api/issues/"+url.PathEscape(ref)+"/links/"+url.PathEscape(target), nil, &out)
	return &out, err
}
