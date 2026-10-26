package pipeline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WordPressConfig holds credentials for publishing to a self-hosted WordPress site
// via the REST API, authenticated with a WordPress Application Password
// (Users → Profile → Application Passwords — NOT your login password).
type WordPressConfig struct {
	SiteURL     string // e.g. "https://wpscholar.com" — no trailing slash
	Username    string
	AppPassword string
}

// PublishWordPressPost creates a post on the configured WordPress site via
// POST /wp-json/wp/v2/posts. status must be "draft" or "publish".
// Returns the public URL (or edit-link preview for drafts) and the new post ID.
func PublishWordPressPost(cfg WordPressConfig, post BlogPostResult, status string) (string, int, error) {
	if cfg.SiteURL == "" || cfg.Username == "" || cfg.AppPassword == "" {
		return "", 0, fmt.Errorf("WordPress is not configured — set site_url, username, and app_password in config.yaml")
	}
	if status != "draft" && status != "publish" {
		status = "draft"
	}

	body := map[string]any{
		"title":   post.Title,
		"slug":    post.Slug,
		"excerpt": post.Excerpt,
		"content": post.ContentHTML,
		"status":  status,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", 0, fmt.Errorf("marshal failed: %w", err)
	}

	siteURL := strings.TrimRight(cfg.SiteURL, "/")
	req, err := http.NewRequest("POST", siteURL+"/wp-json/wp/v2/posts", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("request create failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	token := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.AppPassword))
	req.Header.Set("Authorization", "Basic "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("WordPress request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read WordPress response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("WordPress API error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		ID   int    `json:"id"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", 0, fmt.Errorf("failed to parse WordPress response: %w", err)
	}

	return result.Link, result.ID, nil
}
