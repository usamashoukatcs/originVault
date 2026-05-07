package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ResearchResult holds content fetched from a real online source.
type ResearchResult struct {
	Title   string
	URL     string
	Excerpt string // plain text excerpt
}

// ResearchConfig holds optional API keys for extended research.
type ResearchConfig struct {
	SerperAPIKey string // optional — for Google news/web results via serper.dev
	NewsAPIKey   string // optional — newsapi.org free tier: 100 req/day
}

// SuggestSubject asks Claude to pick one specific subject from the niche to write about.
func SuggestSubject(apiKey, niche string) (string, error) {
	prompt := fmt.Sprintf(`You are a YouTube channel planner for "%s".

Suggest ONE specific company, invention, or brand to make a video about.
Pick something with a dramatic origin story — founding struggles, near-death moments, stolen ideas, accidental discoveries, or shocking falls.

Return ONLY the subject name. Nothing else. No explanation. No punctuation.
Example outputs: "LEGO" / "Post-it Notes" / "Nintendo" / "BlackBerry" / "Polaroid"`, niche)

	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 20,
		Messages:  []claudeMessage{{Role: "user", Content: prompt}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("Claude API request failed: %w", err)
	}
	defer resp.Body.Close()

	var claudeResp claudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if len(claudeResp.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	subject := strings.TrimSpace(claudeResp.Content[0].Text)
	subject = strings.Trim(subject, `"'`)
	return subject, nil
}

// SuggestSubjectsBatch asks Claude to suggest count unique subjects for the niche,
// excluding any subjects already in the exclude list.
func SuggestSubjectsBatch(apiKey, niche string, count int, exclude []string) ([]string, error) {
	excludeNote := ""
	if len(exclude) > 0 {
		excludeNote = "\n\nDo NOT suggest any of these — already covered:\n"
		for _, e := range exclude {
			excludeNote += "- " + e + "\n"
		}
	}

	prompt := fmt.Sprintf(`You are a YouTube channel planner for "%s".%s

Suggest EXACTLY %d specific companies, inventions, or brands to make videos about.
Pick subjects with dramatic origin stories — founding struggles, near-death moments, stolen ideas, accidental discoveries, or shocking falls.
Each must be unique and compelling.

Return ONLY a JSON array of strings. No explanation. No markdown. No code blocks.
Example: ["LEGO", "Post-it Notes", "Nintendo", "BlackBerry", "Polaroid"]`, niche, excludeNote, count)

	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 300,
		Messages:  []claudeMessage{{Role: "user", Content: prompt}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Claude API request failed: %w", err)
	}
	defer resp.Body.Close()

	var claudeResp claudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from Claude")
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

	var subjects []string
	if err := json.Unmarshal([]byte(rawText), &subjects); err != nil {
		return nil, fmt.Errorf("failed to parse subjects list: %w\nRaw: %s", err, rawText)
	}
	return subjects, nil
}

// ResearchTopic gathers deep, structured research for a subject.
// Fetches full Wikipedia history sections, founder article, key product articles,
// and targeted news searches via Serper.
func ResearchTopic(subject string, cfg ResearchConfig) ([]ResearchResult, error) {
	var results []ResearchResult
	seen := map[string]bool{}

	add := func(r ResearchResult) {
		if r.Excerpt == "" || seen[r.URL] {
			return
		}
		seen[r.URL] = true
		results = append(results, r)
	}

	// 1. Main Wikipedia article — full history sections
	fmt.Printf("  [research] Wikipedia main article: %s\n", subject)
	mainArticles, err := fetchWikipediaDeep(subject)
	if err != nil {
		fmt.Printf("  WARNING: Wikipedia failed for '%s': %v\n", subject, err)
	}
	for _, r := range mainArticles {
		add(r)
	}

	// 2. Related Wikipedia searches — founder, key products, key events
	relatedQueries := []string{
		subject + " founder history",
		subject + " products inventions",
		subject + " acquisition bankruptcy scandal",
	}
	for _, q := range relatedQueries {
		related, err := fetchWikipediaDeep(q)
		if err != nil {
			continue
		}
		for _, r := range related {
			if !seen[r.URL] {
				add(r)
				break // one new article per query is enough
			}
		}
	}

	// 3. Google News RSS — free, no key, returns real BBC/Reuters/AP/Bloomberg articles
	newsQueries := []string{
		subject + " history founded origin",
		subject + " acquisition deal billion",
		subject + " product launch invention",
		subject + " crisis bankruptcy scandal",
	}
	fmt.Printf("  [research] Searching Google News...\n")
	for _, q := range newsQueries {
		newsResults, err := searchGoogleNews(q)
		if err != nil {
			fmt.Printf("  WARNING: Google News failed for '%s': %v\n", q, err)
			continue
		}
		added := 0
		for _, r := range newsResults {
			if !seen[r.URL] {
				add(r)
				added++
				if added >= 2 { // 2 articles per query max
					break
				}
			}
		}
	}

	// 4. NewsAPI — free tier 100 req/day, returns article images directly
	if cfg.NewsAPIKey != "" {
		fmt.Printf("  [research] Searching NewsAPI...\n")
		newsAPIResults, err := searchNewsAPI(subject, cfg.NewsAPIKey)
		if err != nil {
			fmt.Printf("  WARNING: NewsAPI failed: %v\n", err)
		} else {
			for _, r := range newsAPIResults {
				add(r)
			}
		}
	}

	// 5. Serper: targeted Google web searches
	if cfg.SerperAPIKey != "" {
		serperQueries := []string{
			subject + " founding history origin story",
			subject + " major acquisitions deals timeline",
			subject + " key inventions products launched year",
			subject + " financial crisis bankruptcy scandal",
			subject + " current state revenue employees",
		}
		fmt.Printf("  [research] Searching Serper...\n")
		for _, q := range serperQueries {
			serperResults, err := searchSerper(q, cfg.SerperAPIKey)
			if err != nil {
				fmt.Printf("  WARNING: Serper failed for '%s': %v\n", q, err)
				continue
			}
			for _, r := range serperResults {
				add(r)
			}
		}
	}

	fmt.Printf("  [research] Total sources: %d\n", len(results))
	return results, nil
}

// fetchWikipediaDeep fetches a Wikipedia article with full History/Products/Acquisitions sections.
// Returns multiple ResearchResult entries — one for the intro, one per key section.
func fetchWikipediaDeep(subject string) ([]ResearchResult, error) {
	client := &http.Client{}
	ua := "OriginVault/1.0 (youtube automation pipeline; contact@originvault.com)"

	// Step 1: find article title via OpenSearch
	searchURL := fmt.Sprintf(
		"https://en.wikipedia.org/w/api.php?action=opensearch&search=%s&limit=3&format=json",
		url.QueryEscape(subject),
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var openSearch []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&openSearch); err != nil || len(openSearch) < 4 {
		return nil, fmt.Errorf("Wikipedia opensearch parse failed")
	}
	var titles, pageURLs []string
	json.Unmarshal(openSearch[1], &titles)
	json.Unmarshal(openSearch[3], &pageURLs)
	if len(titles) == 0 {
		return nil, fmt.Errorf("no Wikipedia article for: %s", subject)
	}

	var results []ResearchResult

	// Step 2: for the top result, fetch intro + key sections
	title := titles[0]
	pageURL := ""
	if len(pageURLs) > 0 {
		pageURL = pageURLs[0]
	}

	// 2a. Fetch full article text (no exintro limit)
	fullURL := fmt.Sprintf(
		"https://en.wikipedia.org/w/api.php?action=query&titles=%s&prop=extracts&explaintext=1&format=json&redirects=1",
		url.QueryEscape(title),
	)
	req2, _ := http.NewRequest("GET", fullURL, nil)
	req2.Header.Set("User-Agent", ua)
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()

	var extractData map[string]interface{}
	json.NewDecoder(resp2.Body).Decode(&extractData)

	fullText := ""
	if q, ok := extractData["query"].(map[string]interface{}); ok {
		if pages, ok := q["pages"].(map[string]interface{}); ok {
			for _, page := range pages {
				if p, ok := page.(map[string]interface{}); ok {
					fullText, _ = p["extract"].(string)
				}
			}
		}
	}

	if fullText == "" {
		return nil, fmt.Errorf("no content for Wikipedia article: %s", title)
	}

	// 2b. Extract intro (first 2000 chars)
	intro := extractSection(fullText, "", 2000)
	if intro != "" {
		results = append(results, ResearchResult{
			Title:   title + " — Overview",
			URL:     pageURL,
			Excerpt: intro,
		})
	}

	// 2c. Extract key sections with real facts — history, founding, products, acquisitions
	keywordSections := []string{
		"History", "Founding", "Foundation", "Origin", "Early history",
		"Products", "Product history", "Inventions",
		"Acquisitions", "Mergers", "Corporate history",
		"Financial", "Decline", "Bankruptcy", "Crisis",
		"Legacy", "Current",
	}
	for _, section := range keywordSections {
		text := extractSection(fullText, section, 1500)
		if text == "" {
			continue
		}
		results = append(results, ResearchResult{
			Title:   title + " — " + section,
			URL:     pageURL,
			Excerpt: text,
		})
		if len(results) >= 8 { // cap at 8 results per article to keep prompt size manageable
			break
		}
	}

	return results, nil
}

// extractSection pulls text from a named section of a Wikipedia plaintext extract.
// If sectionName is empty, returns the intro (text before first ==heading==).
func extractSection(fullText, sectionName string, maxChars int) string {
	if sectionName == "" {
		// intro: text before the first ==
		idx := strings.Index(fullText, "\n==")
		if idx == -1 {
			idx = len(fullText)
		}
		text := strings.TrimSpace(fullText[:idx])
		return truncateAtSentence(text, maxChars)
	}

	// find the section heading (case-insensitive)
	lower := strings.ToLower(fullText)
	lowerSection := strings.ToLower(sectionName)

	// look for == Section Name == or === Section Name ===
	patterns := []string{
		"\n== " + lowerSection + " ==",
		"\n=== " + lowerSection + " ===",
		"\n== " + lowerSection + "\n",
		"\n=== " + lowerSection + "\n",
	}
	start := -1
	for _, p := range patterns {
		if idx := strings.Index(lower, p); idx != -1 {
			// skip past the heading line
			if nl := strings.Index(fullText[idx+1:], "\n"); nl != -1 {
				start = idx + 1 + nl + 1
			}
			break
		}
	}
	if start == -1 {
		return ""
	}

	// find end of section (next == heading ==)
	rest := fullText[start:]
	end := strings.Index(rest, "\n==")
	if end == -1 {
		end = len(rest)
	}
	text := strings.TrimSpace(rest[:end])
	if len(text) < 50 { // too short to be useful
		return ""
	}
	return truncateAtSentence(text, maxChars)
}

func truncateAtSentence(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	cut := text[:maxChars]
	if idx := strings.LastIndex(cut, ". "); idx > maxChars/2 {
		return cut[:idx+1] + " [...]"
	}
	return cut + " [...]"
}

// searchGoogleNews searches Google News RSS for real articles from BBC, Reuters, AP, Bloomberg etc.
// Completely free — no API key required.
func searchGoogleNews(query string) ([]ResearchResult, error) {
	rssURL := fmt.Sprintf(
		"https://news.google.com/rss/search?q=%s&hl=en-US&gl=US&ceid=US:en",
		url.QueryEscape(query),
	)
	req, _ := http.NewRequest("GET", rssURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OriginVaultBot/1.0)")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Google News RSS request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	rss := string(body)

	// parse <item> blocks from RSS XML
	var results []ResearchResult
	items := strings.Split(rss, "<item>")
	for _, item := range items[1:] { // skip first split (before first <item>)
		title := extractXMLTag(item, "title")
		link := extractXMLTag(item, "link")
		desc := extractXMLTag(item, "description")
		pubDate := extractXMLTag(item, "pubDate")
		source := extractXMLTag(item, "source")

		if title == "" || link == "" {
			continue
		}

		// Google News links are wrapped — follow to get real URL
		realURL := resolveGoogleNewsURL(link)
		if realURL == "" {
			realURL = link
		}

		// skip Google News own domain results
		if strings.Contains(realURL, "news.google.com") {
			continue
		}

		excerpt := cleanHTML(desc)
		if pubDate != "" {
			excerpt = "[" + pubDate + "] " + excerpt
		}
		if source != "" {
			excerpt = "[Source: " + source + "] " + excerpt
		}
		if len(excerpt) > 800 {
			excerpt = excerpt[:800] + " [...]"
		}

		results = append(results, ResearchResult{
			Title:   cleanHTML(title),
			URL:     realURL,
			Excerpt: excerpt,
		})
		if len(results) >= 8 {
			break
		}
	}
	return results, nil
}

// resolveGoogleNewsURL follows a Google News redirect URL to get the real article URL.
func resolveGoogleNewsURL(googleURL string) string {
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse // stop and return current URL
			}
			return nil
		},
	}
	req, _ := http.NewRequest("GET", googleURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OriginVaultBot/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	finalURL := resp.Request.URL.String()
	if strings.Contains(finalURL, "news.google.com") {
		return ""
	}
	return finalURL
}

