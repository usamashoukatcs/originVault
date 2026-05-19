package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// resolveDoH resolves a hostname using DNS-over-HTTPS (port 443).
// This works even when UDP/TCP port 53 is blocked, since it uses normal HTTPS.
func resolveDoH(host string) (string, error) {
	for _, endpoint := range []string{
		"https://1.1.1.1/dns-query?name=" + url.QueryEscape(host) + "&type=A",
		"https://8.8.8.8/dns-query?name=" + url.QueryEscape(host) + "&type=A",
	} {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/dns-json")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			continue
		}
		var result struct {
			Answer []struct {
				Type int    `json:"type"`
				Data string `json:"data"`
			} `json:"Answer"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		for _, a := range result.Answer {
			if a.Type == 1 { // A record
				return a.Data, nil
			}
		}
	}
	return "", fmt.Errorf("DoH lookup failed for %s", host)
}

// dohDialer dials using system DNS first, falls back to DNS-over-HTTPS.
// Shared by wikiClient and ddgClient so any blocked domain resolves correctly.
var dohDialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	if ips, err := net.DefaultResolver.LookupHost(ctx, host); err == nil && len(ips) > 0 {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0], port))
	}
	if ip, err := resolveDoH(host); err == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
	}
	return nil, fmt.Errorf("could not resolve %s (system DNS and DoH both failed)", host)
}

var ddgClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DialContext: dohDialer},
}

// wikiClient resolves commons.wikimedia.org via DNS-over-HTTPS so it works even
// when the system DNS or ISP blocks the domain over standard port 53.
var wikiClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DialContext: dohDialer},
}

// SearchResult is one candidate image or video returned by multi-result search.
// The frontend displays ThumbURL in a grid; DownloadURL is used when the user selects it.
type SearchResult struct {
	ThumbURL    string `json:"thumb_url"`    // thumbnail/preview URL safe to embed in <img> or <video>
	DownloadURL string `json:"download_url"` // direct download URL for the actual file
	PageURL     string `json:"page_url"`     // attribution/source page URL
	Source      string `json:"source"`       // "pexels", "pixabay-video", "pixabay-photo", "commons", "duckduckgo"
	Score       int    `json:"score"`        // relevance score (higher = better match)
	Duration    int    `json:"duration"`     // seconds (0 for still images)
	IsVideo     bool   `json:"is_video"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Title       string `json:"title"` // tags or alt description for display
}

// pexelsVideoFull extends the internal pexelsVideo struct to include the thumbnail image.
type pexelsVideoFull struct {
	URL      string `json:"url"`
	Duration int    `json:"duration"`
	Image    string `json:"image"` // Pexels-provided thumbnail
	Files    []struct {
		Link    string `json:"link"`
		Quality string `json:"quality"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
	} `json:"video_files"`
}

// SearchPexelsMulti returns up to 15 Pexels video candidates scored by keyword relevance.
func SearchPexelsMulti(apiKey, query, orientation string) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Pexels API key configured")
	}
	searchURL := fmt.Sprintf(
		"https://api.pexels.com/videos/search?query=%s&per_page=15&orientation=%s&size=medium",
		url.QueryEscape(query), orientation,
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Pexels request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Pexels error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Videos []pexelsVideoFull `json:"videos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Pexels decode failed: %w", err)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	var out []SearchResult
	for _, v := range result.Videos {
		slug := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(v.URL, "-", " "), "/", " "))
		score := 0
		for _, w := range queryWords {
			if len(w) > 3 && strings.Contains(slug, w) {
				score++
			}
		}
		if v.Duration >= 15 {
			score++
		}

		dlURL, w, h := bestPexelsFull(v, orientation)
		if dlURL == "" {
			continue
		}
		// Use a small video file as thumb so the browser can preview it;
		// fall back to the static JPEG if no small file is available.
		thumbURL := v.Image
		for _, f := range v.Files {
			if f.Quality == "sd" && f.Link != "" {
				thumbURL = f.Link
				break
			}
		}

		out = append(out, SearchResult{
			ThumbURL:    thumbURL,
			DownloadURL: dlURL,
			PageURL:     v.URL,
			Source:      "pexels",
			Score:       score,
			Duration:    v.Duration,
			IsVideo:     true,
			Width:       w,
			Height:      h,
		})
	}
	return out, nil
}

