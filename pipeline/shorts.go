package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GenerateShort creates a full short video: voiceover + vertical clips + burnt-in subtitles
func GenerateShort(index int, short ShortVideo, voiceCfg VoiceConfig, footageCfg FootageConfig, outputDir string) (string, error) {
	shortDir := filepath.Join(outputDir, fmt.Sprintf("short_%d", index+1))
	os.MkdirAll(shortDir, 0755)

	fmt.Printf("    Voiceover for short %d...\n", index+1)
	voicePath := filepath.Join(shortDir, "voiceover.mp3")
	voiceResult, err := GenerateVoiceover(short.Script, voicePath, voiceCfg)
	if err != nil {
		return "", fmt.Errorf("voiceover failed: %w", err)
	}

	duration, err := getAudioDuration(voiceResult.AudioPath)
	if err != nil {
		return "", fmt.Errorf("failed to get duration: %w", err)
	}

	fmt.Printf("    Downloading clips for short %d...\n", index+1)
	clipPaths, _, err := DownloadFootagePortrait(footageCfg, shortDir, short.Scenes)
	if err != nil {
		return "", fmt.Errorf("footage download failed: %w", err)
	}

	// Transcribe audio for real per-word timestamps (much better subtitle sync)
	fmt.Printf("    Transcribing short %d for subtitle timing...\n", index+1)
	wordTimestamps, tsErr := TranscribeWithTimestamps(voiceCfg.APIKey, voiceResult.AudioPath)
	if tsErr != nil {
		fmt.Printf("    WARNING: transcription failed, using even timing: %v\n", tsErr)
		wordTimestamps = nil
	}

	fmt.Printf("    Assembling short %d...\n", index+1)
	outputPath := filepath.Join(shortDir, "short.mp4")
	if err := assembleShort(short, voicePath, clipPaths, outputPath, duration, wordTimestamps); err != nil {
		return "", fmt.Errorf("assembly failed: %w", err)
	}

	return outputPath, nil
}

func assembleShort(short ShortVideo, voicePath string, clipPaths []string, outputPath string, duration float64, wordTimestamps []WordTimestamp) error {
	tmpDir := filepath.Dir(outputPath) + "/tmp"
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	clipDuration := duration / float64(len(clipPaths))

	// trim and scale clips to 1080x1920 (vertical) with Ken Burns pan + cinematic grade
	var trimmedClips []string
	for i, clip := range clipPaths {
		trimmed := filepath.Join(tmpDir, fmt.Sprintf("clip_%02d.mp4", i+1))
		// Ken Burns: scale to 1.3x then pan slowly left→right across the crop window
		vf := strings.Join([]string{
			"scale=1404:2496:force_original_aspect_ratio=increase:flags=lanczos",
			"crop=1080:1920:x='(1404-1080)*t/" + fmt.Sprintf("%.2f", clipDuration) + "':y='(2496-1920)/2'",
			"eq=contrast=1.10:brightness=-0.02:saturation=1.30",
			"unsharp=5:5:0.6:5:5:0.0",
		}, ",")
		cmd := exec.Command("ffmpeg", "-y",
			"-i", clip,
			"-t", fmt.Sprintf("%.2f", clipDuration),
			"-vf", vf,
			"-r", "30",
			"-c:v", "libx264",
			"-preset", "fast",
			"-an",
			trimmed,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			// fallback: plain scale+crop without Ken Burns
			cmd2 := exec.Command("ffmpeg", "-y",
				"-i", clip,
				"-t", fmt.Sprintf("%.2f", clipDuration),
				"-vf", "scale=1080:1920:force_original_aspect_ratio=increase:flags=lanczos,crop=1080:1920,eq=contrast=1.10:brightness=-0.02:saturation=1.30",
				"-r", "30",
				"-c:v", "libx264",
				"-preset", "fast",
				"-an",
				trimmed,
			)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				return fmt.Errorf("trim clip %d failed: %w\n%s\n%s", i+1, err, string(out), string(out2))
			}
		}
		abs, _ := filepath.Abs(trimmed)
		trimmedClips = append(trimmedClips, abs)
	}

	// concat clips
	concatFile := filepath.Join(tmpDir, "concat.txt")
	var sb strings.Builder
	for _, c := range trimmedClips {
		sb.WriteString(fmt.Sprintf("file '%s'\n", c))
	}
	os.WriteFile(concatFile, []byte(sb.String()), 0644)

	silentVideo := filepath.Join(tmpDir, "silent.mp4")
	concatCmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", concatFile,
		"-c", "copy",
		silentVideo,
	)
	if out, err := concatCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("concat failed: %w\n%s", err, string(out))
	}

	// generate ASS subtitle file with word-highlight style
	assPath := filepath.Join(tmpDir, "subtitles.ass")
	if err := generateASS(short.Script, duration, assPath, wordTimestamps); err != nil {
		return fmt.Errorf("ASS generation failed: %w", err)
	}
	absASS, _ := filepath.Abs(assPath)

	// merge voiceover + burn subtitles + hook text at top for first 3 seconds
	hookText := escaped(short.Hook)
	subtitleFilter := fmt.Sprintf(
		"subtitles='%s',"+
			"drawtext=text='%s':fontcolor=white:fontsize=58:x=(w-text_w)/2:y=140:font=Liberation Sans Bold:"+
			"box=1:boxcolor=black@0.6:boxborderw=18:enable='between(t,0,3)'",
		absASS, hookText,
	)

	mergeCmd := exec.Command("ffmpeg", "-y",
		"-i", silentVideo,
		"-i", voicePath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-vf", subtitleFilter,
		"-af", "atrim=start=0.1,asetpts=N/SR/TB,afade=t=in:st=0:d=0.15",
		"-c:v", "libx264",
		"-preset", "fast",
		"-c:a", "aac",
		"-shortest",
		outputPath,
	)
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("merge failed: %w\n%s", err, string(out))
	}

	return nil
}