// extractXMLTag extracts the text content of a simple XML tag.
func extractXMLTag(xml, tag string) string {
	open := "<" + tag
	close := "</" + tag + ">"
	start := strings.Index(xml, open)
	if start == -1 {
		return ""
	}
	// skip to end of opening tag
	gt := strings.Index(xml[start:], ">")
	if gt == -1 {
		return ""
	}
	contentStart := start + gt + 1
	end := strings.Index(xml[contentStart:], close)
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(xml[contentStart : contentStart+end])
}

// cleanHTML strips HTML tags and decodes common entities.
func cleanHTML(s string) string {
	// strip tags
	tagRe := strings.NewReplacer("<![CDATA[", "", "]]>", "")
	s = tagRe.Replace(s)
	// strip remaining HTML tags
	for strings.Contains(s, "<") {
		start := strings.Index(s, "<")
		end := strings.Index(s, ">")
		if end == -1 || end < start {
			break
		}
		s = s[:start] + s[end+1:]
	}
	// decode entities
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
	).Replace(s)
	return strings.TrimSpace(s)
}

// searchNewsAPI searches newsapi.org for articles about the subject.
// Free tier: 100 requests/day. Get key at newsapi.org.
func searchNewsAPI(subject, apiKey string) ([]ResearchResult, error) {
	searchURL := fmt.Sprintf(
		"https://newsapi.org/v2/everything?q=%s&sortBy=relevancy&pageSize=10&language=en&apiKey=%s",
		url.QueryEscape(subject+" history origin"),
		apiKey,
	)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("NewsAPI request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Articles []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			URLToImage  string `json:"urlToImage"`
			Source      struct {
				Name string `json:"name"`
			} `json:"source"`
			PublishedAt string `json:"publishedAt"`
		} `json:"articles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("NewsAPI parse failed: %w", err)
	}
	if result.Status != "ok" {
		return nil, fmt.Errorf("NewsAPI returned status: %s", result.Status)
	}

	var results []ResearchResult
	for _, a := range result.Articles {
		if a.URL == "" || a.Title == "" {
			continue
		}
		excerpt := a.Description
		if a.Source.Name != "" {
			excerpt = "[" + a.Source.Name + "] " + excerpt
		}
		if a.PublishedAt != "" {
			excerpt = "[" + a.PublishedAt[:10] + "] " + excerpt
		}
		results = append(results, ResearchResult{
			Title:   a.Title,
			URL:     a.URL,
			Excerpt: excerpt,
		})
	}
	return results, nil
}

// searchSerper queries Google search via serper.dev and returns top article snippets.
func searchSerper(query, apiKey string) ([]ResearchResult, error) {
	type serperResult struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	}
	type serperResponse struct {
		Organic []serperResult `json:"organic"`
	}

	body, _ := json.Marshal(map[string]interface{}{
		"q":   query,
		"num": 5,
	})

	req, _ := http.NewRequest("POST", "https://google.serper.dev/search", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Serper request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Serper API error %d: %s", resp.StatusCode, string(b))
	}

	var serperResp serperResponse
	if err := json.NewDecoder(resp.Body).Decode(&serperResp); err != nil {
		return nil, fmt.Errorf("failed to parse Serper response: %w", err)
	}

	var results []ResearchResult
	for _, r := range serperResp.Organic {
		if r.Snippet == "" {
			continue
		}
		results = append(results, ResearchResult{
			Title:   r.Title,
			URL:     r.Link,
			Excerpt: r.Snippet,
		})
	}
	return results, nil
}