func bestPexelsFull(v pexelsVideoFull, orientation string) (link string, w, h int) {
	for _, f := range v.Files {
		if orientation == "portrait" {
			if f.Quality == "hd" && f.Height >= 1080 && f.Width < f.Height {
				return f.Link, f.Width, f.Height
			}
		} else {
			if f.Quality == "hd" && f.Width >= 1920 {
				return f.Link, f.Width, f.Height
			}
		}
	}
	for _, f := range v.Files {
		if f.Quality == "hd" {
			return f.Link, f.Width, f.Height
		}
	}
	if len(v.Files) > 0 {
		return v.Files[0].Link, v.Files[0].Width, v.Files[0].Height
	}
	return "", 0, 0
}

// SearchPexelsPhotosMulti searches Pexels photos (not videos) and returns up to 15 candidates.
func SearchPexelsPhotosMulti(apiKey, query string) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Pexels API key configured")
	}
	searchURL := fmt.Sprintf(
		"https://api.pexels.com/v1/search?query=%s&per_page=15",
		url.QueryEscape(query),
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Pexels photos request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Pexels photos error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Photos []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			URL    string `json:"url"`
			Src    struct {
				Original string `json:"original"`
				Large    string `json:"large"`
				Medium   string `json:"medium"`
			} `json:"src"`
			Alt string `json:"alt"`
		} `json:"photos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Pexels photos decode failed: %w", err)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	var out []SearchResult
	for _, p := range result.Photos {
		slug := strings.ToLower(p.Alt + " " + p.URL)
		score := 0
		for _, w := range queryWords {
			if len(w) > 3 && strings.Contains(slug, w) {
				score++
			}
		}
		dlURL := p.Src.Large
		if dlURL == "" {
			dlURL = p.Src.Original
		}
		thumb := p.Src.Medium
		if thumb == "" {
			thumb = p.Src.Large
		}
		if dlURL == "" {
			continue
		}
		out = append(out, SearchResult{
			ThumbURL:    thumb,
			DownloadURL: dlURL,
			PageURL:     p.URL,
			Source:      "pexels-photo",
			Score:       score,
			IsVideo:     false,
			Width:       p.Width,
			Height:      p.Height,
			Title:       p.Alt,
		})
	}
	return out, nil
}

// SearchPixabayVideoMulti returns up to 15 Pixabay video candidates scored by tag matching.
func SearchPixabayVideoMulti(apiKey, query, orientation string) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Pixabay API key configured")
	}
	searchURL := fmt.Sprintf(
		"https://pixabay.com/api/videos/?key=%s&q=%s&per_page=15&min_duration=5",
		apiKey, url.QueryEscape(query),
	)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("Pixabay video request failed: %w", err)
	}
	defer resp.Body.Close()

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
		return nil, fmt.Errorf("Pixabay video decode failed: %w", err)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	var out []SearchResult
	for _, h := range result.Hits {
		tags := strings.ToLower(h.Tags)
		score := 0
		for _, w := range queryWords {
			if len(w) > 3 && strings.Contains(tags, w) {
				score++
			}
		}
		if h.Duration >= 15 {
			score++
		}

		dlLink := bestPixabayFile(h.Videos.Large, h.Videos.Medium, h.Videos.Small, orientation)
		if dlLink == "" {
			continue
		}
		// use small file as thumb (browser can render as <video>)
		thumbLink := bestPixabayFile(h.Videos.Small, h.Videos.Medium, h.Videos.Large, orientation)
		if thumbLink == "" {
			thumbLink = dlLink
		}

		out = append(out, SearchResult{
			ThumbURL:    thumbLink,
			DownloadURL: dlLink,
			PageURL:     h.PageURL,
			Source:      "pixabay-video",
			Score:       score,
			Duration:    h.Duration,
			IsVideo:     true,
			Title:       h.Tags,
		})
	}
	return out, nil
}

// SearchPixabayPhotoMulti returns up to 15 Pixabay photo candidates scored by tag matching.
func SearchPixabayPhotoMulti(apiKey, query string) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Pixabay API key configured")
	}
	searchURL := fmt.Sprintf(
		"https://pixabay.com/api/?key=%s&q=%s&image_type=photo&per_page=15&safesearch=true&min_width=800",
		apiKey, url.QueryEscape(query),
	)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("Pixabay photo request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Hits []struct {
			LargeImageURL string `json:"largeImageURL"`
			WebformatURL  string `json:"webformatURL"` // smaller, good for thumbnails
			PageURL       string `json:"pageURL"`
			Tags          string `json:"tags"`
			ImageWidth    int    `json:"imageWidth"`
			ImageHeight   int    `json:"imageHeight"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Pixabay photo decode failed: %w", err)
	}

	queryWords := strings.Fields(strings.ToLower(query))
	var out []SearchResult
	for _, h := range result.Hits {
		if h.LargeImageURL == "" {
			continue
		}
		tags := strings.ToLower(h.Tags)
		score := 0
		for _, w := range queryWords {
			if len(w) > 3 && strings.Contains(tags, w) {
				score++
			}
		}
		if h.ImageWidth > h.ImageHeight {
			score++ // prefer landscape
		}
		thumb := h.WebformatURL
		if thumb == "" {
			thumb = h.LargeImageURL
		}
		out = append(out, SearchResult{
			ThumbURL:    thumb,
			DownloadURL: h.LargeImageURL,
			PageURL:     h.PageURL,
			Source:      "pixabay-photo",
			Score:       score,
			IsVideo:     false,
			Width:       h.ImageWidth,
			Height:      h.ImageHeight,
			Title:       h.Tags,
		})
	}
	return out, nil
}

