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
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("API %d: %s", resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
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

func (c *Client) ListIssues(project, state, category, query string, priority *int, limit int) ([]db.Issue, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if state != "" {
		q.Set("state", state)
	}
	if category != "" {
		q.Set("category", category)
	}
	if query != "" {
		q.Set("q", query)
	}
	if priority != nil {
		q.Set("priority", strconv.Itoa(*priority))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
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
