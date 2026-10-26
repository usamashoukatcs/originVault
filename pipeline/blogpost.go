package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// BlogPostResult holds a WordPress-ready blog post generated from a video script.
type BlogPostResult struct {
	Title        string   `json:"title"`
	Slug         string   `json:"slug"`
	Excerpt      string   `json:"excerpt"`
	ContentHTML  string   `json:"content_html"`
	Tags         []string `json:"tags"`
	Categories   []string `json:"categories"`
	PublishedURL string   `json:"published_url,omitempty"`
	PublishedID  int      `json:"published_id,omitempty"`
}

// GenerateBlogPost uses Claude to adapt a YouTube video script into a standalone
// WordPress blog post — written as an article, not a transcript, with real HTML
// markup (headings, paragraphs, a pull quote) ready to publish.
func GenerateBlogPost(apiKey, niche, title, summary, script string) (*BlogPostResult, error) {
	prompt := fmt.Sprintf(`You are a staff writer adapting a YouTube video into a blog post for a general-audience blog (channel niche: %s).

=== SOURCE VIDEO ===
Title: %s
Summary: %s

Full narration script:
%s

=== YOUR TASK ===
Rewrite this as a standalone blog article — NOT a transcript. A reader who never watches the video should get the full story. Keep every real fact, date, name, and figure from the script. Cut spoken-word filler ("I think that's pretty cool", mid-sentence asides) that only works out loud.

Structure:
- A strong opening paragraph (no "In this article..." or "Today we're looking at...")
- 4-6 H2 section headings that break the story into a narrative arc (background, rise, conflict, fall/current state, lesson)
- Short paragraphs (2-4 sentences) — web readers skim
- One blockquote pulled from the most striking real quote or stat in the script
- A closing paragraph with a takeaway, not a recap

=== OUTPUT — return ONLY valid JSON, no markdown, no code block ===
{
  "title": "SEO-friendly article title, under 70 characters, can differ from the video title",
  "slug": "lowercase-hyphenated-url-slug-under-60-chars",
  "excerpt": "1-2 sentence meta description, under 160 characters",
  "content_html": "The full article as HTML. Use <h2>, <p>, <blockquote>, <strong> only. No <html>/<body> wrapper. No inline styles. No markdown syntax mixed in.",
  "tags": ["3-6 lowercase tags"],
  "categories": ["1-2 broad categories, e.g. 'Business History', 'Technology'"]
}

CRITICAL JSON RULES:
- Never use double-quote characters (") inside any JSON string value — use single quotes (') instead.
- Every backslash must be doubled: use \\ not \.
- Newlines inside strings must be written as \n, not actual newlines.`, niche, title, summary, script)

	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 8000,
		Messages:  []claudeMessage{{Role: "user", Content: prompt}},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal failed: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("request create failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Claude API call failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Claude API error %d: %s", resp.StatusCode, string(respBytes))
	}

	var claudeResp claudeResponse
	if err := json.Unmarshal(respBytes, &claudeResp); err != nil || len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("failed to parse Claude response")
	}

	rawText := strings.TrimSpace(claudeResp.Content[0].Text)
	if strings.HasPrefix(rawText, "```") {
		if idx := strings.Index(rawText, "\n"); idx != -1 {
			rawText = rawText[idx+1:]
		}
		if idx := strings.LastIndex(rawText, "```"); idx != -1 {
			rawText = strings.TrimSpace(rawText[:idx])
		}
	}

	var result BlogPostResult
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		repaired := repairJSON(rawText)
		if err2 := json.Unmarshal([]byte(repaired), &result); err2 != nil {
			return nil, fmt.Errorf("failed to parse blog post JSON: %w\nRaw: %s", err, rawText)
		}
	}
	return &result, nil
}