// SearchCommonsMulti returns up to 8 Wikimedia Commons image candidates for a query.
func SearchCommonsMulti(query string) ([]SearchResult, error) {
	searchURL := fmt.Sprintf(
		"https://commons.wikimedia.org/w/api.php?action=query&list=search&srsearch=%s&srnamespace=6&srlimit=12&format=json",
		url.QueryEscape(query),
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "OriginVault/1.0 (youtube automation pipeline)")
	resp, err := wikiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Commons search failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("Commons rate limited (429) — wait a moment and try again")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Commons returned HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") && !strings.Contains(ct, "text/javascript") {
		body, _ := io.ReadAll(resp.Body)
		_ = body
		return nil, fmt.Errorf("Commons returned non-JSON response (rate limited or blocked) — try again in a moment")
	}

	var searchResult struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return nil, fmt.Errorf("Commons decode failed: %w", err)
	}

	var out []SearchResult
	for _, r := range searchResult.Query.Search {
		title := r.Title
		lower := strings.ToLower(title)
		if strings.Contains(lower, ".svg") || strings.Contains(lower, ".ogg") ||
			strings.Contains(lower, ".ogv") || strings.Contains(lower, ".webm") ||
			strings.Contains(lower, ".mid") || strings.Contains(lower, ".wav") {
			continue
		}

		time.Sleep(200 * time.Millisecond) // polite pacing

		infoURL := fmt.Sprintf(
			"https://commons.wikimedia.org/w/api.php?action=query&titles=%s&prop=imageinfo&iiprop=url|thumburl&iiurlwidth=400&format=json",
			url.QueryEscape(title),
		)
		req2, _ := http.NewRequest("GET", infoURL, nil)
		req2.Header.Set("User-Agent", "OriginVault/1.0 (youtube automation pipeline)")
		resp2, err := wikiClient.Do(req2)
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
			if info.URL == "" || strings.Contains(strings.ToLower(info.URL), ".svg") {
				continue
			}
			thumb := info.ThumbURL
			if thumb == "" {
				thumb = info.URL
			}
			pg := info.DescriptionURL
			if pg == "" {
				pg = "https://commons.wikimedia.org/wiki/" + url.QueryEscape(title)
			}
			out = append(out, SearchResult{
				ThumbURL:    thumb,
				DownloadURL: info.URL,
				PageURL:     pg,
				Source:      "commons",
				IsVideo:     false,
				Title:       strings.TrimPrefix(title, "File:"),
			})
			break
		}
		if len(out) >= 8 {
			break
		}
	}
	return out, nil
}

