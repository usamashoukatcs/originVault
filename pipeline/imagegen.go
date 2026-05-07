package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// apiClient is shared for all external API calls — 60 second timeout prevents hangs.
var apiClient = &http.Client{Timeout: 60 * time.Second}

// wikiPhotoCache caches successfully downloaded Wikipedia photo bytes (title → image bytes).
// Prevents re-downloading the same image when multiple cues reference the same person/company.
var wikiPhotoCache = map[string][]byte{}

// FootageConfig holds all API keys for the multi-source clip system.
type FootageConfig struct {
	PexelsAPIKey      string
	PixabayAPIKey     string // Free at pixabay.com/api/docs — 100 req/min, no credit card
	UnsplashAccessKey string // Free at unsplash.com/developers — 50 req/hour, high-quality photos
	FalAPIKey         string // fal.ai — Kling v1.6 AI video generation, ~$0.10/5s clip
	ImagenAPIKey      string // Google AI Studio — free at aistudio.google.com
	DALLEAPIKey       string // OpenAI — same key as TTS
}

// ClipSource records where a clip came from for sources.txt attribution.
type ClipSource struct {
	Source string // "pexels", "imagen3", "gpt-image-1", "placeholder"
	URL    string // Pexels page URL, or "AI Generated (Imagen 3)", etc.
}

// GetBestClipPublic is the exported wrapper around getBestClip, used by the dashboard server
// to auto-download clips for cues that the user left unselected.
func GetBestClipPublic(cfg FootageConfig, cue ClipCue, outputPath string, portrait bool, research []ResearchResult) (ClipSource, error) {
	return getBestClip(cfg, cue, outputPath, portrait, research)
}

