package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type pexelsVideo struct {
	URL      string `json:"url"`
	Duration int    `json:"duration"`
	Files    []struct {
		Link    string `json:"link"`
		Quality string `json:"quality"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
	} `json:"video_files"`
}

type pexelsVideoResponse struct {
	Videos []pexelsVideo `json:"videos"`
}

// DownloadFootageForCues fetches the best clip for each cue using the multi-source system:
// News screenshot → Person photo → Company image → Animation → Pexels → DALL-E 3 → Pexels forced.
func DownloadFootageForCues(cfg FootageConfig, outputDir string, cues []ClipCue, research []ResearchResult) ([]string, []ClipSource, error) {
	var clipPaths []string
	var sources []ClipSource

	// Print the planned shot list so the sequence can be reviewed before download.
	fmt.Println("  Shot list planned by editor:")
	for i, cue := range cues {
		vt := cue.VisualType
		if vt == "" {
			vt = "unset"
		}
		subj := cue.ImageQuery
		if cue.Person != "" {
			subj = "PERSON: " + cue.Person
		} else if cue.Product != "" {
			subj = "PRODUCT: " + cue.Product
		} else if cue.Company != "" {
			subj = "COMPANY: " + cue.Company
		}
		fmt.Printf("    %2d. [%-12s] %s\n", i+1, vt, subj)
	}
	fmt.Println()

	// usedQueries tracks how many times each image_query has been used.
	// On repeat use, a variation suffix is appended to force a different result.
	usedQueries := map[string]int{}
	// variationSuffixes cycles through angle/view descriptors to get different photos
	// of the same subject when the primary query has already been used.
	variationSuffixes := []string{
		" different angle", " exterior view", " close up detail",
		" historic photo", " alternate view", " interior", " wide shot",
	}

	// usedSourceURLs tracks the source page URL of every clip that was successfully
	// downloaded. If getBestClip returns the same URL as a previous clip (i.e. Pexels
	// returned the same top result for two different queries), we delete the duplicate
	// and retry once with a varied query + keyword so the video has visual variety.
	usedSourceURLs := map[string]bool{}

	prevVisualType := ""
	for i, cue := range cues {
		clipPath := filepath.Join(outputDir, fmt.Sprintf("clip_%02d.mp4", i+1))

		if isValidClip(clipPath) {
			clipPaths = append(clipPaths, clipPath)
			sources = append(sources, ClipSource{Source: "cached"})
			prevVisualType = cue.VisualType
			continue
		}
		os.Remove(clipPath)

		if cue.VisualType != "" && cue.VisualType == prevVisualType {
			fmt.Printf("  WARNING: cue %d has same visual_type as previous (%s) — may look repetitive\n", i+1, cue.VisualType)
		}

		// Deduplicate image_query: if already used, append a variation suffix so a
		// different image of the same subject is retrieved.
		// Skip deduplication for stat cues — they have no image_query and generate
		// their own graphic, so appending variation suffixes is meaningless noise.
		originalQuery := cue.ImageQuery
		if cue.Stat == nil && originalQuery != "" {
			if count := usedQueries[originalQuery]; count > 0 {
				suffix := variationSuffixes[(count-1)%len(variationSuffixes)]
				cue.ImageQuery = originalQuery + suffix
				fmt.Printf("  [%d/%d] query reused — varying: %s\n", i+1, len(cues), cue.ImageQuery)
			}
			usedQueries[originalQuery]++
		}

		vt := cue.VisualType
		if vt == "" {
			vt = "?"
		}
		fmt.Printf("  [%d/%d] [%s] %s\n", i+1, len(cues), vt, cue.ImageQuery)
		src, err := getBestClip(cfg, cue, clipPath, false, research)

		// URL-level deduplication: two different queries can return the same Pexels/Pixabay
		// top result. If that happens, delete the file and retry once with a variation so
		// the same stock clip doesn't appear twice in the video.
		if err == nil && src.URL != "" && usedSourceURLs[src.URL] {
			fmt.Printf("  [%d/%d] duplicate clip detected (same URL as earlier clip) — retrying\n", i+1, len(cues))
			os.Remove(clipPath)
			variedCue := cue
			variedCue.ImageQuery = cue.ImageQuery + " alternate scene"
			variedCue.Keyword = cue.Keyword + " different"
			src, err = getBestClip(cfg, variedCue, clipPath, false, research)
		}
		if src.URL != "" {
			usedSourceURLs[src.URL] = true
		}

		if err != nil {
			fmt.Printf("  WARNING: all sources failed for clip %d, using black placeholder: %v\n", i+1, err)
			sources = append(sources, ClipSource{Source: "placeholder"})
			clipPaths = append(clipPaths, clipPath)
			prevVisualType = cue.VisualType
			continue
		}

		clipPaths = append(clipPaths, clipPath)
		sources = append(sources, src)
		prevVisualType = cue.VisualType

		// Fetch PiP image if requested. Stored as pip_NN.jpg alongside the main clips.
		if cue.PipQuery != "" {
			pipPath := filepath.Join(outputDir, fmt.Sprintf("pip_%02d.jpg", i+1))
			if !isValidClip(pipPath) {
				if imgURL, _, err2 := searchWikimediaCommons(cue.PipQuery); err2 == nil && imgURL != "" {
					if dlErr := downloadFile(imgURL, pipPath); dlErr != nil {
						fmt.Printf("  PiP [%d/%d]: download failed: %v\n", i+1, len(cues), dlErr)
					} else {
						fmt.Printf("  PiP [%d/%d]: %s\n", i+1, len(cues), cue.PipQuery)
					}
				} else {
					fmt.Printf("  PiP [%d/%d]: no image found for %q\n", i+1, len(cues), cue.PipQuery)
				}
			}
		}
	}

	return clipPaths, sources, nil
}

// DownloadFootagePortrait fetches portrait (9:16) clips for shorts using the same multi-source system.
func DownloadFootagePortrait(cfg FootageConfig, outputDir string, scenes []string) ([]string, []ClipSource, error) {
	var clipPaths []string
	var sources []ClipSource

	for i, scene := range scenes {
		clipPath := filepath.Join(outputDir, fmt.Sprintf("clip_%02d.mp4", i+1))
		if isValidClip(clipPath) {
			clipPaths = append(clipPaths, clipPath)
			sources = append(sources, ClipSource{Source: "cached"})
			continue
		}
		os.Remove(clipPath)

		fmt.Printf("    [%d/%d] %s\n", i+1, len(scenes), scene)
		src, err := getBestClip(cfg, ClipCue{Keyword: scene}, clipPath, true, nil)
		if err != nil {
			fmt.Printf("    WARNING: all sources failed for portrait clip %d: %v\n", i+1, err)
			sources = append(sources, ClipSource{Source: "placeholder"})
			clipPaths = append(clipPaths, clipPath)
			continue
		}

		clipPaths = append(clipPaths, clipPath)
		sources = append(sources, src)
	}

	return clipPaths, sources, nil
}

// searchBestClipScored fetches 15 Pexels results and returns the best match with its relevance score.
// Score ≥ 1 means at least one keyword matched the Pexels URL slug.
func searchBestClipScored(apiKey, query, orientation string) (downloadURL, pageURL string, score int, err error) {
	searchURL := fmt.Sprintf(
		"https://api.pexels.com/videos/search?query=%s&per_page=15&orientation=%s&size=medium&min_duration=10",
		url.QueryEscape(query), orientation,
	)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Authorization", apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("Pexels API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("Pexels API error %d: %s", resp.StatusCode, string(body))
	}

	var result pexelsVideoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, fmt.Errorf("failed to decode Pexels response: %w", err)
	}

	if len(result.Videos) == 0 {
		return "", "", 0, fmt.Errorf("no videos found for: %s", query)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	bestScore := -1
	bestLink := ""
	bestPageURL := ""

	for _, video := range result.Videos {
		slug := strings.ToLower(video.URL)
		slug = strings.ReplaceAll(slug, "-", " ")
		slug = strings.ReplaceAll(slug, "/", " ")

		s := 0
		for _, word := range queryWords {
			if len(word) > 3 && strings.Contains(slug, word) {
				s++
			}
		}
		if video.Duration >= 15 {
			s++
		}

		if s > bestScore {
			link := bestFileLink(video, orientation)
			if link != "" {
				bestScore = s
				bestLink = link
				bestPageURL = video.URL
			}
		}
	}

	if bestLink != "" {
		return bestLink, bestPageURL, bestScore, nil
	}

	// fallback: first available file, score 0
	for _, video := range result.Videos {
		if link := bestFileLink(video, orientation); link != "" {
			return link, video.URL, 0, nil
		}
	}

	return "", "", 0, fmt.Errorf("no downloadable file found for: %s", query)
}

// bestFileLink picks the best quality file from a Pexels video result.
func bestFileLink(video pexelsVideo, orientation string) string {
	for _, file := range video.Files {
		if orientation == "portrait" {
			if file.Quality == "hd" && file.Height >= 1080 && file.Width < file.Height {
				return file.Link
			}
		} else {
			if file.Quality == "hd" && file.Width >= 1920 {
				return file.Link
			}
		}
	}
	for _, file := range video.Files {
		if file.Quality == "hd" {
			return file.Link
		}
	}
	if len(video.Files) > 0 {
		return video.Files[0].Link
	}
	return ""
}

// searchPixabayScored queries Pixabay's free video API and returns the best match.
// Pixabay API key: free at pixabay.com/api/docs — 100 req/min, no credit card needed.
func searchPixabayScored(apiKey, query, orientation string) (downloadURL, pageURL string, score int, err error) {
	videoType := "film"
	if orientation == "portrait" {
		videoType = "film" // Pixabay doesn't filter by orientation in videos, we pick best file
	}
	searchURL := fmt.Sprintf(
		"https://pixabay.com/api/videos/?key=%s&q=%s&per_page=15&video_type=%s&min_duration=10",
		apiKey, url.QueryEscape(query), videoType,
	)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(searchURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("Pixabay request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("Pixabay error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Hits []struct {
			PageURL  string `json:"pageURL"`
			Duration int    `json:"duration"`
			Tags     string `json:"tags"`
			Videos   struct {
				Large  pixabayFile `json:"large"`
				Medium pixabayFile `json:"medium"`
				Small  pixabayFile `json:"small"`
			} `json:"videos"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, fmt.Errorf("failed to decode Pixabay response: %w", err)
	}
	if len(result.Hits) == 0 {
		return "", "", 0, fmt.Errorf("no Pixabay videos for: %s", query)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	bestScore := -1
	bestLink := ""
	bestPage := ""

	for _, hit := range result.Hits {
		tags := strings.ToLower(hit.Tags)
		s := 0
		for _, word := range queryWords {
			if len(word) > 3 && strings.Contains(tags, word) {
				s++
			}
		}
		if hit.Duration >= 15 {
			s++
		}

		link := bestPixabayFile(hit.Videos.Large, hit.Videos.Medium, hit.Videos.Small, orientation)
		if link != "" && s > bestScore {
			bestScore = s
			bestLink = link
			bestPage = hit.PageURL
		}
	}

	if bestLink != "" {
		return bestLink, bestPage, bestScore, nil
	}
	// fallback: first available
	for _, hit := range result.Hits {
		if link := bestPixabayFile(hit.Videos.Large, hit.Videos.Medium, hit.Videos.Small, orientation); link != "" {
			return link, hit.PageURL, 0, nil
		}
	}
	return "", "", 0, fmt.Errorf("no downloadable Pixabay file for: %s", query)
}

type pixabayFile struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func bestPixabayFile(large, medium, small pixabayFile, orientation string) string {
	files := []pixabayFile{large, medium, small}
	for _, f := range files {
		if f.URL == "" {
			continue
		}
		if orientation == "portrait" {
			if f.Height > f.Width {
				return f.URL
			}
		} else {
			if f.Width >= f.Height {
				return f.URL
			}
		}
	}
	// any file as last resort
	for _, f := range files {
		if f.URL != "" {
			return f.URL
		}
	}
	return ""
}

// searchPixabayPhoto queries Pixabay's image API (separate from videos).
// Great for historical photos, product shots, and scenes stock video won't have.
// Returns the best photo URL with a relevance score.
func searchPixabayPhoto(apiKey, query string) (imageURL, pageURL string, score int, err error) {
	searchURL := fmt.Sprintf(
		"https://pixabay.com/api/?key=%s&q=%s&image_type=photo&per_page=15&safesearch=true&min_width=1280",
		apiKey, url.QueryEscape(query),
	)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(searchURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("Pixabay photo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("Pixabay photo error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Hits []struct {
			LargeImageURL string `json:"largeImageURL"`
			PageURL       string `json:"pageURL"`
			Tags          string `json:"tags"`
			ImageWidth    int    `json:"imageWidth"`
			ImageHeight   int    `json:"imageHeight"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, fmt.Errorf("failed to decode Pixabay photo response: %w", err)
	}
	if len(result.Hits) == 0 {
		return "", "", 0, fmt.Errorf("no Pixabay photos for: %s", query)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	bestScore := -1
	bestURL := ""
	bestPage := ""

	for _, hit := range result.Hits {
		if hit.LargeImageURL == "" {
			continue
		}
		tags := strings.ToLower(hit.Tags)
		s := 0
		for _, word := range queryWords {
			if len(word) > 3 && strings.Contains(tags, word) {
				s++
			}
		}
		// prefer landscape photos for main video
		if hit.ImageWidth > hit.ImageHeight {
			s++
		}
		if s > bestScore {
			bestScore = s
			bestURL = hit.LargeImageURL
			bestPage = hit.PageURL
		}
	}

	if bestURL == "" {
		bestURL = result.Hits[0].LargeImageURL
		bestPage = result.Hits[0].PageURL
		bestScore = 0
	}
	return bestURL, bestPage, bestScore, nil
}

// searchUnsplashPhoto searches Unsplash for a high-quality photo matching the keyword.
// Unsplash free tier: 50 req/hour. Only the access key is needed for search.
// Returns imageURL, pageURL, score.
func searchUnsplashPhoto(accessKey, query string, landscape bool) (imageURL, pageURL string, score int, err error) {
	orientation := "landscape"
	if !landscape {
		orientation = "portrait"
	}
	searchURL := fmt.Sprintf(
		"https://api.unsplash.com/search/photos?query=%s&per_page=15&orientation=%s",
		url.QueryEscape(query), orientation,
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", "Client-ID "+accessKey)
	req.Header.Set("Accept-Version", "v1")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("Unsplash request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("Unsplash error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			Description string `json:"description"`
			AltDesc     string `json:"alt_description"`
			Links       struct {
				HTML string `json:"html"`
			} `json:"links"`
			Urls struct {
				Full    string `json:"full"`
				Regular string `json:"regular"` // ~1080px wide
			} `json:"urls"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Results) == 0 {
		return "", "", 0, fmt.Errorf("no Unsplash results for: %s", query)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	bestScore := -1
	bestURL := ""
	bestPage := ""

	for _, photo := range result.Results {
		text := strings.ToLower(photo.Description + " " + photo.AltDesc)
		s := 0
		for _, word := range queryWords {
			if len(word) > 3 && strings.Contains(text, word) {
				s++
			}
		}
		if s > bestScore {
			bestScore = s
			// prefer full-size; fall back to regular (~1080px)
			if photo.Urls.Full != "" {
				bestURL = photo.Urls.Full
			} else {
				bestURL = photo.Urls.Regular
			}
			bestPage = photo.Links.HTML
		}
	}

	if bestURL == "" {
		p := result.Results[0]
		if p.Urls.Full != "" {
			bestURL = p.Urls.Full
		} else {
			bestURL = p.Urls.Regular
		}
		bestPage = p.Links.HTML
		bestScore = 0
	}
	return bestURL, bestPage, bestScore, nil
}

// searchWikimediaCommons searches Wikimedia Commons for real photos matching the query.
// Free, no API key. 80M+ images including product photos, historical events, company buildings.
// Returns a 1200px thumbnail URL of the best non-SVG match.
func searchWikimediaCommons(query string) (imageURL, pageURL string, err error) {
	searchURL := fmt.Sprintf(
		"https://commons.wikimedia.org/w/api.php?action=query&list=search&srsearch=%s&srnamespace=6&srlimit=15&format=json",
		url.QueryEscape(query),
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "OriginVault/1.0 (youtube automation pipeline)")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Commons search failed: %w", err)
	}
	defer resp.Body.Close()

	var searchResult struct {
		Query struct {
			Search []struct {
				Title string `json:"title"` // e.g. "File:Philips CD100.jpg"
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return "", "", fmt.Errorf("Commons search parse failed: %w", err)
	}
	if len(searchResult.Query.Search) == 0 {
		return "", "", fmt.Errorf("no Commons images for: %s", query)
	}

	// Try each result in order; return the first usable photo (non-SVG, non-OGG)
	for _, result := range searchResult.Query.Search {
		title := result.Title
		lower := strings.ToLower(title)
		// skip non-photo file types
		if strings.Contains(lower, ".svg") || strings.Contains(lower, ".ogg") ||
			strings.Contains(lower, ".ogv") || strings.Contains(lower, ".webm") ||
			strings.Contains(lower, ".mid") || strings.Contains(lower, ".wav") {
			continue
		}

		time.Sleep(300 * time.Millisecond) // polite pacing
		infoURL := fmt.Sprintf(
			"https://commons.wikimedia.org/w/api.php?action=query&titles=%s&prop=imageinfo&iiprop=url|thumburl&iiurlwidth=1200&format=json",
			url.QueryEscape(title),
		)
		req2, _ := http.NewRequest("GET", infoURL, nil)
		req2.Header.Set("User-Agent", "OriginVault/1.0 (youtube automation pipeline)")
		resp2, err := (&http.Client{Timeout: 20 * time.Second}).Do(req2)
		if err != nil {
			continue
		}

		var infoResult struct {
			Query struct {
				Pages map[string]struct {
					ImageInfo []struct {
						ThumbURL       string `json:"thumburl"`
						URL            string `json:"url"`
						DescriptionURL string `json:"descriptionurl"`
					} `json:"imageinfo"`
				} `json:"pages"`
			} `json:"query"`
		}
		json.NewDecoder(resp2.Body).Decode(&infoResult)
		resp2.Body.Close()

		for _, page := range infoResult.Query.Pages {
			if len(page.ImageInfo) == 0 {
				continue
			}
			info := page.ImageInfo[0]
			imgURL := info.ThumbURL
			if imgURL == "" {
				imgURL = info.URL
			}
			if imgURL == "" {
				continue
			}
			// skip SVG even if it slipped through the title check
			if strings.Contains(strings.ToLower(imgURL), ".svg") {
				continue
			}
			pg := info.DescriptionURL
			if pg == "" {
				pg = "https://commons.wikimedia.org/wiki/" + url.QueryEscape(title)
			}
			return imgURL, pg, nil
		}
	}
	return "", "", fmt.Errorf("no usable Commons images for: %s", query)
}

// searchDuckDuckGoImages searches DuckDuckGo for real images matching the query.
// No API key required. Good fallback for product photos and news images not on Commons.
func searchDuckDuckGoImages(query string) (imageURL string, err error) {
	// DDG image search token endpoint
	tokenURL := fmt.Sprintf("https://duckduckgo.com/?q=%s&iax=images&ia=images", url.QueryEscape(query))
	req, _ := http.NewRequest("GET", tokenURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("DDG token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// extract vqd token from HTML
	vqd := ""
	bodyStr := string(body)
	if idx := strings.Index(bodyStr, "vqd="); idx != -1 {
		start := idx + 4
		end := strings.IndexAny(bodyStr[start:], "&\"'")
		if end != -1 {
			vqd = bodyStr[start : start+end]
		}
	}
	if vqd == "" {
		return "", fmt.Errorf("could not extract DDG vqd token")
	}

	// fetch image results
	imgURL := fmt.Sprintf(
		"https://duckduckgo.com/i.js?q=%s&o=json&p=1&s=0&u=bing&f=,,,,,&l=us-en&vqd=%s",
		url.QueryEscape(query), url.QueryEscape(vqd),
	)
	req2, _ := http.NewRequest("GET", imgURL, nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36")
	req2.Header.Set("Referer", "https://duckduckgo.com/")

	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("DDG image request failed: %w", err)
	}
	defer resp2.Body.Close()

	var ddgResult struct {
		Results []struct {
			Image  string `json:"image"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&ddgResult); err != nil || len(ddgResult.Results) == 0 {
		return "", fmt.Errorf("no DDG image results for: %s", query)
	}

	// prefer landscape images that are reasonably sized
	for _, r := range ddgResult.Results {
		if r.Image == "" {
			continue
		}
		lower := strings.ToLower(r.Image)
		if strings.Contains(lower, ".svg") || strings.Contains(lower, ".gif") {
			continue
		}
		if r.Width >= r.Height { // prefer landscape
			return r.Image, nil
		}
	}
	// fallback: first result
	return ddgResult.Results[0].Image, nil
}

// simplifyKeyword strips time periods and specifics for a broader fallback search.
func simplifyKeyword(keyword string) string {
	removals := []string{
		"1800s", "1900s", "1920s", "1930s", "1940s", "1950s", "1960s", "1970s",
		"vintage", "antique", "historical", "ancient", "old", "retro", "classic",
		"cinematic", "close-up", "closeup", "dramatic",
	}
	result := strings.ToLower(keyword)
	for _, r := range removals {
		result = strings.ReplaceAll(result, r, "")
	}
	return strings.Join(strings.Fields(result), " ")
}

func downloadFile(fileURL, destPath string) error {
	return downloadFileWithUA(fileURL, destPath, "")
}

// downloadFileWithUA downloads a file, optionally setting a custom User-Agent.
// Wikimedia Commons returns 403 for Go's default "Go-http-client" UA.
func downloadFileWithUA(fileURL, destPath, userAgent string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d for %s", resp.StatusCode, fileURL)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// isValidImageFile checks that a file has valid image magic bytes (JPEG or PNG).
// Prevents passing HTML error pages or corrupt downloads to FFmpeg.
func isValidImageFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 8)
	n, err := f.Read(header)
	if err != nil || n < 4 {
		return false
	}
	// JPEG: FF D8 FF
	if header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return true
	}
	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47 {
		return true
	}
	// WebP: RIFF....WEBP
	if n >= 8 && header[0] == 'R' && header[1] == 'I' && header[2] == 'F' && header[3] == 'F' {
		return true
	}
	return false
}

// isValidClip returns true only if the file exists and is large enough to be a real video.
// A partial/corrupt file left by a killed FFmpeg process is typically 0 bytes or very small.
func isValidClip(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 50*1024 // must be > 50 KB
}