// SearchDDGMulti returns up to 10 DuckDuckGo image candidates.
func SearchDDGMulti(query string) ([]SearchResult, error) {
	tokenURL := fmt.Sprintf("https://duckduckgo.com/?q=%s&iax=images&ia=images", url.QueryEscape(query))
	req, _ := http.NewRequest("GET", tokenURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := ddgClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DDG token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Newer DDG versions return the vqd token in response headers.
	// Fall back to scanning the HTML body with multiple patterns if headers are absent.
	vqd := resp.Header.Get("x-vqd-4")
	if vqd == "" {
		vqd = resp.Header.Get("x-vqd-3")
	}
	if vqd == "" {
		bodyStr := string(body)
		for _, entry := range []struct{ prefix, end string }{
			{`vqd="`, `"`},
			{`vqd='`, `'`},
			{`data-vqd="`, `"`},
			{`"vqd":"`, `"`},
			{`vqd=`, `&`},
		} {
			if idx := strings.Index(bodyStr, entry.prefix); idx != -1 {
				start := idx + len(entry.prefix)
				end := strings.Index(bodyStr[start:], entry.end)
				if end > 0 {
					vqd = strings.TrimSpace(bodyStr[start : start+end])
					if vqd != "" {
						break
					}
				}
			}
		}
	}
	if vqd == "" {
		return nil, fmt.Errorf("no DDG vqd token found (DDG may have changed their API)")
	}

	imgURL := fmt.Sprintf(
		"https://duckduckgo.com/i.js?q=%s&o=json&p=1&s=0&u=bing&f=,,,,,&l=us-en&vqd=%s",
		url.QueryEscape(query), url.QueryEscape(vqd),
	)
	req2, _ := http.NewRequest("GET", imgURL, nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36")
	req2.Header.Set("Referer", "https://duckduckgo.com/")

	resp2, err := ddgClient.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("DDG image request failed: %w", err)
	}
	defer resp2.Body.Close()

	var ddgResult struct {
		Results []struct {
			Image     string `json:"image"`
			Thumbnail string `json:"thumbnail"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Title     string `json:"title"`
			URL       string `json:"url"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&ddgResult); err != nil {
		return nil, fmt.Errorf("DDG decode failed: %w", err)
	}

	// Collect candidates (skip svg/gif).
	type candidate struct {
		thumb, proxy, pageURL, title string
		width, height                int
	}
	var candidates []candidate
	for _, r := range ddgResult.Results {
		if r.Image == "" {
			continue
		}
		lower := strings.ToLower(r.Image)
		if strings.Contains(lower, ".svg") || strings.Contains(lower, ".gif") {
			continue
		}
		thumb := r.Thumbnail
		if thumb == "" {
			thumb = r.Image
		}
		proxy := "https://external-content.duckduckgo.com/iu/?u=" + url.QueryEscape(r.Image) + "&f=1"
		candidates = append(candidates, candidate{thumb, proxy, r.URL, r.Title, r.Width, r.Height})
	}

	// Probe all candidates in parallel with a short timeout.
	// Filter out any that return non-200 (hotlink-blocked, gone, etc.).
	type probeResult struct {
		idx int
		ok  bool
	}
	probes := make([]probeResult, len(candidates))
	var wg sync.WaitGroup
	probeClient := &http.Client{Timeout: 6 * time.Second}
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, proxyURL string) {
			defer wg.Done()
			req, err := http.NewRequest("GET", proxyURL, nil)
			if err != nil {
				probes[i] = probeResult{i, false}
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
			req.Header.Set("Referer", "https://duckduckgo.com/")
			resp, err := probeClient.Do(req)
			if err != nil {
				probes[i] = probeResult{i, false}
				return
			}
			resp.Body.Close()
			probes[i] = probeResult{i, resp.StatusCode == http.StatusOK}
		}(i, c.proxy)
	}
	wg.Wait()

	var out []SearchResult
	for i, c := range candidates {
		if !probes[i].ok {
			continue
		}
		out = append(out, SearchResult{
			ThumbURL:    c.thumb,
			DownloadURL: c.proxy,
			PageURL:     c.pageURL,
			Source:      "duckduckgo",
			IsVideo:     false,
			Width:       c.width,
			Height:      c.height,
			Title:       c.title,
		})
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}

// SearchSerperImagesMulti returns Google Image Search results via serper.dev.
// page is 1-based; each page returns up to 20 results for infinite scroll.
// Uses the existing Serper API key — no additional signup needed.
func SearchSerperImagesMulti(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("serper API key not configured (add research.serper_api_key to config.yaml)")
	}
	if page < 1 {
		page = 1
	}

	body, _ := json.Marshal(map[string]interface{}{
		"q":    query,
		"num":  20,
		"page": page,
	})

	req, _ := http.NewRequest("POST", "https://google.serper.dev/images", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Serper images request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Serper images API error %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Images []struct {
			Title          string `json:"title"`
			ImageURL       string `json:"imageUrl"`
			ImageWidth     int    `json:"imageWidth"`
			ImageHeight    int    `json:"imageHeight"`
			ThumbnailURL   string `json:"thumbnailUrl"`
			Link           string `json:"link"`
		} `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Serper images decode failed: %w", err)
	}

	var out []SearchResult
	for _, img := range result.Images {
		if img.ImageURL == "" {
			continue
		}
		thumb := img.ThumbnailURL
		if thumb == "" {
			thumb = img.ImageURL
		}
		out = append(out, SearchResult{
			ThumbURL:    thumb,
			DownloadURL: img.ImageURL,
			PageURL:     img.Link,
			Source:      "google",
			IsVideo:     false,
			Width:       img.ImageWidth,
			Height:      img.ImageHeight,
			Title:       img.Title,
		})
		}
	return out, nil
}

// SearchUnsplashMulti returns up to 15 Unsplash photo results for a query.
// page is 1-based; pass different values to get fresh results on re-search.
func SearchUnsplashMulti(accessKey, query string, page int) ([]SearchResult, error) {
	if page < 1 {
		page = 1
	}
	searchURL := fmt.Sprintf(
		"https://api.unsplash.com/search/photos?query=%s&per_page=15&page=%d&orientation=landscape",
		url.QueryEscape(query), page,
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", "Client-ID "+accessKey)
	req.Header.Set("Accept-Version", "v1")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Unsplash request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		snip := string(body); if len(snip) > 200 { snip = snip[:200] }
		return nil, fmt.Errorf("Unsplash error %d: %s", resp.StatusCode, snip)
	}

	var result struct {
		Results []struct {
			Description string `json:"description"`
			AltDesc     string `json:"alt_description"`
			Links       struct{ HTML string `json:"html"` } `json:"links"`
			Urls        struct {
				Full    string `json:"full"`
				Regular string `json:"regular"`
				Small   string `json:"small"`
				Thumb   string `json:"thumb"`
			} `json:"urls"`
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Unsplash decode failed: %w", err)
	}

	var out []SearchResult
	for _, p := range result.Results {
		dl := p.Urls.Full
		if dl == "" {
			dl = p.Urls.Regular
		}
		if dl == "" {
			continue
		}
		thumb := p.Urls.Thumb
		if thumb == "" {
			thumb = p.Urls.Small
		}
		title := p.Description
		if title == "" {
			title = p.AltDesc
		}
		out = append(out, SearchResult{
			ThumbURL:    thumb,
			DownloadURL: dl,
			PageURL:     p.Links.HTML,
			Source:      "unsplash",
			Width:       p.Width,
			Height:      p.Height,
			Title:       title,
		})
	}
	return out, nil
}


// SearchFlickrMulti returns real photos from Flickr — excellent for historical,
// archival, and news photography. Many Getty/AP/Reuters photos are on Flickr.
// page is 1-based; returns 20 results per page.
func SearchFlickrMulti(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("flickr API key not configured (add search.flickr_api_key to config.yaml)")
	}
	if page < 1 {
		page = 1
	}
	searchURL := fmt.Sprintf(
		"https://api.flickr.com/services/rest/?method=flickr.photos.search&api_key=%s&text=%s&per_page=20&page=%d&format=json&nojsoncallback=1&extras=url_m,url_l,url_o,owner_name&sort=relevance&content_type=1",
		apiKey, url.QueryEscape(query), page,
	)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("Flickr request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Photos struct {
			Photo []struct {
				ID    string `json:"id"`
				Owner string `json:"owner"`
				Title string `json:"title"`
				URLM  string `json:"url_m"`
				URLL  string `json:"url_l"`
				URLO  string `json:"url_o"`
				HM    int    `json:"height_m"`
				WM    int    `json:"width_m"`
				HL    int    `json:"height_l"`
				WL    int    `json:"width_l"`
			} `json:"photo"`
		} `json:"photos"`
		Stat string `json:"stat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Flickr decode failed: %w", err)
	}
	if result.Stat != "ok" {
		return nil, fmt.Errorf("Flickr API error: stat=%s", result.Stat)
	}

	var out []SearchResult
	for _, p := range result.Photos.Photo {
		dl := p.URLO
		if dl == "" { dl = p.URLL }
		if dl == "" { dl = p.URLM }
		if dl == "" { continue }
		thumb := p.URLM
		if thumb == "" { thumb = dl }
		w, h := p.WL, p.HL
		if w == 0 { w, h = p.WM, p.HM }
		out = append(out, SearchResult{
			ThumbURL:    thumb,
			DownloadURL: dl,
			PageURL:     fmt.Sprintf("https://www.flickr.com/photos/%s/%s", p.Owner, p.ID),
			Source:      "flickr",
			Width:       w,
			Height:      h,
			Title:       p.Title,
		})
	}
	return out, nil
}