// getBestClip is the master function that picks the best visual for a clip cue.
//
// Priority order:
//  0. News screenshot — real article og:image + headline chyron (if cue.NewsRef set)
//  1. Person photo    — Wikipedia photo + name chyron (Ken Burns if animate=true)
//  2. Product photo   — Wikipedia photo of specific device/model (Ken Burns if animate=true)
//  3. Company image   — Wikipedia/Clearbit logo (Ken Burns if animate=true)
//  4. Pexels video    — real footage; score>=1 for animated scenes, >=2 for static
//  5. gpt-image-1     — last resort for abstract concepts with no real-world photo
//  6. Pexels forced   — whatever Pexels has regardless of score
func getBestClip(cfg FootageConfig, cue ClipCue, outputPath string, portrait bool, research []ResearchResult) (ClipSource, error) {
	keyword := cue.Keyword
	orientation := "landscape"
	if portrait {
		orientation = "portrait"
	}

	// --- Step 0a: Stat card — generated infographic for numeric facts (highest priority) ---
	// When Claude marks a cue with a stat field (number/percentage/dollar), skip all image
	// search and generate a clean infographic directly. Always relevant, always sharp.
	if cue.Stat != nil && cue.Stat.BigNumber != "" {
		duration := estimateClipDuration(outputPath)
		if err := generateStatCard(cue.Stat, outputPath, duration, portrait); err == nil {
			fmt.Printf("    [stat-card] %s — %s\n", cue.Stat.BigNumber, cue.Stat.Label)
			return ClipSource{Source: "stat-card", URL: ""}, nil
		} else {
			fmt.Printf("    stat-card failed: %v\n", err)
		}
	}

	// --- Step 0b: News article screenshot (og:image + headline chyron) ---
	if cue.NewsRef > 0 && cue.NewsRef <= len(research) {
		article := research[cue.NewsRef-1]
		imgPath := outputPath + "_news.jpg"
		if err := fetchArticleOGImage(article.URL, imgPath); err == nil {
			duration := estimateClipDuration(outputPath)
			if kbErr := imageToVideoClipWithNewsChyron(imgPath, outputPath, duration, portrait, article.Title, extractDomain(article.URL)); kbErr == nil {
				fmt.Printf("    [news-screenshot] %s\n", extractDomain(article.URL))
				return ClipSource{Source: "news", URL: article.URL}, nil
			}
			os.Remove(imgPath)
		} else {
			fmt.Printf("    news og:image failed: %v\n", err)
		}
	}

	// --- Steps 1-3: Wikipedia real photos for specific subjects ---
	// Uses Ken Burns (slow pan) when animate=true, static otherwise.
	type wikiSubject struct {
		name    string
		imgSuffix string
		chyron  string // non-empty = show name label; empty = no overlay
	}
	subjects := []wikiSubject{}
	if cue.Person != "" {
		subjects = append(subjects, wikiSubject{cue.Person, "_person.jpg", cue.Person})
	}
	if cue.Product != "" {
		subjects = append(subjects, wikiSubject{cue.Product, "_product.jpg", ""})
	}
	if cue.Company != "" {
		subjects = append(subjects, wikiSubject{cue.Company, "_company.jpg", ""})
	}

	for _, subj := range subjects {
		imgPath := outputPath + subj.imgSuffix
		wikiURL := "https://en.wikipedia.org/wiki/" + url.QueryEscape(strings.ReplaceAll(subj.name, " ", "_"))
		isPerson := subj.imgSuffix == "_person.jpg"

		// For companies and products: skip Wikipedia cache and use image_query via Commons
		// so each cue gets a DIFFERENT image even when the company name is the same.
		// For persons: always use Wikipedia (same face = correct, cache is fine).
		if !isPerson && cue.ImageQuery != "" {
			imgURL, pgURL, err := searchWikimediaCommons(cue.ImageQuery)
			if err == nil {
				if dlErr := downloadFileWithUA(imgURL, imgPath, wikiUA); dlErr == nil {
					duration := estimateClipDuration(outputPath)
					var encErr error
					if cue.Animate {
						encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
					} else {
						encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
					}
					os.Remove(imgPath)
					if encErr == nil {
						fmt.Printf("    [commons-direct] %s\n", cue.ImageQuery)
						return ClipSource{Source: "wikimedia-commons", URL: pgURL}, nil
					}
				}
			}
			fmt.Printf("    commons direct failed for '%s', falling through\n", cue.ImageQuery)
		}

		fmt.Printf("    trying wikipedia for '%s'...\n", subj.name)
		if err := fetchWikipediaPhoto(subj.name, imgPath); err != nil {
			fmt.Printf("    wikipedia FETCH failed for '%s': %v\n", subj.name, err)
			// for companies, also try Clearbit logo as fallback
			if subj.imgSuffix == "_company.jpg" {
				if err2 := fetchClearbitLogo(subj.name, imgPath); err2 != nil {
					fmt.Printf("    clearbit failed for '%s': %v\n", subj.name, err2)
					continue
				}
			} else {
				continue
			}
		} else {
			fmt.Printf("    wikipedia photo downloaded for '%s'\n", subj.name)
		}

		duration := estimateClipDuration(outputPath)
		var encErr error
		if cue.Animate {
			if subj.chyron != "" {
				encErr = imageToVideoKenBurnsWithChyron(imgPath, outputPath, duration, portrait, subj.chyron)
			} else {
				encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
			}
		} else {
			if subj.chyron != "" {
				encErr = imageToVideoClipWithChyron(imgPath, outputPath, duration, portrait, subj.chyron)
			} else {
				encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
			}
		}
		os.Remove(imgPath)
		if encErr == nil {
			animLabel := ""
			if cue.Animate {
				animLabel = " (ken-burns)"
			}
			fmt.Printf("    [wikipedia] %s%s\n", subj.name, animLabel)
			return ClipSource{Source: "wikipedia", URL: wikiURL}, nil
		}
		fmt.Printf("    encode failed for '%s': %v\n", subj.name, encErr)
	}

	// --- Step 3b: Wikimedia Commons search — real product/event/company photos ---
	// Uses cue.ImageQuery (specific) then cue.keyword (generic) as search terms.
	{
		queries := []string{}
		if cue.ImageQuery != "" {
			queries = append(queries, cue.ImageQuery)
		}
		if cue.Product != "" {
			queries = append(queries, cue.Product)
		}
		for _, q := range queries {
			imgPath := outputPath + "_commons.jpg"
			fmt.Printf("    searching Commons: %q\n", q)
			if imgURL, pgURL, err := searchWikimediaCommons(q); err == nil {
				if dlErr := downloadFileWithUA(imgURL, imgPath, wikiUA); dlErr == nil {
					duration := estimateClipDuration(outputPath)
					var encErr error
					if cue.Animate {
						encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
					} else {
						encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
					}
					os.Remove(imgPath)
					if encErr == nil {
						fmt.Printf("    [wikimedia-commons] %s\n", q)
						return ClipSource{Source: "wikimedia-commons", URL: pgURL}, nil
					}
				}
			}
		}
	}

	// --- Step 3c: DuckDuckGo image search — real photos from across the web ---
	if cue.ImageQuery != "" {
		imgPath := outputPath + "_ddg.jpg"
		fmt.Printf("    searching DDG: %q\n", cue.ImageQuery)
		if imgURL, err := searchDuckDuckGoImages(cue.ImageQuery); err == nil {
			if dlErr := downloadFile(imgURL, imgPath); dlErr == nil {
				if isValidImageFile(imgPath) {
					duration := estimateClipDuration(outputPath)
					var encErr error
					if cue.Animate {
						encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
					} else {
						encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
					}
					os.Remove(imgPath)
					if encErr == nil {
						fmt.Printf("    [duckduckgo]\n")
						return ClipSource{Source: "duckduckgo", URL: imgURL}, nil
					}
				}
				os.Remove(imgPath)
			}
		}
	}

	// --- Step 4: Stock video — Pexels + Pixabay (pick best score) ---
	// Animated scenes accept a lower match score (real footage >> painted image).
	{
		minScore := 2
		if cue.Animate {
			minScore = 1
		}

		type stockResult struct {
			url     string
			pageURL string
			score   int
			source  string
		}
		var best stockResult

		if cfg.PexelsAPIKey != "" {
			if u, pg, sc, err := searchBestClipScored(cfg.PexelsAPIKey, keyword, orientation); err == nil && sc > best.score {
				best = stockResult{u, pg, sc, "pexels"}
			}
		}
		if cfg.PixabayAPIKey != "" {
			if u, pg, sc, err := searchPixabayScored(cfg.PixabayAPIKey, keyword, orientation); err == nil && sc > best.score {
				best = stockResult{u, pg, sc, "pixabay"}
			}
		}

		if best.url != "" && best.score >= minScore {
			if dlErr := downloadFile(best.url, outputPath); dlErr == nil {
				fmt.Printf("    [%s] score=%d\n", best.source, best.score)
				return ClipSource{Source: best.source, URL: best.pageURL}, nil
			}
		}
	}

	// --- Step 5: fal.ai Kling — real AI video for animated scenes ---
	// Only used for animate=true since it costs ~$0.10/clip and takes ~30-60s.
	if cue.Animate && cfg.FalAPIKey != "" {
		fmt.Printf("    calling fal.ai kling...\n")
		if err := generateVideoFal(buildVideoPrompt(keyword), cfg.FalAPIKey, outputPath, portrait); err == nil {
			fmt.Printf("    [fal-kling]\n")
			return ClipSource{Source: "fal-kling", URL: "AI Generated (Kling v1.6)"}, nil
		} else {
			fmt.Printf("    fal.ai failed: %v\n", err)
		}
	}

	// --- Step 6: Pixabay photos — great for historical shots stock video won't have ---
	if cfg.PixabayAPIKey != "" {
		if imgURL, pgURL, score, err := searchPixabayPhoto(cfg.PixabayAPIKey, keyword); err == nil && score >= 2 {
			imgPath := outputPath + "_pixphoto.jpg"
			if dlErr := downloadFile(imgURL, imgPath); dlErr == nil {
				duration := estimateClipDuration(outputPath)
				var encErr error
				if cue.Animate {
					encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
				} else {
					encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
				}
				os.Remove(imgPath)
				if encErr == nil {
					fmt.Printf("    [pixabay-photo] score=%d\n", score)
					return ClipSource{Source: "pixabay-photo", URL: pgURL}, nil
				}
			}
		}
	}

	// --- Step 6b: Unsplash photos — high-quality editorial photos ---
	if cfg.UnsplashAccessKey != "" {
		if imgURL, pgURL, score, err := searchUnsplashPhoto(cfg.UnsplashAccessKey, keyword, !portrait); err == nil && score >= 0 {
			imgPath := outputPath + "_unsplash.jpg"
			if dlErr := downloadFile(imgURL, imgPath); dlErr == nil {
				duration := estimateClipDuration(outputPath)
				var encErr error
				if cue.Animate {
					encErr = imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
				} else {
					encErr = imageToVideoClip(imgPath, outputPath, duration, portrait)
				}
				os.Remove(imgPath)
				if encErr == nil {
					fmt.Printf("    [unsplash] score=%d\n", score)
					return ClipSource{Source: "unsplash", URL: pgURL}, nil
				}
			}
		}
	}

	// --- Steps 7a-d: AI image generation fallback chain ---
	// Try each generator in order; encode to video (Ken Burns if animated).
	type aiImageGen struct {
		name    string
		enabled bool
		fn      func() error
	}
	imgPath := outputPath + "_gen.png"
	encodeImage := func() error {
		duration := estimateClipDuration(outputPath)
		if cue.Animate {
			return imageToVideoKenBurns(imgPath, outputPath, duration, portrait)
		}
		return imageToVideoClip(imgPath, outputPath, duration, portrait)
	}

	aiGens := []aiImageGen{
		{
			// Flux Dev — best quality, ~$0.025/image, synchronous
			name:    "flux-dev",
			enabled: cfg.FalAPIKey != "",
			fn: func() error {
				return generateImageFal(buildImagePrompt(keyword), cfg.FalAPIKey, imgPath, portrait)
			},
		},
		{
			// Imagen 3 — free (15 req/min), Google AI Studio
			name:    "imagen3",
			enabled: cfg.ImagenAPIKey != "",
			fn: func() error {
				return generateImageImagen3(buildImagePrompt(keyword), cfg.ImagenAPIKey, imgPath, portrait)
			},
		},
		{
			// gpt-image-1 — ~$0.04/image, last resort
			name:    "gpt-image-1",
			enabled: cfg.DALLEAPIKey != "",
			fn: func() error {
				return generateImageGPTImage1(buildImagePrompt(keyword), cfg.DALLEAPIKey, imgPath, portrait)
			},
		},
	}

	for _, gen := range aiGens {
		if !gen.enabled {
			continue
		}
		fmt.Printf("    calling %s...\n", gen.name)
		if err := gen.fn(); err != nil {
			fmt.Printf("    %s failed: %v\n", gen.name, err)
			continue
		}
		if encErr := encodeImage(); encErr != nil {
			fmt.Printf("    %s encode failed: %v\n", gen.name, encErr)
			os.Remove(imgPath)
			continue
		}
		os.Remove(imgPath)
		fmt.Printf("    [%s]\n", gen.name)
		return ClipSource{Source: gen.name, URL: "AI Generated (" + gen.name + ")"}, nil
	}

	// --- Step 8: Scored forced fallback — only accept if score >= 2 to block irrelevant results ---
	// A score of 0-1 means Pexels/Pixabay found something but it barely matches (e.g. a butcher
	// shop returned for "Nokia factory workers"). Reject those and fall through to the topic card.
	simplified := simplifyKeyword(keyword)
	for _, q := range []string{keyword, simplified} {
		if q == "" {
			continue
		}
		if cfg.PexelsAPIKey != "" {
			if u, pg, score, err := searchBestClipScored(cfg.PexelsAPIKey, q, orientation); err == nil && score >= 2 {
				if dlErr := downloadFile(u, outputPath); dlErr == nil {
					fmt.Printf("    [pexels-forced score=%d]\n", score)
					return ClipSource{Source: "pexels", URL: pg}, nil
				}
			}
		}
		if cfg.PixabayAPIKey != "" {
			if u, pg, score, err := searchPixabayScored(cfg.PixabayAPIKey, q, orientation); err == nil && score >= 2 {
				if dlErr := downloadFile(u, outputPath); dlErr == nil {
					fmt.Printf("    [pixabay-forced score=%d]\n", score)
					return ClipSource{Source: "pixabay", URL: pg}, nil
				}
			}
		}
	}

	// --- Step 9: Topic card — branded text card, always contextually relevant ---
	// Used when every image/video source failed or returned irrelevant content.
	// Far better than a random butcher shop clip from Pexels.
	duration := estimateClipDuration(outputPath)
	subject := keyword
	if cue.Person != "" {
		subject = cue.Person
	} else if cue.Product != "" {
		subject = cue.Product
	} else if cue.Company != "" {
		subject = cue.Company
	}
	if err := createTopicCard(subject, outputPath, duration, portrait); err == nil {
		fmt.Printf("    [topic-card] %s\n", subject)
		return ClipSource{Source: "topic-card", URL: ""}, nil
	}

	return ClipSource{Source: "placeholder"}, fmt.Errorf("all sources failed for: %s", keyword)
}

