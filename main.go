package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/originvault/pipeline"
	"gopkg.in/yaml.v3"
)

type Config struct {
	AnthropicAPIKey string `yaml:"anthropic_api_key"`
	PexelsAPIKey       string `yaml:"pexels_api_key"`
	PixabayAPIKey      string `yaml:"pixabay_api_key"`
	UnsplashAccessKey  string `yaml:"unsplash_access_key"`
	FalAPIKey          string `yaml:"fal_api_key"`
	OpenAIAPIKey    string `yaml:"openai_api_key"`
	ImagenAPIKey    string `yaml:"imagen_api_key"`
	Voice           struct {
		Voice string `yaml:"voice"`
		Model string `yaml:"model"`
	} `yaml:"voice"`
	Channel struct {
		Niche          string `yaml:"niche"`
		VideoLengthMin int    `yaml:"video_length_min"`
		VideoLengthMax int    `yaml:"video_length_max"`
	} `yaml:"channel"`
	Research struct {
		SerperAPIKey string `yaml:"serper_api_key"`
		NewsAPIKey   string `yaml:"news_api_key"`
	} `yaml:"research"`
	OutputDir string `yaml:"output_dir"`
}

func main() {
	// --resume <folder>  : resume a crashed run from its saved script_output.json
	// --subject <name>   : force a specific subject (e.g. --subject "Blockbuster")
	// --restore <file>   : restore from a backup JSON in backups/ and create a new video folder
	// --test             : 1-minute script, 6 clips, no shorts — minimal API cost for testing
	resumeFolder := flag.String("resume", "", "Resume a failed run from its output folder")
	forceSubject := flag.String("subject", "", "Force a specific subject (e.g. --subject \"Blockbuster\")")
	restoreFile  := flag.String("restore", "", "Restore from a backup file in backups/ (e.g. --restore backups/the-dark-secret-behind-blockbuster.json)")
	testMode     := flag.Bool("test", false, "Test mode: 1-minute script, 6 clips, no shorts")
	flag.Parse()

	cfg, err := loadConfig("config.yaml")
	if err != nil {
		fmt.Printf("ERROR loading config: %v\n", err)
		os.Exit(1)
	}

	voiceCfg := pipeline.VoiceConfig{
		APIKey: cfg.OpenAIAPIKey,
		Voice:  cfg.Voice.Voice,
		Model:  cfg.Voice.Model,
	}
	footageCfg := pipeline.FootageConfig{
		PexelsAPIKey:      cfg.PexelsAPIKey,
		PixabayAPIKey:     cfg.PixabayAPIKey,
		UnsplashAccessKey: cfg.UnsplashAccessKey,
		FalAPIKey:         cfg.FalAPIKey,
		ImagenAPIKey:      cfg.ImagenAPIKey,
		DALLEAPIKey:       cfg.OpenAIAPIKey,
	}
	researchCfg := pipeline.ResearchConfig{
		SerperAPIKey: cfg.Research.SerperAPIKey,
		NewsAPIKey:   cfg.Research.NewsAPIKey,
	}

	fmt.Println("=== OriginVault Pipeline ===")

	var outputDir string
	var script *pipeline.ScriptOutput
	var research []pipeline.ResearchResult
	// ── RESTORE MODE (deleted folder, script in backups/) ─────────────────────
	if *restoreFile != "" {
		data, err := os.ReadFile(*restoreFile)
		if err != nil {
			fmt.Printf("ERROR: cannot read backup file: %v\n", err)
			os.Exit(1)
		}
		script = &pipeline.ScriptOutput{}
		if err := json.Unmarshal(data, script); err != nil {
			fmt.Printf("ERROR: failed to parse backup file: %v\n", err)
			os.Exit(1)
		}
		// create a fresh output folder for this restored script
		today := time.Now().Format("2006-01-02")
		outputDir = nextVideoDir(filepath.Join(cfg.OutputDir, today))
		os.MkdirAll(outputDir, 0755)
		// copy the backup JSON into the new folder so --resume works too
		data2, _ := json.MarshalIndent(script, "", "  ")
		os.WriteFile(filepath.Join(outputDir, "script_output.json"), data2, 0644)
		os.WriteFile(filepath.Join(outputDir, "script.txt"), []byte(formatUploadSheet(script)), 0644)
		os.WriteFile(filepath.Join(outputDir, "description.txt"), []byte(script.Description), 0644)
		fmt.Printf("Restored: %s\n", script.Title)
		fmt.Printf("Output folder: %s\n\n", outputDir)

	// ── RESUME MODE ──────────────────────────────────────────────────────────
	} else if *resumeFolder != "" {
		outputDir = *resumeFolder
		if _, err := os.Stat(outputDir); os.IsNotExist(err) {
			fmt.Printf("ERROR: folder not found: %s\n", outputDir)
			os.Exit(1)
		}
		fmt.Printf("Resuming from: %s\n\n", outputDir)

		jsonPath := filepath.Join(outputDir, "script_output.json")
		if fileExists(jsonPath) {
			// modern format — full data
			data, err := os.ReadFile(jsonPath)
			if err != nil {
				fmt.Printf("ERROR: cannot read script_output.json: %v\n", err)
				os.Exit(1)
			}
			script = &pipeline.ScriptOutput{}
			if err := json.Unmarshal(data, script); err != nil {
				fmt.Printf("ERROR: failed to parse script_output.json: %v\n", err)
				os.Exit(1)
			}
		} else {
			// legacy format — reconstruct from script.txt + clip_cues.json
			fmt.Println("  script_output.json not found, reconstructing from script.txt + clip_cues.json...")
			script, err = reconstructFromLegacy(outputDir)
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
				fmt.Println("  Cannot resume — use --subject \"TopicName\" to generate a fresh video instead.")
				os.Exit(1)
			}
			// save script_output.json now so future resumes work normally
			data, _ := json.MarshalIndent(script, "", "  ")
			os.WriteFile(jsonPath, data, 0644)
			fmt.Println("  Saved script_output.json for future resumes.")
		}
		fmt.Printf("  Title: %s\n  Cues: %d  Shorts: %d\n\n", script.Title, len(script.ClipCues), len(script.Shorts))

		// reload research for news screenshots (optional — missing is fine)
		if rdata, err := os.ReadFile(filepath.Join(outputDir, "research.json")); err == nil {
			_ = json.Unmarshal(rdata, &research)
			fmt.Printf("  Loaded %d research sources\n", len(research))
		}

	// ── NORMAL MODE ──────────────────────────────────────────────────────────
	} else {
		today := time.Now().Format("2006-01-02")
		outputDir = nextVideoDir(filepath.Join(cfg.OutputDir, today))
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			fmt.Printf("ERROR creating output dir: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Output folder: %s\n", outputDir)

		videoLength := cfg.Channel.VideoLengthMin + rand.Intn(cfg.Channel.VideoLengthMax-cfg.Channel.VideoLengthMin+1)
		if *testMode {
			videoLength = 1
		}
		fmt.Printf("Video length:  %d minutes\n\n", videoLength)

		// STEP 0: Research
		fmt.Println("[0/5] Researching topic...")
		var subject string
		if *forceSubject != "" {
			subject = *forceSubject
			fmt.Printf("  Subject: %s (forced via --subject)\n", subject)
		} else {
			subject, err = pipeline.SuggestSubject(cfg.AnthropicAPIKey, cfg.Channel.Niche)
			if err != nil {
				fmt.Printf("  WARNING: could not suggest subject: %v\n", err)
				subject = ""
			} else {
				fmt.Printf("  Subject: %s\n", subject)
			}
		}
		if subject != "" {
			research, err = pipeline.ResearchTopic(subject, researchCfg)
			if err != nil {
				fmt.Printf("  WARNING: research failed: %v\n", err)
			} else {
				fmt.Printf("  Found %d source(s)\n", len(research))
				for _, r := range research {
					fmt.Printf("    - %s\n      %s\n", r.Title, r.URL)
				}
			}
		}

		// STEP 1: Generate script
		fmt.Println("[1/5] Generating script with Claude...")
		script, err = pipeline.GenerateScript(cfg.AnthropicAPIKey, cfg.Channel.Niche, videoLength, research)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Title:     %s\n", script.Title)
		fmt.Printf("  Clip cues: %d\n", len(script.ClipCues))
		fmt.Printf("  Shorts:    %d\n", len(script.Shorts))

		// Save all script data so we can resume if something crashes later
		saveScriptOutput(outputDir, script, research)

		// STEP 2: Log to Excel
		fmt.Println("[2/5] Logging to Excel...")
		today2 := time.Now().Format("2006-01-02")
		record := pipeline.VideoRecord{
			Date:        today2,
			Title:       script.Title,
			Summary:     script.Summary,
			Description: script.Description,
			Keywords:    script.Keywords,
			Hashtags:    script.Hashtags,
			Status:      "draft",
			Folder:      outputDir,
		}
		if len(script.Shorts) >= 1 {
			record.Short1Title = script.Shorts[0].Title
			record.Short1Keys = script.Shorts[0].Scenes
		}
		if len(script.Shorts) >= 2 {
			record.Short2Title = script.Shorts[1].Title
			record.Short2Keys = script.Shorts[1].Scenes
		}
		if len(script.Shorts) >= 3 {
			record.Short3Title = script.Shorts[2].Title
			record.Short3Keys = script.Shorts[2].Scenes
		}
		if err := pipeline.LogToExcel(record); err != nil {
			fmt.Printf("  WARNING: %v\n", err)
		} else {
			fmt.Println("  Logged to videos.xlsx")
		}
	}

	// ── STEPS 3-5: always run (resume or fresh) ───────────────────────────────

	// STEP 3: Voiceover — skip if already exists
	voicePath := filepath.Join(outputDir, "voiceover.mp3")
	var voiceResult *pipeline.VoiceoverResult
	if fileExists(voicePath) {
		fmt.Println("[3/5] Voiceover already exists, loading...")
		voiceResult, err = pipeline.LoadVoiceoverResult(voicePath, script.Script)
		if err != nil {
			fmt.Printf("ERROR loading voiceover: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("[3/5] Generating voiceover...")
		voiceResult, err = pipeline.GenerateVoiceover(script.Script, voicePath, voiceCfg)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Saved: %s\n", voicePath)

		// Transcribe with Whisper for precise word-level timestamps used in clip timing
		fmt.Printf("  Transcribing for clip sync...")
		wordTS, tsErr := pipeline.TranscribeWithTimestamps(cfg.OpenAIAPIKey, voicePath)
		if tsErr != nil {
			fmt.Printf(" WARNING: Whisper failed (%v), using approximate timing\n", tsErr)
		} else {
			voiceResult.WordTimestamps = wordTS
			fmt.Printf(" %d words\n", len(wordTS))
		}
	}

	// STEP 4: Download clips + assemble video
	videoPath := filepath.Join(outputDir, "draft_video.mp4")
	if fileExists(videoPath) {
		fmt.Println("[4/5] draft_video.mp4 already exists, skipping footage + assembly.")
	} else {
		fmt.Println("[4/5] Downloading footage...")
		clipPaths, clipSources, err := pipeline.DownloadFootageForCues(footageCfg, outputDir, script.ClipCues, research)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Got %d clips\n", len(clipPaths))
		appendFootageSources(outputDir, script.ClipCues, clipSources)
		appendImageCredits(outputDir, script.ClipCues, clipSources)

		fmt.Println("  Assembling video...")
		clipGroups := make([][]string, len(clipPaths))
		for ci, cp := range clipPaths {
			clipGroups[ci] = []string{cp}
		}
		if err := pipeline.AssembleVideo(script.Title, voiceResult, script.Script, script.ClipCues, clipGroups, videoPath); err != nil {
			fmt.Printf("ERROR assembling: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Draft video: %s\n", videoPath)
	}

	// STEP 5: Shorts — skip each one that already has short.mp4
	if *testMode {
		fmt.Println("[5/5] Skipping shorts (test mode).")
		fmt.Printf("\n=== DONE ===\n")
		fmt.Printf("Main video:  %s\n", videoPath)
		fmt.Printf("Description: %s\n", filepath.Join(outputDir, "description.txt"))
		fmt.Printf("Sources:     %s\n", filepath.Join(outputDir, "sources.txt"))
		return
	}
	fmt.Println("[5/5] Generating shorts...")
	for i, short := range script.Shorts {
		shortOut := filepath.Join(outputDir, fmt.Sprintf("short_%d", i+1), "short.mp4")
		if fileExists(shortOut) {
			fmt.Printf("  Short %d already exists, skipping.\n", i+1)
			continue
		}
		fmt.Printf("  Short %d: %s\n", i+1, short.Title)
		shortPath, err := pipeline.GenerateShort(i, short, voiceCfg, footageCfg, outputDir)
		if err != nil {
			fmt.Printf("  WARNING short %d failed: %v\n", i+1, err)
			continue
		}
		fmt.Printf("  Short %d done: %s\n", i+1, shortPath)
	}

	fmt.Printf("\n=== DONE ===\n")
	fmt.Printf("Main video:  %s\n", videoPath)
	fmt.Printf("Description: %s\n", filepath.Join(outputDir, "description.txt"))
	fmt.Printf("Sources:     %s\n", filepath.Join(outputDir, "sources.txt"))
	fmt.Printf("Excel:       videos.xlsx\n")
	fmt.Println("\nReview draft_video.mp4 then upload to YouTube.")
}

// saveScriptOutput saves the full script JSON + human-readable files so the
// run can be resumed later with --resume <folder>.
// Also writes a backup to backups/<slug>.json so deleting the output folder
// doesn't lose the script permanently.
func saveScriptOutput(outputDir string, script *pipeline.ScriptOutput, research []pipeline.ResearchResult) {
	// save research so resume mode can use news screenshots
	if len(research) > 0 {
		if rdata, err := json.MarshalIndent(research, "", "  "); err == nil {
			os.WriteFile(filepath.Join(outputDir, "research.json"), rdata, 0644)
		}
	}

	data, _ := json.MarshalIndent(script, "", "  ")

	// primary copy inside the video folder
	os.WriteFile(filepath.Join(outputDir, "script_output.json"), data, 0644)

	// backup copy in central backups/ folder (safe even if video folder is deleted)
	backupDir := "backups"
	os.MkdirAll(backupDir, 0755)
	slug := sanitizeFilename(script.Title)
	backupPath := filepath.Join(backupDir, slug+".json")
	if err := os.WriteFile(backupPath, data, 0644); err == nil {
		fmt.Printf("  Backup saved: %s\n", backupPath)
	}

	// human-readable helpers
	os.WriteFile(filepath.Join(outputDir, "script.txt"), []byte(formatUploadSheet(script)), 0644)
	os.WriteFile(filepath.Join(outputDir, "description.txt"), []byte(script.Description), 0644)
	cuesBytes, _ := json.MarshalIndent(script.ClipCues, "", "  ")
	os.WriteFile(filepath.Join(outputDir, "clip_cues.json"), cuesBytes, 0644)

	writeSources(outputDir, research)
}

// sanitizeFilename turns a title into a safe filename slug.
// reconstructFromLegacy rebuilds a ScriptOutput from the old-style saved files
// (script.txt + clip_cues.json + description.txt) for folders created before
// script_output.json was introduced.
func reconstructFromLegacy(folder string) (*pipeline.ScriptOutput, error) {
	// read script.txt — format: "TITLE: ...\n\nSUMMARY: ...\n\n---\n\n<script>"
	scriptTxt, err := os.ReadFile(filepath.Join(folder, "script.txt"))
	if err != nil {
		return nil, fmt.Errorf("script.txt not found in %s", folder)
	}

	var title, summary, scriptBody string
	parts := strings.SplitN(string(scriptTxt), "\n\n---\n\n", 2)
	if len(parts) == 2 {
		scriptBody = strings.TrimSpace(parts[1])
		for _, line := range strings.Split(parts[0], "\n") {
			if strings.HasPrefix(line, "TITLE: ") {
				title = strings.TrimPrefix(line, "TITLE: ")
			} else if strings.HasPrefix(line, "SUMMARY: ") {
				summary = strings.TrimPrefix(line, "SUMMARY: ")
			}
		}
	}
	if title == "" || scriptBody == "" {
		return nil, fmt.Errorf("could not parse script.txt")
	}

	// read clip_cues.json
	var cues []pipeline.ClipCue
	cuesData, err := os.ReadFile(filepath.Join(folder, "clip_cues.json"))
	if err != nil {
		return nil, fmt.Errorf("clip_cues.json not found in %s", folder)
	}
	if err := json.Unmarshal(cuesData, &cues); err != nil {
		return nil, fmt.Errorf("failed to parse clip_cues.json: %w", err)
	}

	// read description.txt (optional)
	description := ""
	if descData, err := os.ReadFile(filepath.Join(folder, "description.txt")); err == nil {
		description = string(descData)
	}

	return &pipeline.ScriptOutput{
		Title:       title,
		Summary:     summary,
		Script:      scriptBody,
		Description: description,
		ClipCues:    cues,
		// Shorts not available in legacy format — skipped
	}, nil
}

func sanitizeFilename(title string) string {
	title = strings.ToLower(title)
	var b strings.Builder
	for _, c := range title {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else if c == ' ' || c == '-' {
			b.WriteRune('-')
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.Trim(s, "-")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeSources(outputDir string, research []pipeline.ResearchResult) {
	var sb strings.Builder
	sb.WriteString("=== Research Sources ===\n")
	sb.WriteString("Articles and pages used as source material for this video's script.\n\n")
	if len(research) == 0 {
		sb.WriteString("No web sources were fetched for this video.\n")
	} else {
		for i, r := range research {
			sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n\n", i+1, r.Title, r.URL))
		}
	}
	path := filepath.Join(outputDir, "sources.txt")
	os.WriteFile(path, []byte(sb.String()), 0644)
	fmt.Printf("  Sources saved: %s\n", path)
}

func appendFootageSources(outputDir string, cues []pipeline.ClipCue, sources []pipeline.ClipSource) {
	var sb strings.Builder
	sb.WriteString("\n=== Footage Sources ===\n\n")
	for i, src := range sources {
		keyword := ""
		if i < len(cues) {
			keyword = cues[i].Keyword
		}
		switch src.Source {
		case "pexels", "pexels-forced", "pexels-simplified":
			sb.WriteString(fmt.Sprintf("Clip %02d [Pexels]:     %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "wikipedia":
			sb.WriteString(fmt.Sprintf("Clip %02d [Wikipedia]:  %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "clearbit":
			sb.WriteString(fmt.Sprintf("Clip %02d [Clearbit]:   %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "pixabay":
			sb.WriteString(fmt.Sprintf("Clip %02d [Pixabay]:    %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "pixabay-photo":
			sb.WriteString(fmt.Sprintf("Clip %02d [Pixabay Photo]: %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "unsplash":
			sb.WriteString(fmt.Sprintf("Clip %02d [Unsplash]:         %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "wikimedia-commons":
			sb.WriteString(fmt.Sprintf("Clip %02d [Wikimedia Commons]: %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "duckduckgo":
			sb.WriteString(fmt.Sprintf("Clip %02d [DuckDuckGo]:       %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "fal-kling":
			sb.WriteString(fmt.Sprintf("Clip %02d [Kling AI]:   AI Generated (Kling v1.6)\n             Keyword: %s\n", i+1, keyword))
		case "news":
			sb.WriteString(fmt.Sprintf("Clip %02d [News]:       %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "imagen3":
			sb.WriteString(fmt.Sprintf("Clip %02d [Imagen 3]:   AI Generated\n             Keyword: %s\n", i+1, keyword))
		case "dalle3":
			sb.WriteString(fmt.Sprintf("Clip %02d [DALL-E 3]:   AI Generated\n             Keyword: %s\n", i+1, keyword))
		case "imagen3-animated", "dalle3-animated":
			sb.WriteString(fmt.Sprintf("Clip %02d [Animated]:   %s\n             Keyword: %s\n", i+1, src.URL, keyword))
		case "cached":
			sb.WriteString(fmt.Sprintf("Clip %02d [cached]\n", i+1))
		default:
			sb.WriteString(fmt.Sprintf("Clip %02d [placeholder]\n", i+1))
		}
	}
	path := filepath.Join(outputDir, "sources.txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(sb.String())
}

// appendImageCredits appends a Wikipedia image credits section to description.txt.
// This covers attribution requirements for Wikipedia CC-licensed photos.
func appendImageCredits(outputDir string, cues []pipeline.ClipCue, sources []pipeline.ClipSource) {
	var credits []string
	seen := map[string]bool{}
	for i, src := range sources {
		if src.Source != "wikipedia" || src.URL == "" || seen[src.URL] {
			continue
		}
		seen[src.URL] = true
		label := ""
		if i < len(cues) {
			if cues[i].Person != "" {
				label = cues[i].Person
			} else if cues[i].Product != "" {
				label = cues[i].Product
			} else if cues[i].Company != "" {
				label = cues[i].Company
			}
		}
		if label != "" {
			credits = append(credits, fmt.Sprintf("• %s — %s (Wikipedia, CC license)", label, src.URL))
		}
	}
	if len(credits) == 0 {
		return
	}

	descPath := filepath.Join(outputDir, "description.txt")
	f, err := os.OpenFile(descPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	f.WriteString("\n\n📸 Image Credits (Wikipedia)\n")
	for _, c := range credits {
		f.WriteString(c + "\n")
	}
}

func nextVideoDir(dateDir string) string {
	for i := 1; ; i++ {
		dir := filepath.Join(dateDir, fmt.Sprintf("video_%03d", i))
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir
		}
	}
}

// formatUploadSheet formats a complete YouTube upload cheat-sheet into one file.
// Contains everything needed to publish: title, tags, hashtags, description, and full script.
func formatUploadSheet(script *pipeline.ScriptOutput) string {
	var sb strings.Builder

	line := func(ch string, n int) string {
		result := ""
		for i := 0; i < n; i++ {
			result += ch
		}
		return result
	}

	// ── YouTube Title ────────────────────────────────────────────────────────
	sb.WriteString("YOUTUBE TITLE\n")
	sb.WriteString(line("=", 60) + "\n")
	sb.WriteString(script.Title + "\n\n")

	// ── Keywords / Tags ──────────────────────────────────────────────────────
	sb.WriteString("KEYWORDS  (paste into YouTube Tags field)\n")
	sb.WriteString(line("=", 60) + "\n")
	if len(script.Keywords) > 0 {
		sb.WriteString(strings.Join(script.Keywords, ", ") + "\n\n")
	} else {
		sb.WriteString("(none)\n\n")
	}

	// ── Hashtags ─────────────────────────────────────────────────────────────
	sb.WriteString("HASHTAGS  (add to top of description or end of title)\n")
	sb.WriteString(line("=", 60) + "\n")
	if len(script.Hashtags) > 0 {
		sb.WriteString(strings.Join(script.Hashtags, " ") + "\n\n")
	} else {
		sb.WriteString("(none)\n\n")
	}

	// ── Description ──────────────────────────────────────────────────────────
	sb.WriteString("DESCRIPTION  (paste into YouTube Description box)\n")
	sb.WriteString(line("=", 60) + "\n")
	sb.WriteString(script.Description + "\n\n")

	// ── Summary ───────────────────────────────────────────────────────────────
	sb.WriteString("ONE-LINE SUMMARY\n")
	sb.WriteString(line("=", 60) + "\n")
	sb.WriteString(script.Summary + "\n\n")

	// ── Full Script ──────────────────────────────────────────────────────────
	sb.WriteString(line("-", 60) + "\n")
	sb.WriteString("FULL NARRATION SCRIPT\n")
	sb.WriteString(line("-", 60) + "\n\n")
	sb.WriteString(script.Script + "\n")

	return sb.String()
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config: %w", err)
	}
	return &cfg, nil
}
