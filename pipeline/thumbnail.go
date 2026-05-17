package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ThumbnailConfig holds content inputs for thumbnail generation.
type ThumbnailConfig struct {
	HookLine  string
	Title     string
	Topic     string
	Script    string
	ImageFile string // filename of a clip image in the same output directory (optional)
}

type thumbnailTheme struct {
	TopBadge        string
	SubtitleBefore  string
	SubtitleAfter   string
	StatBefore      string
	StatBeforeLabel string
	StatAfter       string
	StatAfterLabel  string
	HookQuestion    string
	HookHighlight   string
	BottomBar       string
	AccentTheme     string // "red" | "gold" | "blue"
	LogoDataURI     string // base64-embedded logo
}

// thumbnailSeq cycles through the 3 designs in order so consecutive generations
// always use a different layout+color combination.
var thumbnailSeq int

// themeColors: accent, title-color, triangle-color
var themeColors = map[string][3]string{
	"red":  {"#e8000d", "#F5C518", "#7a0000"},
	"gold": {"#D4A017", "#FFFFFF", "#6b4c00"},
	"blue": {"#0099FF", "#00DDFF", "#003a70"},
}

// dohLogoClient uses DNS-over-HTTPS when system DNS fails.
var dohLogoClient = &http.Client{
	Timeout: 12 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, addr)
			}
			if ips, err := net.DefaultResolver.LookupHost(ctx, host); err == nil && len(ips) > 0 {
				return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0], port))
			}
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
					if a.Type == 1 {
						return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.Data, port))
					}
				}
			}
			return nil, fmt.Errorf("could not resolve %s", host)
		},
	},
}