// fetchClearbitLogo downloads a company logo from the Clearbit Logo API (free, no auth).
// It first looks up the company's official domain via Wikipedia's extract, then fetches
// the logo from logo.clearbit.com/{domain}.
func fetchClearbitLogo(companyName, outputPath string) error {
	// Step 1: get the company's official website from Wikipedia
	searchURL := fmt.Sprintf(
		"https://en.wikipedia.org/w/api.php?action=query&titles=%s&prop=extlinks&ellimit=10&format=json&redirects=1",
		url.QueryEscape(companyName),
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "OriginVault/1.0")
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("wikipedia extlinks request failed: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse extlinks: %w", err)
	}

	// extract a likely official domain from external links
	domain := extractOfficialDomain(result, companyName)
	if domain == "" {
		return fmt.Errorf("could not find official domain for %s", companyName)
	}

	// Step 2: fetch logo from Clearbit
	logoURL := fmt.Sprintf("https://logo.clearbit.com/%s?size=400", domain)
	logoReq, _ := http.NewRequest("GET", logoURL, nil)
	logoResp, err := apiClient.Do(logoReq)
	if err != nil {
		return fmt.Errorf("clearbit request failed: %w", err)
	}
	defer logoResp.Body.Close()
	if logoResp.StatusCode != http.StatusOK {
		return fmt.Errorf("clearbit returned %d for %s", logoResp.StatusCode, domain)
	}
	imgData, err := io.ReadAll(logoResp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, imgData, 0644)
}