// SearchGoogleCSEMulti returns Google Image Search results via the official
// Custom Search API. Free tier: 100 queries/day.
// Requires: API key (console.cloud.google.com) + Search Engine ID (cse.google.com).
// page is 1-based; Google CSE returns up to 10 results per page.
func SearchGoogleCSEMulti(apiKey, cseID, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Google CSE API key not configured (add search.google_api_key to config.yaml)")
	}
	if cseID == "" {
		return nil, fmt.Errorf("Google CSE ID not configured (add search.google_cse_id to config.yaml)")
	}
	if page < 1 { page = 1 }
	start := (page-1)*10 + 1

	searchURL := fmt.Sprintf(
		"https://www.googleapis.com/customsearch/v1?key=%s&cx=%s&q=%s&searchType=image&num=10&start=%d",
		apiKey, cseID, url.QueryEscape(query), start,
	)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("Google CSE request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Items []struct {
			Title string `json:"title"`
			Link  string `json:"link"`
			Image struct {
				ContextLink   string `json:"contextLink"`
				Width         int    `json:"width"`
				Height        int    `json:"height"`
				ThumbnailLink string `json:"thumbnailLink"`
			} `json:"image"`
		} `json:"items"`
		Error *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Google CSE decode failed: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("Google CSE API error: %s", result.Error.Message)
	}

	var out []SearchResult
	for _, item := range result.Items {
		if item.Link == "" { continue }
		out = append(out, SearchResult{
			ThumbURL:    item.Image.ThumbnailLink,
			DownloadURL: item.Link,
			PageURL:     item.Image.ContextLink,
			Source:      "google-cse",
			Width:       item.Image.Width,
			Height:      item.Image.Height,
			Title:       item.Title,
		})
	}
	return out, nil
}