// fetchLogoDataURI fetches a company logo via Google's favicon service as a base64 data URI.
func fetchLogoDataURI(domain string) (string, error) {
	logoURL := "https://www.google.com/s2/favicons?domain=" + url.QueryEscape(domain) + "&sz=256"
	resp, err := dohLogoClient.Get(logoURL)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) < 1500 {
		return "", fmt.Errorf("no custom logo found for %s (generic favicon)", domain)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	if i := strings.Index(mime, ";"); i > 0 {
		mime = mime[:i]
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// GenerateThumbnailHTML creates a clickbait YouTube thumbnail as a self-contained HTML file.
func GenerateThumbnailHTML(anthropicKey string, tcfg ThumbnailConfig, outputPath string) error {
	if !strings.HasSuffix(outputPath, ".html") {
		outputPath = strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".html"
	}
	theme, err := getThumbnailTheme(anthropicKey, tcfg)
	if err != nil {
		fmt.Printf("  [thumb] theme API failed (%v), using default\n", err)
		theme = thumbnailTheme{
			TopBadge: "The untold story", SubtitleBefore: "Rise", SubtitleAfter: "Collapse",
			StatBefore: "???", StatBeforeLabel: "At Peak", StatAfter: "$0", StatAfterLabel: "Reality",
			HookQuestion: "What really happened to " + tcfg.Topic + "?", HookHighlight: tcfg.Topic,
			BottomBar: "The full story nobody talks about",
		}
	}
	return os.WriteFile(outputPath, []byte(renderThumbnailHTML(tcfg.Topic, tcfg.ImageFile, theme)), 0644)
}

// getThumbnailTheme asks Claude for all creative content as JSON.
func getThumbnailTheme(apiKey string, tcfg ThumbnailConfig) (thumbnailTheme, error) {
	snippet := tcfg.Script
	if len(snippet) > 600 {
		snippet = snippet[:600]
	}

	prompt := fmt.Sprintf(`You are a YouTube thumbnail copywriter. Return ONLY a valid JSON object — no explanation, no markdown.

Topic: %s
Title: %s
Script opening: %s

Return exactly this JSON:
{
  "top_badge": "...",
  "subtitle_before": "...",
  "subtitle_after": "...",
  "stat_before": "...",
  "stat_before_label": "...",
  "stat_after": "...",
  "stat_after_label": "...",
  "hook_question": "...",
  "hook_highlight": "...",
  "bottom_bar": "...",
  "accent_theme": "...",
  "company_domain": "..."
}

Rules:
- top_badge: 5-9 words, punchy one-liner about the story
- subtitle_before: ONE word — the rise (e.g. "Rise", "Glory", "Boom", "Peak")
- subtitle_after: ONE word — the fall (e.g. "Collapse", "Ruin", "Failure", "Crash")
- stat_before: peak number WITH unit, MUST contain a digit (e.g. "$5B", "$150M", "10M users")
- stat_before_label: 1-3 words (e.g. "Expected", "At Peak", "Raised")
- stat_after: crash number WITH unit, MUST contain a digit OR be "Bankrupt" (e.g. "$0", "$31M")
- stat_after_label: 1-3 words (e.g. "Reality", "Sold For", "Final Revenue")
- hook_question: 10-18 word question with at least one number
- hook_highlight: exact phrase from hook_question to highlight
- bottom_bar: 5-8 word mysterious teaser
- accent_theme: "red" for ANY story where a company/product failed, collapsed, lost money, got sold cheap, went bankrupt, or disappointed — even if it was a tech product (Segway, Nokia, Blackberry, Quibi are all RED). "gold" for massive wealth, success, or empire-building stories. "blue" ONLY for currently-thriving tech or science breakthroughs. When in doubt use "red" — failure is always more dramatic
- company_domain: official website domain (e.g. "segway.com"). Return "" if unknown.`, tcfg.Topic, tcfg.Title, snippet)

	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-sonnet-4-6", "max_tokens": 600,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(body))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return thumbnailTheme{}, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return thumbnailTheme{}, fmt.Errorf("API %d: %s", resp.StatusCode, string(respBytes))
	}

	var apiResp struct {
		Content []struct{ Text string `json:"text"` } `json:"content"`
	}
	if err := json.Unmarshal(respBytes, &apiResp); err != nil || len(apiResp.Content) == 0 {
		return thumbnailTheme{}, fmt.Errorf("bad API response")
	}

	text := apiResp.Content[0].Text
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}

	var raw struct {
		TopBadge        string `json:"top_badge"`
		SubtitleBefore  string `json:"subtitle_before"`
		SubtitleAfter   string `json:"subtitle_after"`
		StatBefore      string `json:"stat_before"`
		StatBeforeLabel string `json:"stat_before_label"`
		StatAfter       string `json:"stat_after"`
		StatAfterLabel  string `json:"stat_after_label"`
		HookQuestion    string `json:"hook_question"`
		HookHighlight   string `json:"hook_highlight"`
		BottomBar       string `json:"bottom_bar"`
		AccentTheme     string `json:"accent_theme"`
		CompanyDomain   string `json:"company_domain"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return thumbnailTheme{}, fmt.Errorf("json parse: %w — text: %s", err, text)
	}

	logoDataURI := ""
	if raw.CompanyDomain != "" {
		uri, logoErr := fetchLogoDataURI(raw.CompanyDomain)
		if logoErr != nil {
			fmt.Printf("  [thumb] logo fetch failed for %s: %v\n", raw.CompanyDomain, logoErr)
		} else {
			logoDataURI = uri
			fmt.Printf("  [thumb] logo fetched for %s\n", raw.CompanyDomain)
		}
	}

	return thumbnailTheme{
		TopBadge: raw.TopBadge, SubtitleBefore: raw.SubtitleBefore, SubtitleAfter: raw.SubtitleAfter,
		StatBefore: raw.StatBefore, StatBeforeLabel: raw.StatBeforeLabel,
		StatAfter: raw.StatAfter, StatAfterLabel: raw.StatAfterLabel,
		HookQuestion: raw.HookQuestion, HookHighlight: raw.HookHighlight,
		BottomBar: raw.BottomBar, AccentTheme: raw.AccentTheme, LogoDataURI: logoDataURI,
	}, nil
}

// ── Design 1: Classic Centered ────────────────────────────────────────────────
// The original layout the user liked. Everything centered, bg-image at low opacity,
// big topic title + rise/fall arc, stats row, hook, bottom bar.
const thumbnailTpl1 = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>Thumbnail</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:1280px;height:720px;overflow:hidden;background:#0d0d0d}
.thumb{width:1280px;height:720px;background:#0d0d0d;position:relative;overflow:hidden;font-family:'Arial Black','Arial Bold',Arial,sans-serif;display:flex;flex-direction:column;justify-content:space-between}
.bg-img{position:absolute;inset:0;background-image:url('{{IMAGE_FILE}}');background-size:cover;background-position:center;opacity:.10;filter:grayscale(60%) blur(2px);z-index:0}
.tri{position:absolute;bottom:0;left:0;right:0;height:45%;background:{{TRIANGLE_COLOR}};clip-path:polygon(0 60%,100% 20%,100% 100%,0 100%);opacity:.22;z-index:0}
.badge{position:absolute;top:20px;left:24px;background:{{ACCENT}};color:#fff;font-size:15px;font-weight:900;letter-spacing:1.5px;text-transform:uppercase;padding:8px 18px;border-radius:3px;z-index:2}
.logo-box{position:absolute;top:18px;right:24px;background:rgba(255,255,255,.08);border:1px solid rgba(255,255,255,.15);border-radius:10px;padding:10px 14px;z-index:3;display:flex;align-items:center;backdrop-filter:blur(4px)}
.logo-box img{height:52px;width:auto;max-width:150px;object-fit:contain;filter:brightness(1.15) drop-shadow(0 2px 6px rgba(0,0,0,.6))}
.center{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:55px 24px 0;position:relative;z-index:1}
.title{font-size:96px;font-weight:900;color:{{TITLE_COLOR}};letter-spacing:-2px;line-height:1;text-transform:uppercase;text-shadow:4px 4px 0 rgba(0,0,0,.65);text-align:center}
.sub{font-size:24px;font-weight:900;color:#fff;letter-spacing:5px;text-transform:uppercase;margin:10px 0 0;display:flex;align-items:center;gap:12px}
.arr{color:{{ACCENT}};font-size:26px}
.divider{width:calc(100% - 48px);height:2.5px;background:{{ACCENT}};margin:0 24px;position:relative;z-index:1}
.stats{display:flex;align-items:center;justify-content:center;position:relative;z-index:1;padding:8px 24px 0}
.stat{text-align:center}
.sn{font-size:58px;font-weight:900;line-height:1;display:block}
.sl{font-size:12px;letter-spacing:2px;color:#888;text-transform:uppercase;margin-top:3px;display:block}
.sa{font-size:58px;font-weight:900;color:{{ACCENT}};padding:0 14px;line-height:1}
.hook{text-align:center;font-size:16px;font-weight:900;color:#ccc;text-transform:uppercase;letter-spacing:1px;padding:8px 24px 10px;position:relative;z-index:1}
.hook span{color:{{TITLE_COLOR}}}
.bar{background:{{ACCENT}};text-align:center;font-size:14px;font-weight:900;color:#fff;letter-spacing:2.5px;text-transform:uppercase;padding:11px 24px;position:relative;z-index:1}
</style></head><body>
<div class="thumb">
{{BG_IMAGE_BLOCK}}
<div class="tri"></div>
<div class="badge">{{TOP_BADGE}}</div>
{{LOGO_BLOCK}}
<div class="center">
  <p class="title">{{TOPIC}}</p>
  <div class="sub"><span>{{SUBTITLE_BEFORE}}</span><span class="arr">&#8594;</span><span>{{SUBTITLE_AFTER}}</span></div>
</div>
<div class="divider"></div>
<div class="stats">
  <div class="stat"><span class="sn" style="color:#fff">{{STAT_BEFORE}}</span><span class="sl">{{STAT_BEFORE_LABEL}}</span></div>
  <div class="sa">&#8594;</div>
  <div class="stat"><span class="sn" style="color:{{ACCENT}}">{{STAT_AFTER}}</span><span class="sl">{{STAT_AFTER_LABEL}}</span></div>
</div>
<div class="hook">{{HOOK_HTML}}</div>
<div class="bar">{{BOTTOM_BAR}}</div>
</div></body></html>`

// ── Design 2: Numbers Hero ────────────────────────────────────────────────────
// The drama numbers ($5B → $75M) are the visual centrepiece at 100px.
// Company name and arc subtitle sit below them. Corner accent triangles frame the card.
const thumbnailTpl2 = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>Thumbnail</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:1280px;height:720px;overflow:hidden;background:#0d0d0d}
.thumb{width:1280px;height:720px;background:#0d0d0d;position:relative;overflow:hidden;font-family:'Arial Black','Arial Bold',Arial,sans-serif;display:flex;flex-direction:column}
.bg-img{position:absolute;inset:0;background-image:url('{{IMAGE_FILE}}');background-size:cover;background-position:center;opacity:.08;filter:grayscale(55%);z-index:0}
.ctl{position:absolute;top:0;left:0;border-top:120px solid {{ACCENT}};border-right:120px solid transparent;z-index:1}
.cbr{position:absolute;bottom:42px;right:0;border-bottom:120px solid {{ACCENT}};border-left:120px solid transparent;opacity:.5;z-index:1}
.badge{position:absolute;top:18px;left:0;right:0;text-align:center;font-size:14px;font-weight:900;letter-spacing:2.5px;text-transform:uppercase;color:rgba(255,255,255,.6);z-index:3}
.logo-box{position:absolute;top:18px;right:24px;background:rgba(0,0,0,.55);border:1px solid rgba(255,255,255,.12);border-radius:10px;padding:9px 13px;z-index:4;display:flex;align-items:center;backdrop-filter:blur(4px)}
.logo-box img{height:46px;width:auto;max-width:130px;object-fit:contain;filter:brightness(1.1) drop-shadow(0 2px 4px rgba(0,0,0,.7))}
.hero{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;position:relative;z-index:2;padding:44px 40px 0}
.nrow{display:flex;align-items:baseline;line-height:1}
.nb{font-size:104px;font-weight:900;color:#fff;line-height:1;text-shadow:4px 4px 0 rgba(0,0,0,.6)}
.na{font-size:68px;font-weight:900;color:{{ACCENT}};padding:0 16px;line-height:1}
.nf{font-size:104px;font-weight:900;color:{{ACCENT}};line-height:1;text-shadow:4px 4px 0 rgba(0,0,0,.6)}
.nlabels{display:flex;width:100%;justify-content:space-around;margin-top:6px}
.nl{font-size:12px;letter-spacing:2px;color:#777;text-transform:uppercase}
.topic{text-align:center;padding:0 24px 6px;position:relative;z-index:2}
.tn{font-size:52px;font-weight:900;color:{{TITLE_COLOR}};letter-spacing:-1px;text-transform:uppercase;text-shadow:3px 3px 0 rgba(0,0,0,.6)}
.arc{font-size:16px;font-weight:900;letter-spacing:5px;color:#666;text-transform:uppercase;margin-top:5px}
.hook{text-align:center;font-size:14px;font-weight:900;color:#bbb;text-transform:uppercase;letter-spacing:1px;padding:8px 24px 10px;position:relative;z-index:2}
.hook span{color:{{TITLE_COLOR}}}
.bar{background:{{ACCENT}};text-align:center;font-size:14px;font-weight:900;color:#fff;letter-spacing:2.5px;text-transform:uppercase;padding:11px 24px;position:relative;z-index:2}
</style></head><body>
<div class="thumb">
{{BG_IMAGE_BLOCK}}
<div class="ctl"></div>
<div class="cbr"></div>
<div class="badge">{{TOP_BADGE}}</div>
{{LOGO_BLOCK}}
<div class="hero">
  <div class="nrow"><span class="nb">{{STAT_BEFORE}}</span><span class="na">&#8594;</span><span class="nf">{{STAT_AFTER}}</span></div>
  <div class="nlabels"><span class="nl">{{STAT_BEFORE_LABEL}}</span><span class="nl">{{STAT_AFTER_LABEL}}</span></div>
</div>
<div class="topic">
  <p class="tn">{{TOPIC}}</p>
  <p class="arc">{{SUBTITLE_BEFORE}} &rarr; {{SUBTITLE_AFTER}}</p>
</div>
<div class="hook">{{HOOK_HTML}}</div>
<div class="bar">{{BOTTOM_BAR}}</div>
</div></body></html>`

// ── Design 3: Left-Aligned ────────────────────────────────────────────────────
// Same proven flex:1 structure as Design 1 but all text is left-aligned.
// Stronger bg-image with a diagonal gradient overlay, left-edge accent bar,
// and badge/logo positions swapped vs Design 1 for visual variety.
const thumbnailTpl3 = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>Thumbnail</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:1280px;height:720px;overflow:hidden;background:#0d0d0d}
.thumb{width:1280px;height:720px;background:#0d0d0d;position:relative;overflow:hidden;font-family:'Arial Black','Arial Bold',Arial,sans-serif;display:flex;flex-direction:column;justify-content:space-between}
.bg-img{position:absolute;inset:0;background-image:url('{{IMAGE_FILE}}');background-size:cover;background-position:center;opacity:.13;filter:grayscale(60%) blur(1.5px);z-index:0}
.ov{position:absolute;inset:0;background:linear-gradient(105deg,rgba(13,13,13,.96) 0%,rgba(13,13,13,.72) 55%,rgba(13,13,13,.18) 100%);z-index:0}
.vbar{position:absolute;left:0;top:0;bottom:0;width:6px;background:{{ACCENT}};z-index:2}
.badge{position:absolute;top:20px;right:24px;background:{{ACCENT}};color:#fff;font-size:15px;font-weight:900;letter-spacing:1.5px;text-transform:uppercase;padding:8px 18px;border-radius:3px;z-index:2}
.logo-box{position:absolute;top:18px;left:22px;background:rgba(255,255,255,.08);border:1px solid rgba(255,255,255,.15);border-radius:10px;padding:10px 14px;z-index:3;display:flex;align-items:center;backdrop-filter:blur(4px)}
.logo-box img{height:52px;width:auto;max-width:150px;object-fit:contain;filter:brightness(1.15) drop-shadow(0 2px 6px rgba(0,0,0,.6))}
.center{flex:1;display:flex;flex-direction:column;align-items:flex-start;justify-content:center;padding:55px 460px 0 44px;position:relative;z-index:1}
.title{font-size:96px;font-weight:900;color:{{TITLE_COLOR}};letter-spacing:-2px;line-height:1;text-transform:uppercase;text-shadow:4px 4px 0 rgba(0,0,0,.65);text-align:left}
.sub{font-size:22px;font-weight:900;color:#ddd;letter-spacing:5px;text-transform:uppercase;margin:10px 0 0;display:flex;align-items:center;gap:12px}
.arr{color:{{ACCENT}};font-size:24px}
.divider{width:calc(100% - 0px);height:2.5px;background:{{ACCENT}};margin:0 24px 0 44px;position:relative;z-index:1}
.stats{display:flex;align-items:center;justify-content:flex-start;position:relative;z-index:1;padding:8px 24px 0 44px}
.stat{text-align:left}
.sn{font-size:58px;font-weight:900;line-height:1;display:block}
.sl{font-size:12px;letter-spacing:2px;color:#888;text-transform:uppercase;margin-top:3px;display:block}
.sa{font-size:58px;font-weight:900;color:{{ACCENT}};padding:0 14px;line-height:1}
.hook{text-align:left;font-size:15px;font-weight:900;color:#ccc;text-transform:uppercase;letter-spacing:1px;padding:8px 24px 10px 44px;position:relative;z-index:1}
.hook span{color:{{TITLE_COLOR}}}
.bar{background:{{ACCENT}};text-align:center;font-size:14px;font-weight:900;color:#fff;letter-spacing:2.5px;text-transform:uppercase;padding:11px 24px;position:relative;z-index:1}
</style></head><body>
<div class="thumb">
{{BG_IMAGE_BLOCK}}
<div class="ov"></div>
<div class="vbar"></div>
<div class="badge">{{TOP_BADGE}}</div>
{{LOGO_BLOCK}}
<div class="center">
  <p class="title">{{TOPIC}}</p>
  <div class="sub"><span>{{SUBTITLE_BEFORE}}</span><span class="arr">&#8594;</span><span>{{SUBTITLE_AFTER}}</span></div>
</div>
<div class="divider"></div>
<div class="stats">
  <div class="stat"><span class="sn" style="color:#fff">{{STAT_BEFORE}}</span><span class="sl">{{STAT_BEFORE_LABEL}}</span></div>
  <div class="sa">&#8594;</div>
  <div class="stat"><span class="sn" style="color:{{ACCENT}}">{{STAT_AFTER}}</span><span class="sl">{{STAT_AFTER_LABEL}}</span></div>
</div>
<div class="hook">{{HOOK_HTML}}</div>
<div class="bar">{{BOTTOM_BAR}}</div>
</div></body></html>`

func renderThumbnailHTML(topic, imageFile string, t thumbnailTheme) string {
	// Resolve base theme index; default to red
	themeOrder := [3]string{"red", "gold", "blue"}
	baseIdx := 0
	for i, name := range themeOrder {
		if name == t.AccentTheme {
			baseIdx = i
			break
		}
	}

	// Round-robin through the 3 designs so consecutive generations always differ
	tpls := [3]string{thumbnailTpl1, thumbnailTpl2, thumbnailTpl3}
	templateIdx := thumbnailSeq % 3
	thumbnailSeq++
	tpl := tpls[templateIdx]

	// Rotate color theme per template so all 3 designs look visually distinct
	rotated := themeColors[themeOrder[(baseIdx+templateIdx)%3]]
	accent, titleColor, triangleColor := rotated[0], rotated[1], rotated[2]

	bgImageBlock := ""
	if imageFile != "" {
		bgImageBlock = `<div class="bg-img"></div>`
	}

	logoBlock := ""
	if t.LogoDataURI != "" {
		logoBlock = `<div class="logo-box"><img src="` + t.LogoDataURI + `" alt="logo"></div>`
	}

	hookHTML := t.HookQuestion
	if t.HookHighlight != "" {
		hookHTML = strings.ReplaceAll(hookHTML, t.HookHighlight, `<span>`+t.HookHighlight+`</span>`)
	}

	return strings.NewReplacer(
		"{{ACCENT}}", accent,
		"{{TITLE_COLOR}}", titleColor,
		"{{TRIANGLE_COLOR}}", triangleColor,
		"{{IMAGE_FILE}}", imageFile,
		"{{BG_IMAGE_BLOCK}}", bgImageBlock,
		"{{LOGO_BLOCK}}", logoBlock,
		"{{TOP_BADGE}}", t.TopBadge,
		"{{TOPIC}}", strings.ToUpper(topic),
		"{{SUBTITLE_BEFORE}}", t.SubtitleBefore,
		"{{SUBTITLE_AFTER}}", t.SubtitleAfter,
		"{{STAT_BEFORE}}", t.StatBefore,
		"{{STAT_BEFORE_LABEL}}", t.StatBeforeLabel,
		"{{STAT_AFTER}}", t.StatAfter,
		"{{STAT_AFTER_LABEL}}", t.StatAfterLabel,
		"{{HOOK_HTML}}", hookHTML,
		"{{BOTTOM_BAR}}", t.BottomBar,
	).Replace(tpl)
}