// extractOfficialDomain scans Wikipedia extlinks for a likely official company website.
func extractOfficialDomain(result map[string]interface{}, companyName string) string {
	query, ok := result["query"].(map[string]interface{})
	if !ok {
		return ""
	}
	pages, ok := query["pages"].(map[string]interface{})
	if !ok {
		return ""
	}
	// social/reference domains to skip
	skip := map[string]bool{
		"twitter.com": true, "x.com": true, "facebook.com": true,
		"instagram.com": true, "youtube.com": true, "linkedin.com": true,
		"en.wikipedia.org": true, "wikidata.org": true,
	}
	slug := strings.ToLower(strings.ReplaceAll(companyName, " ", ""))
	for _, page := range pages {
		p, ok := page.(map[string]interface{})
		if !ok {
			continue
		}
		links, ok := p["extlinks"].([]interface{})
		if !ok {
			continue
		}
		for _, l := range links {
			lm, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			href, _ := lm["*"].(string)
			if href == "" {
				href, _ = lm["url"].(string)
			}
			if href == "" {
				continue
			}
			// extract host
			u, err := url.Parse(href)
			if err != nil || u.Host == "" {
				continue
			}
			host := strings.TrimPrefix(u.Host, "www.")
			if skip[host] {
				continue
			}
			// prefer domains that contain part of the company name
			hostClean := strings.ReplaceAll(host, ".", "")
			if strings.Contains(hostClean, slug[:min(len(slug), 5)]) {
				return host
			}
		}
		// second pass: return first non-skipped domain
		for _, l := range links {
			lm, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			href, _ := lm["*"].(string)
			if href == "" {
				href, _ = lm["url"].(string)
			}
			u, err := url.Parse(href)
			if err != nil || u.Host == "" {
				continue
			}
			host := strings.TrimPrefix(u.Host, "www.")
			if !skip[host] {
				return host
			}
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fetchArticleOGImage fetches the og:image meta tag from a news article URL.
// Only reads the first 8KB of HTML — og:image is always in <head>.
func fetchArticleOGImage(articleURL, outputPath string) error {
	// follow redirects (Google News links redirect to real article URLs)
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", articleURL, nil)
	if err != nil {
		return err
	}
	// news sites require a browser-like UA or they return 403/paywall
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("article fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("article returned HTTP %d: %s", resp.StatusCode, articleURL)
	}

	// read first 16KB — og:image is always in <head>
	buf := make([]byte, 16384)
	n, _ := io.ReadAtLeast(resp.Body, buf, 1)
	html := string(buf[:n])

	imgURL := extractMetaImage(html)
	if imgURL == "" {
		return fmt.Errorf("no og:image found at %s", articleURL)
	}
	if strings.HasPrefix(imgURL, "//") {
		imgURL = "https:" + imgURL
	}

	return downloadFile(imgURL, outputPath)
}

// extractMetaImage tries multiple meta tag patterns to find an article's main image.
// Checks og:image, twitter:image, and other common patterns.
func extractMetaImage(html string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image["'][^>]+content=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image["']`),
		regexp.MustCompile(`(?i)<meta[^>]+name=["']twitter:image["'][^>]+content=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+name=["']twitter:image["']`),
		regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image:url["'][^>]+content=["']([^"']+)["']`),
	}
	for _, re := range patterns {
		if m := re.FindStringSubmatch(html); len(m) >= 2 && m[1] != "" {
			return m[1]
		}
	}
	return ""
}

// imageToVideoClipWithNewsChyron creates a video clip from a news article image
// with a dark headline bar at the bottom — mimicking the Company Man news-citation style.
func imageToVideoClipWithNewsChyron(imagePath, outputPath string, duration float64, portrait bool, headline, domain string) error {
	if !isValidImageFile(imagePath) {
		return fmt.Errorf("imageToVideoClipWithNewsChyron: invalid or corrupt image file: %s", imagePath)
	}
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}

	// truncate headline so it fits on one line
	if len(headline) > 70 {
		headline = headline[:67] + "..."
	}

	baseFilter := staticImageFilter(outW, outH)

	// dark bar + headline text + domain badge
	chyron := fmt.Sprintf(
		"drawbox=x=0:y=h-110:w=iw:h=110:color=black@0.80:t=fill,"+
			"drawtext=text='%s':fontcolor=white:fontsize=30:x=24:y=h-88:font=Liberation Sans Bold:line_spacing=4,"+
			"drawtext=text='%s':fontcolor=#AAAAAA:fontsize=22:x=24:y=h-44:font=Liberation Sans",
		escaped(headline),
		escaped(domain),
	)
	fullFilter := fmt.Sprintf("%s,%s", baseFilter, chyron)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
		"-vf", fullFilter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "1",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// extractDomain pulls the hostname from a URL for display in the chyron.
func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// fetchWikipediaPhoto downloads the main Wikipedia article image for a real person.
const wikiUA = "OriginVault/1.0 (youtube automation pipeline; mailto:contact@originvault.com)"

// fetchWikipediaPhoto downloads the main image from a Wikipedia article.
// Uses an in-memory cache to avoid re-downloading the same image across multiple cues.
// Adds a polite delay between requests to avoid Wikimedia 429 rate limits.
func fetchWikipediaPhoto(name, outputPath string) error {
	// Cache hit: write cached bytes to the requested output path
	if cached, ok := wikiPhotoCache[name]; ok {
		return os.WriteFile(outputPath, cached, 0644)
	}

	// Polite delay: Wikimedia rate-limits aggressive clients
	time.Sleep(700 * time.Millisecond)

	imgURL, err := wikiPageImage(name)
	if err != nil {
		// Fallback: search Wikipedia to find the canonical article title
		searchURL := fmt.Sprintf(
			"https://en.wikipedia.org/w/api.php?action=opensearch&search=%s&limit=1&format=json",
			url.QueryEscape(name),
		)
		req, _ := http.NewRequest("GET", searchURL, nil)
		req.Header.Set("User-Agent", wikiUA)
		resp, err2 := apiClient.Do(req)
		if err2 != nil {
			return fmt.Errorf("wikipedia search failed: %w", err2)
		}
		defer resp.Body.Close()
		var osResult []json.RawMessage
		if err2 = json.NewDecoder(resp.Body).Decode(&osResult); err2 != nil || len(osResult) < 2 {
			return fmt.Errorf("no wikipedia article found for: %s", name)
		}
		var titles []string
		if err2 = json.Unmarshal(osResult[1], &titles); err2 != nil || len(titles) == 0 {
			return fmt.Errorf("no wikipedia article found for: %s", name)
		}
		time.Sleep(700 * time.Millisecond)
		imgURL, err = wikiPageImage(titles[0])
		if err != nil {
			return fmt.Errorf("no image on wikipedia article '%s': %w", titles[0], err)
		}
	}

	if err := downloadFileWithUA(imgURL, outputPath, wikiUA); err != nil {
		return err
	}
	// Cache raw bytes for reuse — outputPath gets deleted after encoding
	if data, err := os.ReadFile(outputPath); err == nil {
		wikiPhotoCache[name] = data
	}
	return nil
}

// wikiPageImage fetches the main image URL for a Wikipedia article by exact title.
// Requests a 1200px thumbnail — smaller than original, different CDN, avoids 429 rate limits.
// Also skips SVG files (logos, diagrams) which FFmpeg cannot decode.
func wikiPageImage(title string) (string, error) {
	// Request both original and thumbnail; prefer thumbnail to avoid Wikimedia 429 rate limits
	apiURL := fmt.Sprintf(
		"https://en.wikipedia.org/w/api.php?action=query&titles=%s&prop=pageimages&piprop=thumbnail&pithumbsize=1200&format=json&redirects=1",
		url.QueryEscape(title),
	)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "OriginVault/1.0 (youtube automation pipeline)")
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	query, ok := result["query"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no query in response")
	}
	pages, ok := query["pages"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no pages in response")
	}
	for _, page := range pages {
		p, ok := page.(map[string]interface{})
		if !ok {
			continue
		}
		thumb, ok := p["thumbnail"].(map[string]interface{})
		if !ok {
			continue
		}
		imgURL, ok := thumb["source"].(string)
		if !ok || imgURL == "" {
			continue
		}
		// skip SVGs — FFmpeg cannot decode them
		lower := strings.ToLower(imgURL)
		if strings.Contains(lower, ".svg") {
			return "", fmt.Errorf("wikipedia image is SVG (logo/diagram), skipping: %s", title)
		}
		return imgURL, nil
	}
	return "", fmt.Errorf("no image found for: %s", title)
}

// kenBurnsFilter builds an FFmpeg filter that slowly pans across an upscaled image.
// It avoids the zoompan filter entirely — scale+crop is single-pass and fast.
// The image is scaled to 1.3x then the crop window moves left→right over `duration` seconds.
func kenBurnsFilter(outW, outH int, duration float64) string {
	scaledW := int(float64(outW) * 1.30)
	scaledH := int(float64(outH) * 1.30)
	// pan horizontally: start at left edge, end at right edge
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d:x='(%d-%d)*t/%.2f':y='(%d-%d)/2',format=yuv420p",
		scaledW, scaledH,
		outW, outH,
		scaledW, outW, duration,
		scaledH, outH,
	)
}

// imageToVideoKenBurns converts a still image to video with a smooth Ken Burns pan.
// Falls back to static (no pan) for images smaller than the output canvas — upscaling
// a small photo produces visible pixelation, so we show it at native resolution instead.
func imageToVideoKenBurns(imagePath, outputPath string, duration float64, portrait bool) error {
	if !isValidImageFile(imagePath) {
		return fmt.Errorf("imageToVideoKenBurns: invalid or corrupt image file: %s", imagePath)
	}
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}
	// Ken Burns requires scaling to 1.3× output — only viable if image is already
	// at least as large as the output canvas. Otherwise fall back to static.
	if w, h, err := getImageDimensions(imagePath); err != nil || w < outW || h < outH {
		return imageToVideoClip(imagePath, outputPath, duration, portrait)
	}
	filter := kenBurnsFilter(outW, outH, duration)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
		"-vf", filter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "24",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// imageToVideoKenBurnsWithChyron adds a Ken Burns pan AND a name chyron (for persons).
// Falls back to static+chyron when the image is smaller than the output canvas.
func imageToVideoKenBurnsWithChyron(imagePath, outputPath string, duration float64, portrait bool, personName string) error {
	if !isValidImageFile(imagePath) {
		return fmt.Errorf("imageToVideoKenBurnsWithChyron: invalid or corrupt image file: %s", imagePath)
	}
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}
	// Fall back to static chyron if image is too small for Ken Burns without upscaling.
	if w, h, err := getImageDimensions(imagePath); err != nil || w < outW || h < outH {
		return imageToVideoClipWithChyron(imagePath, outputPath, duration, portrait, personName)
	}
	kb := kenBurnsFilter(outW, outH, duration)
	chyron := fmt.Sprintf(
		"drawtext=text='%s':fontcolor=white:fontsize=32:x=50:y=h-80:font=Liberation Sans Bold:box=1:boxcolor=black@0.65:boxborderw=12",
		escaped(personName),
	)
	filter := fmt.Sprintf("%s,%s", kb, chyron)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
		"-vf", filter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "24",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// imageToVideoClipWithChyron creates a pan video from a person photo
// with their name displayed as a news-style chyron at the bottom.
func imageToVideoClipWithChyron(imagePath, outputPath string, duration float64, portrait bool, personName string) error {
	if !isValidImageFile(imagePath) {
		return fmt.Errorf("imageToVideoClipWithChyron: invalid or corrupt image file: %s", imagePath)
	}
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}

	panFilter := staticImageFilter(outW, outH)
	chyron := fmt.Sprintf(
		"drawtext=text='%s':fontcolor=white:fontsize=32:x=50:y=h-80:font=Liberation Sans Bold:box=1:boxcolor=black@0.65:boxborderw=12",
		escaped(personName),
	)
	fullFilter := fmt.Sprintf("%s,%s", panFilter, chyron)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
		"-vf", fullFilter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "1",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}


