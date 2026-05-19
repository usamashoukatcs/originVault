package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ThumbnailPromptResult contains prompts optimized for Imagine Art.
type ThumbnailPromptResult struct {
	FocalElement   string `json:"focal_element"`   // the main visual subject
	VisualConcept  string `json:"visual_concept"`  // 2-4 word concept name
	ColorMood      string `json:"color_mood"`      // dominant palette and light quality
	Prompt         string `json:"prompt"`          // positive prompt — paste into Imagine Art
	NegativePrompt string `json:"negative_prompt"` // paste into Imagine Art's negative prompt field
	TextOverlay    string `json:"text_overlay"`    // bold uppercase words to add in Canva after
	TextPlacement  string `json:"text_placement"`  // where/how to place the text in Canva
	CanvaTips      string `json:"canva_tips"`      // finishing tips to maximize CTR
}

// GenerateThumbnailPrompt uses Claude to create an Imagine Art prompt optimized for
// a high-CTR YouTube thumbnail, tailored to the video's subject, title, and tone.
func GenerateThumbnailPrompt(apiKey, subject, title, summary, scriptExcerpt, accentTheme string) (*ThumbnailPromptResult, error) {
	themeDescription := map[string]string{
		"red":  "dramatic failure, controversy, scandal — deep crimson / blood red lighting, tension and dread",
		"gold": "wealth, rise, success story — warm amber / gold lighting, power and prestige",
		"blue": "technology, innovation, corporate scale — electric blue / cyan rim light, precision and control",
	}
	theme := themeDescription[accentTheme]
	if theme == "" {
		theme = themeDescription["red"]
	}

	excerpt := scriptExcerpt
	if len(excerpt) > 300 {
		excerpt = excerpt[:300] + "..."
	}

	prompt := fmt.Sprintf(`You are a professional YouTube thumbnail art director. Your job is to write an Imagine Art prompt that produces a high-CTR thumbnail image.

=== VIDEO CONTEXT ===
Subject: %s
Title: %s
Tone: %s
Summary: %s
Script opening: "%s"

=== HOW IMAGINE ART WORKS ===
Imagine Art is a Stable Diffusion-based AI image generator.
- Positive prompt: natural language description of what you want, with style tags at the end
- Negative prompt: comma-separated list of what to exclude
- Best models for photorealistic thumbnails: Imagine V5, Realistic Vision V5.1
- Aspect ratio is set separately in the UI — do NOT include aspect ratio in the prompt
- Keep the positive prompt under 120 words — Imagine Art degrades with very long prompts
- Style tags at the end (separated by commas) carry significant weight: "photorealistic, cinematic photography, 8K, dramatic lighting, commercial product shoot"

=== THE WINNING THUMBNAIL FORMULA FOR ORIGIN STORY CHANNELS ===

The formula: "A dramatic spotlight emerges from darkness to reveal the subject"
Psychological trigger: CURIOSITY (what's being revealed?) + TENSION (something important happened)
The viewer's gut reaction should be: "I need to know what happened here."

=== MANDATORY VISUAL RULES ===
1. ONE focal subject — single clear hero object, nothing else competing for attention
2. DARK MOODY BACKGROUND — deep charcoal, near-black, textured (not pure black — that looks fake)
3. ONE STRONG DIRECTIONAL LIGHT — spotlight, rim light, or harsh light from below. No soft box, no flat even light.
4. COMPOSITION — subject in the center-right or right third. Left third intentionally darker — this is where text goes in Canva.
5. PHOTOREALISTIC — NOT cartoon, NOT illustration, NOT render. Think: professional documentary still.
6. NO text, letters, words, or watermarks anywhere in the image
7. NO human faces (faceless channel) — if people are implied, show hands, silhouettes, or objects only
8. PREMIUM FEEL — like a $10,000 commercial shoot, not a stock photo

=== WHAT KILLS CTR — NEVER DO THIS ===
- Generic imagery: office buildings, handshakes, generic cityscapes, charts
- Flat or even lighting — no drama means no clicks
- Multiple competing objects — viewer's eye has nowhere to rest
- Orange/teal color grading — overused, looks dated in 2025
- Any text, logo, or watermark in the generated image

=== REFERENCE ARCHETYPES ===
Failure/scandal story: broken or battered version of the iconic product, blood-red or emergency light from below, shattered glass or dust, deep shadows, foreboding
Rise/success story: pristine, gleaming product on dark velvet or marble, golden spotlight from upper-right, rich warm tones, sense of grandeur
Tech/innovation story: sleek product with electric-blue or cyan rim light, dark industrial or concrete background, cool and precise, futuristic but grounded

=== HOW TO WRITE THE IMAGINE ART PROMPT ===
Structure (exactly in this order):
1. Subject sentence: exact description of the object — brand name, product name, era, condition/state
2. Environment sentence: what it sits on, what surrounds it
3. Lighting sentence: exact light source type, direction, color, intensity
4. Composition note: subject placement, depth of field
5. Comma-separated style tags: photorealistic, cinematic photography, hyperdetailed, 8K resolution, dramatic lighting, professional commercial product photography, ultra-sharp focus, dark moody atmosphere

=== OUTPUT — return ONLY valid JSON, no markdown, no code block ===
{
  "focal_element": "Exact description of the single hero object. Include: brand + product name + era + condition. E.g. 'A cracked and battered Nokia 3310 mobile phone, circa 2000' — not just 'a phone'",
  "visual_concept": "2-4 word creative concept name that captures the emotional angle. E.g. 'Shattered Legacy', 'Rise From Ashes', 'The Hidden Truth', 'Empire's Last Day'",
  "color_mood": "Describe the dominant colors and light quality in plain words. E.g. 'Deep blood-red emergency light from below, dark charcoal marble surface, long harsh shadows'",
  "prompt": "The complete Imagine Art positive prompt. Follow the structure above exactly. 80-120 words. End with style tags. Every sentence must serve the emotional story. No text. No faces. No generic imagery.",
  "negative_prompt": "Comma-separated negative prompt for Imagine Art. Always start with: text, words, letters, watermark, signature, logo, label, face, person, human, hands, fingers, low quality, blurry, noise, grain, cartoon, illustration, anime, 3D render, CGI, painting, sketch — then add 6-10 MORE negatives that are SPECIFIC to this subject to prevent wrong outputs (e.g. if subject is Nokia: 'modern smartphone, iPhone, Android phone, touchscreen, app icons')",
  "text_overlay": "5-8 bold UPPERCASE words to add over the image in Canva. Must create an open question or imply a shocking reveal. E.g. 'THE REAL REASON NOKIA FAILED' or 'HOW LEGO ALMOST DIED'. Match the video's hook.",
  "text_placement": "Specific placement instructions for Canva: which third of the frame (left/center), top or bottom half, font style recommendation, text color (white/yellow — whichever contrasts best), whether to use a drop shadow or semi-transparent box, approximate text size.",
  "canva_tips": "4 specific, actionable finishing tips to maximize CTR in Canva. Include: vignette advice, color overlay opacity if needed, drop shadow settings for text, whether to add any accent elements like an arrow or color stripe."
}`, subject, title, theme, summary, excerpt)

	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1500,
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

	respBytes, _ := io.ReadAll(resp.Body)
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

	var result ThumbnailPromptResult
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		return nil, fmt.Errorf("failed to parse thumbnail prompt JSON: %w\nRaw: %s", err, rawText)
	}
	return &result, nil
}