// generateASS creates an ASS subtitle file with the word-highlight style:
// each chunk shows 3-4 words, all white, one key word highlighted in a
// rotating color (yellow → cyan → orange → pink → green → repeat).
//
// When wordTimestamps is non-nil (Whisper transcription), real per-word timing
// is used for accurate sync. Falls back to evenly distributed timing otherwise.
func generateASS(script string, duration float64, outputPath string, wordTimestamps []WordTimestamp) error {
	// highlight colors in ASS BGR format (&HBBGGRR&), cycling per chunk
	colors := []string{
		"&H00FFFF&", // yellow  #FFFF00
		"&HFFD400&", // cyan    #00D4FF
		"&H0099FF&", // orange  #FF9900
		"&HCC00FF&", // pink    #FF00CC
		"&H00FF66&", // green   #66FF00
	}

	var ass strings.Builder
	ass.WriteString("[Script Info]\n")
	ass.WriteString("ScriptType: v4.00+\n")
	ass.WriteString("PlayResX: 1080\n")
	ass.WriteString("PlayResY: 1920\n")
	ass.WriteString("WrapStyle: 0\n\n")
	ass.WriteString("[V4+ Styles]\n")
	ass.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	ass.WriteString("Style: Default,Liberation Sans Bold,75,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,-1,0,0,0,100,100,0,0,1,5,0,2,40,40,380,1\n\n")
	ass.WriteString("[Events]\n")
	ass.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	const chunkSize = 4

	if len(wordTimestamps) >= 4 {
		// --- Real timing from Whisper ---
		// Group word timestamps into chunks of chunkSize
		for i := 0; i < len(wordTimestamps); i += chunkSize {
			end := i + chunkSize
			if end > len(wordTimestamps) {
				end = len(wordTimestamps)
			}
			group := wordTimestamps[i:end]

			start := group[0].Start
			chunkEnd := group[len(group)-1].End

			words := make([]string, len(group))
			for j, wt := range group {
				words[j] = wt.Word
			}

			color := colors[(i/chunkSize)%len(colors)]
			highlightIdx := findHighlightWord(words)

			var parts []string
			for j, word := range words {
				if j == highlightIdx {
					parts = append(parts, fmt.Sprintf("{\\c%s}%s{\\c&H00FFFFFF&}", color, word))
				} else {
					parts = append(parts, word)
				}
			}

			ass.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
				formatASSTime(start),
				formatASSTime(chunkEnd),
				strings.Join(parts, " "),
			))
		}
	} else {
		// --- Fallback: even distribution ---
		words := strings.Fields(script)
		var chunks [][]string
		for i := 0; i < len(words); i += chunkSize {
			e := i + chunkSize
			if e > len(words) {
				e = len(words)
			}
			chunks = append(chunks, words[i:e])
		}
		timePerChunk := duration / float64(len(chunks))

		for i, chunk := range chunks {
			start := float64(i) * timePerChunk
			chunkEnd := start + timePerChunk
			color := colors[i%len(colors)]
			highlightIdx := findHighlightWord(chunk)

			var parts []string
			for j, word := range chunk {
				if j == highlightIdx {
					parts = append(parts, fmt.Sprintf("{\\c%s}%s{\\c&H00FFFFFF&}", color, word))
				} else {
					parts = append(parts, word)
				}
			}

			ass.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
				formatASSTime(start),
				formatASSTime(chunkEnd),
				strings.Join(parts, " "),
			))
		}
	}

	return os.WriteFile(outputPath, []byte(ass.String()), 0644)
}

// findHighlightWord picks the most important word index in a chunk.
// Priority: numbers/money → proper nouns → power words → last word.
func findHighlightWord(words []string) int {
	// Priority 1: contains a number or currency symbol
	for i, w := range words {
		if containsDigit(w) || strings.ContainsAny(w, "$%£€") {
			return i
		}
	}
	// Priority 2: proper noun (capitalized, not first word)
	for i, w := range words {
		if i > 0 && len(w) > 1 && w[0] >= 'A' && w[0] <= 'Z' {
			return i
		}
	}
	// Priority 3: high-impact words
	impactWords := []string{
		"never", "always", "secret", "billion", "million", "trillion",
		"crashed", "failed", "bankrupt", "launched", "stolen", "lost",
		"won", "died", "fired", "refused", "laughed", "destroyed",
		"built", "sold", "bought", "rejected", "ignored", "biggest",
		"worst", "best", "only", "first", "last", "every",
	}
	for i, w := range words {
		lower := strings.ToLower(strings.Trim(w, ".,!?"))
		for _, impact := range impactWords {
			if lower == impact {
				return i
			}
		}
	}
	// Fallback: last word in chunk
	return len(words) - 1
}

func containsDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}


// formatASSTime formats seconds into ASS timestamp: H:MM:SS.cs
func formatASSTime(seconds float64) string {
	h := int(seconds) / 3600
	m := (int(seconds) % 3600) / 60
	s := int(seconds) % 60
	cs := int((seconds - float64(int(seconds))) * 100)
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}