// SearchBingImagesMulti returns image results from Bing Image Search API.
// Free tier: 1,000 queries/month via Azure Cognitive Services (F1 tier).
// page is 1-based; returns 20 results per page.
func SearchBingImagesMulti(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Bing API key not configured (add search.bing_api_key to config.yaml)")
	}
	if page < 1 { page = 1 }
	offset := (page - 1) * 20

	searchURL := fmt.Sprintf(
		"https://api.bing.microsoft.com/v7.0/images/search?q=%s&count=20&offset=%d&mkt=en-US&safeSearch=Off",
		url.QueryEscape(query), offset,
	)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Ocp-Apim-Subscription-Key", apiKey)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Bing request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Bing API error %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Value []struct {
			Name        string `json:"name"`
			ContentURL  string `json:"contentUrl"`
			HostPageURL string `json:"hostPageUrl"`
			ThumbnailURL string `json:"thumbnailUrl"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("Bing decode failed: %w", err)
	}

	var out []SearchResult
	for _, item := range result.Value {
		if item.ContentURL == "" { continue }
		out = append(out, SearchResult{
			ThumbURL:    item.ThumbnailURL,
			DownloadURL: item.ContentURL,
			PageURL:     item.HostPageURL,
			Source:      "bing",
			Width:       item.Width,
			Height:      item.Height,
			Title:       item.Name,
		})
	}
	return out, nil
}

// searchSerpAPI is a shared helper that calls the SerpAPI JSON endpoint.
func searchSerpAPI(params map[string]string) ([]byte, error) {
	base := "https://serpapi.com/search.json?"
	vals := url.Values{}
	for k, v := range params {
		vals.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(base + vals.Encode())
	if err != nil {
		return nil, fmt.Errorf("SerpAPI request failed: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SerpAPI error %d: %s", resp.StatusCode, string(b))
	}
	return b, nil
}

// SearchSerpAPIGoogleImages returns Google Image Search results via SerpAPI.
// Free tier: 250 searches/month at serpapi.com.
// page is 1-based; uses Google's ijn (image page) parameter internally.
func SearchSerpAPIGoogleImages(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SerpAPI key not configured (add search.serp_api_key to config.yaml)")
	}
	if page < 1 {
		page = 1
	}
	b, err := searchSerpAPI(map[string]string{
		"engine":  "google_images",
		"q":       query,
		"ijn":     fmt.Sprintf("%d", page-1), // 0-indexed
		"api_key": apiKey,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		ImagesResults []struct {
			Thumbnail     string `json:"thumbnail"`
			Original      string `json:"original"`
			Title         string `json:"title"`
			Link          string `json:"link"`
			OriginalWidth  int   `json:"original_width"`
			OriginalHeight int   `json:"original_height"`
		} `json:"images_results"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("SerpAPI Google Images decode failed: %w", err)
	}

	var out []SearchResult
	for _, img := range result.ImagesResults {
		dl := img.Original
		if dl == "" { dl = img.Thumbnail }
		if dl == "" { continue }
		out = append(out, SearchResult{
			ThumbURL:    img.Thumbnail,
			DownloadURL: dl,
			PageURL:     img.Link,
			Source:      "serp-google",
			Width:       img.OriginalWidth,
			Height:      img.OriginalHeight,
			Title:       img.Title,
		})
		if len(out) >= 20 { break }
	}
	return out, nil
}