// generateStatCard creates a clean infographic clip showing a big number/stat — the same
// style used by Company Man when mentioning dollar figures, store counts, or percentages.
// Dark navy background, huge white number, gray label below, optional context line.
func generateStatCard(stat *StatCard, outputPath string, duration float64, portrait bool) error {
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}

	// Escape for FFmpeg drawtext (commas and colons need backslash)
	bigNum := escaped(stat.BigNumber)
	label := escaped(stat.Label)
	ctx2 := escaped(stat.Context)

	// Build filter: big number centered, label below, optional context below that
	labelY := "(h/2)+80"
	contextFilter := ""
	if ctx2 != "" {
		labelY = "(h/2)+60"
		contextFilter = fmt.Sprintf(
			",drawtext=text='%s':fontcolor=#64748b:fontsize=28:font=Liberation Sans:x=(w-text_w)/2:y=(h/2)+110",
			ctx2,
		)
	}

	filter := fmt.Sprintf(
		"drawtext=text='%s':fontcolor=white:fontsize=150:font=Liberation Sans Bold:x=(w-text_w)/2:y=(h/2)-120"+
			",drawtext=text='%s':fontcolor=#94a3b8:fontsize=40:font=Liberation Sans:x=(w-text_w)/2:y=%s%s",
		bigNum, label, labelY, contextFilter,
	)

	sCtx, sCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer sCancel()
	cmd := exec.CommandContext(sCtx, "ffmpeg", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=0x0f172a:size=%dx%d:rate=1:duration=%.2f", outW, outH, duration),
		"-vf", filter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "1",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// createTopicCard generates a branded text card clip — dark navy background with the subject
// name centered in large white text. Used as last resort when all image sources fail so the
// video always shows something contextually relevant instead of a random stock clip.
func createTopicCard(subject, outputPath string, duration float64, portrait bool) error {
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}

	// Trim to 5 words max so text fits cleanly on one line
	words := strings.Fields(subject)
	if len(words) > 5 {
		words = words[:5]
	}
	displayText := escaped(strings.Join(words, " "))

	filter := fmt.Sprintf(
		"drawtext=text='%s':fontcolor=white:fontsize=64:font=Liberation Sans Bold:x=(w-text_w)/2:y=(h-text_h)/2",
		displayText,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=0x111827:size=%dx%d:rate=1:duration=%.2f", outW, outH, duration),
		"-vf", filter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "1",
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// isHistoricalCue returns true if the keyword describes a historical/period scene
// that stock footage is unlikely to have — triggers AI image generation directly.
func isHistoricalCue(keyword string) bool {
	lower := strings.ToLower(keyword)

	// year patterns: 1850, 1920s, 19th century, etc.
	yearRe := regexp.MustCompile(`\b1[0-9]{3}s?\b`)
	if yearRe.MatchString(lower) {
		return true
	}
	centuryRe := regexp.MustCompile(`\b(19|18|17|16)th century\b`)
	if centuryRe.MatchString(lower) {
		return true
	}

	// historical descriptors
	for _, term := range []string{
		"vintage", "antique", "historical", "ancient", "old painting",
		"portrait of", "illustration of", "drawing of", "engraving",
		"founding", "original factory", "wartime", "world war",
		"great depression", "industrial revolution",
	} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// buildImagePrompt converts a clip keyword into a rich image generation prompt.
// Uses photorealistic documentary style so images look like real footage stills.
func buildImagePrompt(keyword string) string {
	style := "Cinematic photorealistic still frame, documentary photography style, shot on 35mm film, natural dramatic lighting, high detail, wide establishing shot"
	safety := "no text overlays, no watermarks, no logos, no CGI look, no cartoon style"
	return fmt.Sprintf("%s, %s, %s", style, keyword, safety)
}

// estimateClipDuration reads a target duration from a sibling .dur file if present,
// otherwise returns a default. Used when image gen happens before timing is known.
func estimateClipDuration(_ string) float64 {
	return 30.0 // generous duration — actual trimming happens in AssembleVideo
}

// generateImageImagen3 generates an image via Google's Imagen 3 model.
// Free tier: 15 req/min. Get API key at aistudio.google.com.
func generateImageImagen3(prompt, apiKey, outputPath string, portrait bool) error {
	aspectRatio := "16:9"
	if portrait {
		aspectRatio = "9:16"
	}

	type instance struct {
		Prompt string `json:"prompt"`
	}
	type parameters struct {
		SampleCount      int    `json:"sampleCount"`
		AspectRatio      string `json:"aspectRatio"`
		SafetyFilterLevel string `json:"safetyFilterLevel"`
	}
	body, _ := json.Marshal(map[string]interface{}{
		"instances":  []instance{{Prompt: prompt}},
		"parameters": parameters{SampleCount: 1, AspectRatio: aspectRatio, SafetyFilterLevel: "block_some"},
	})

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1/models/imagen-3.0-generate-001:predict?key=%s",
		apiKey,
	)
	req, _ := http.NewRequest("POST", endpoint, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("Imagen 3 request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Imagen 3 error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Predictions []struct {
			BytesBase64Encoded string `json:"bytesBase64Encoded"`
			MimeType           string `json:"mimeType"`
		} `json:"predictions"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return fmt.Errorf("failed to parse Imagen 3 response: %w", err)
	}
	if len(result.Predictions) == 0 || result.Predictions[0].BytesBase64Encoded == "" {
		return fmt.Errorf("no image returned from Imagen 3")
	}

	imgData, err := base64.StdEncoding.DecodeString(result.Predictions[0].BytesBase64Encoded)
	if err != nil {
		return fmt.Errorf("failed to decode base64 image: %w", err)
	}
	return os.WriteFile(outputPath, imgData, 0644)
}

// generateImageGPTImage1 generates an image via OpenAI gpt-image-1.
// Uses the same API key as TTS. Cost: ~$0.04/image (medium quality).
// gpt-image-1 returns base64-encoded PNG, not a URL.
func generateImageGPTImage1(prompt, apiKey, outputPath string, portrait bool) error {
	size := "1536x1024"
	if portrait {
		size = "1024x1536"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":   "gpt-image-1",
		"prompt":  prompt,
		"n":       1,
		"size":    size,
		"quality": "medium",
	})

	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/images/generations", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("gpt-image-1 request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gpt-image-1 error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return fmt.Errorf("failed to parse gpt-image-1 response: %w", err)
	}
	if len(result.Data) == 0 || result.Data[0].B64JSON == "" {
		return fmt.Errorf("no image returned from gpt-image-1")
	}

	imgBytes, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		return fmt.Errorf("failed to decode gpt-image-1 base64: %w", err)
	}
	return os.WriteFile(outputPath, imgBytes, 0644)
}

// falQueueSubmit posts a job to any fal.ai queue-based model and returns the request ID.
func falQueueSubmit(modelID, apiKey string, payload map[string]interface{}) (string, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST",
		"https://queue.fal.run/"+modelID,
		bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fal.ai submit failed: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != 201 {
		return "", fmt.Errorf("fal.ai submit error %d: %s", resp.StatusCode, string(respBytes))
	}
	var r struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(respBytes, &r); err != nil || r.RequestID == "" {
		return "", fmt.Errorf("fal.ai: no request_id: %s", string(respBytes))
	}
	return r.RequestID, nil
}

// falQueueWait polls a fal.ai queue request until COMPLETED or timeout.
// Returns the raw result JSON bytes.
func falQueueWait(modelID, apiKey, requestID string, timeout time.Duration) ([]byte, error) {
	baseURL := "https://queue.fal.run/" + modelID + "/requests/" + requestID
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)

		statusReq, _ := http.NewRequest("GET", baseURL+"/status", nil)
		statusReq.Header.Set("Authorization", "Key "+apiKey)
		statusResp, err := apiClient.Do(statusReq)
		if err != nil {
			continue
		}
		statusBytes, _ := io.ReadAll(statusResp.Body)
		statusResp.Body.Close()

		var status struct {
			Status string `json:"status"`
		}
		json.Unmarshal(statusBytes, &status)

		switch status.Status {
		case "COMPLETED":
			// fetch result
			resultReq, _ := http.NewRequest("GET", baseURL, nil)
			resultReq.Header.Set("Authorization", "Key "+apiKey)
			resultResp, err := apiClient.Do(resultReq)
			if err != nil {
				return nil, fmt.Errorf("fal.ai result fetch failed: %w", err)
			}
			defer resultResp.Body.Close()
			return io.ReadAll(resultResp.Body)
		case "FAILED":
			return nil, fmt.Errorf("fal.ai job failed: %s", string(statusBytes))
		}
	}
	return nil, fmt.Errorf("fal.ai job timed out after %s", timeout)
}

// generateVideoFal generates a cinematic AI video using Kling 2.5 Turbo via fal.ai queue.
// Kling 2.5 Turbo produces high-quality 5s clips. Model: fal-ai/kling-video/v2.5/turbo/text-to-video
func generateVideoFal(prompt, apiKey, outputPath string, portrait bool) error {
	aspectRatio := "16:9"
	if portrait {
		aspectRatio = "9:16"
	}

	const model = "fal-ai/kling-video/v2.5/turbo/text-to-video"

	requestID, err := falQueueSubmit(model, apiKey, map[string]interface{}{
		"prompt":       prompt,
		"duration":     "5",
		"aspect_ratio": aspectRatio,
	})
	if err != nil {
		return err
	}

	fmt.Printf("    fal.ai job %s...\n", requestID[:8])
	resultBytes, err := falQueueWait(model, apiKey, requestID, 4*time.Minute)
	if err != nil {
		return err
	}

	var result struct {
		Video struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil || result.Video.URL == "" {
		return fmt.Errorf("fal.ai: no video URL: %s", string(resultBytes))
	}
	return downloadFile(result.Video.URL, outputPath)
}

// generateImageFal generates a high-quality image using Flux Dev via fal.ai.
// Flux Dev is synchronous (no queue), ~$0.025/image — cheaper and better than DALL-E 3.
func generateImageFal(prompt, apiKey, outputPath string, portrait bool) error {
	imageSize := "landscape_16_9"
	if portrait {
		imageSize = "portrait_9_16"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"prompt":           prompt,
		"image_size":       imageSize,
		"num_images":       1,
		"enable_safety_checker": false,
	})

	req, _ := http.NewRequest("POST", "https://fal.run/fal-ai/flux/dev", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Flux Dev is synchronous — use longer timeout
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Flux Dev request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Flux Dev error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil || len(result.Images) == 0 {
		return fmt.Errorf("Flux Dev: no image in response: %s", string(respBytes))
	}
	return downloadFile(result.Images[0].URL, outputPath)
}

// buildVideoPrompt converts a clip keyword into a cinematic video generation prompt.
func buildVideoPrompt(keyword string) string {
	return fmt.Sprintf("Cinematic documentary footage, %s, realistic, high quality, smooth camera movement, professional lighting, no text overlays", keyword)
}

// staticImageFilter returns an FFmpeg filter that scales an image to fill the output
// resolution (maintaining aspect ratio, black-padding any leftover space).
// Uses flags=lanczos — the highest-quality resampling algorithm for both upscaling
// and downscaling — which significantly reduces blocky artefacts on small images.
func staticImageFilter(outW, outH int) string {
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease:flags=lanczos,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,format=yuv420p",
		outW, outH, outW, outH,
	)
}

// getImageDimensions returns the pixel width and height of an image via ffprobe.
func getImageDimensions(imagePath string) (int, int, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		imagePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected ffprobe output: %s", out)
	}
	w, e1 := strconv.Atoi(parts[0])
	h, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return 0, 0, fmt.Errorf("non-integer dimensions in: %s", out)
	}
	return w, h, nil
}

// imageToVideoClip converts a still image into a short static video clip.
// No animation — scale+pad only. Encodes in under a second on any hardware.
func imageToVideoClip(imagePath, outputPath string, duration float64, portrait bool) error {
	if !isValidImageFile(imagePath) {
		return fmt.Errorf("imageToVideoClip: invalid or corrupt image file: %s", imagePath)
	}
	outW, outH := 1920, 1080
	if portrait {
		outW, outH = 1080, 1920
	}

	filter := staticImageFilter(outW, outH)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
		"-vf", filter,
		"-t", fmt.Sprintf("%.2f", duration),
		"-r", "1",       // 1fps — trimClip re-encodes to 30fps anyway; keeps encode blazing fast
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// Exported wrappers for the dashboard server to call AI generation directly.

func BuildImagePromptPublic(keyword string) string  { return buildImagePrompt(keyword) }
func GenerateImageFalPublic(prompt, apiKey, outputPath string, portrait bool) error {
	return generateImageFal(prompt, apiKey, outputPath, portrait)
}
func GenerateImageImagen3Public(prompt, apiKey, outputPath string, portrait bool) error {
	return generateImageImagen3(prompt, apiKey, outputPath, portrait)
}
func GenerateImageGPT1Public(prompt, apiKey, outputPath string, portrait bool) error {
	return generateImageGPTImage1(prompt, apiKey, outputPath, portrait)
}