// SearchSerpAPIBingImages returns Bing Image Search results via SerpAPI.
func SearchSerpAPIBingImages(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SerpAPI key not configured (add search.serp_api_key to config.yaml)")
	}
	if page < 1 { page = 1 }
	first := (page-1)*20 + 1

	b, err := searchSerpAPI(map[string]string{
		"engine":  "bing_images",
		"q":       query,
		"first":   fmt.Sprintf("%d", first),
		"api_key": apiKey,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		Images []struct {
			Thumbnail struct{ Src string `json:"src"` }   `json:"thumbnail"`
			Original  struct{ Src string `json:"src"`; Width int `json:"width"`; Height int `json:"height"` } `json:"original"`
			Link      string `json:"link"`
			Title     string `json:"title"`
		} `json:"images"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("SerpAPI Bing Images decode failed: %w", err)
	}

	var out []SearchResult
	for _, img := range result.Images {
		dl := img.Original.Src
		if dl == "" { dl = img.Thumbnail.Src }
		if dl == "" { continue }
		out = append(out, SearchResult{
			ThumbURL:    img.Thumbnail.Src,
			DownloadURL: dl,
			PageURL:     img.Link,
			Source:      "serp-bing",
			Width:       img.Original.Width,
			Height:      img.Original.Height,
			Title:       img.Title,
		})
		if len(out) >= 20 { break }
	}
	return out, nil
}

// SearchSerpAPIYahooImages returns Yahoo Image Search results via SerpAPI.
func SearchSerpAPIYahooImages(apiKey, query string, page int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SerpAPI key not configured (add search.serp_api_key to config.yaml)")
	}
	if page < 1 { page = 1 }
	b := (page-1)*20 + 1

	raw, err := searchSerpAPI(map[string]string{
		"engine":  "yahoo_images",
		"p":       query,       // Yahoo uses "p" not "q"
		"b":       fmt.Sprintf("%d", b),
		"api_key": apiKey,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		Images []struct {
			Thumbnail string `json:"thumbnail"`
			Original  string `json:"original"`
			Link      string `json:"link"`
			Title     string `json:"title"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("SerpAPI Yahoo Images decode failed: %w", err)
	}

	var out []SearchResult
	for _, img := range result.Images {
		dl := img.Original
		if dl == "" { dl = img.Thumbnail }
		if dl == "" { continue }
		out = append(out, SearchResult{
			ThumbURL:    img.Thumbnail,
			DownloadURL: dl,
			PageURL:     img.Link,
			Source:      "serp-yahoo",
			Width:       img.Width,
			Height:      img.Height,
			Title:       img.Title,
		})
		if len(out) >= 20 { break }
	}
	return out, nil
}

// SearchSerpAPIYouTube returns YouTube video results via SerpAPI.
// Results include video thumbnails and YouTube links — useful for finding
// real historical footage, company commercials, and archive clips.
func SearchSerpAPIYouTube(apiKey, query string) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SerpAPI key not configured (add search.serp_api_key to config.yaml)")
	}

	b, err := searchSerpAPI(map[string]string{
		"engine":       "youtube",
		"search_query": query,
		"api_key":      apiKey,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		VideoResults []struct {
			Title     string `json:"title"`
			Link      string `json:"link"`
			Thumbnail struct {
				Static string `json:"static"`
			} `json:"thumbnail"`
			Channel struct {
				Name string `json:"name"`
			} `json:"channel"`
			Views         string `json:"views"`
			PublishedDate string `json:"published_date"`
			Length        string `json:"length"`
		} `json:"video_results"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("SerpAPI YouTube decode failed: %w", err)
	}

	var out []SearchResult
	for _, v := range result.VideoResults {
		if v.Link == "" { continue }
		title := v.Title
		if v.Channel.Name != "" {
			title += " — " + v.Channel.Name
		}
		if v.Length != "" {
			title += " (" + v.Length + ")"
		}
		out = append(out, SearchResult{
			ThumbURL:    v.Thumbnail.Static,
			DownloadURL: v.Link,
			PageURL:     v.Link,
			Source:      "serp-youtube",
			IsVideo:     true,
			Title:       title,
		})
		if len(out) >= 20 { break }
	}
	return out, nil
}
