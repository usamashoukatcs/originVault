package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/originvault/pipeline"
)

// matches clip_01_1.mp4, clip_02_3.mp4, etc. (image→video intermediates)
var intermediateClipRe = regexp.MustCompile(`^clip_\d{2}_\d+\.mp4$`)

// shortErr truncates error messages to 200 chars so FFmpeg stderr doesn't bloat the status log.
func shortErr(err error) string {
	s := err.Error()
	// Keep only the first line (FFmpeg prints a config dump after the actual error message)
	if nl := strings.Index(s, "\n"); nl > 0 {
		s = s[:nl]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// dohTransport is a shared HTTP transport that resolves DNS via HTTPS (DoH)
// so downloads work even when the system DNS (127.0.0.53) misbehaves.
var dohTransport = &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		}
		// Try system DNS first (fast path)
		if ips, err := net.DefaultResolver.LookupHost(ctx, host); err == nil && len(ips) > 0 {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		}
		// Fall back to DNS-over-HTTPS via Cloudflare then Google
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
					return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.Data, port))
				}
			}
		}
		return nil, fmt.Errorf("could not resolve %s (system DNS and DoH both failed)", host)
	},
}

// handleGenerateVoice kicks off voiceover generation + Whisper transcription.
func handleGenerateVoice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.Script == "" {
		jsonErr(w, 400, "script not generated yet")
		return
	}
	if p.Status == "voicing" {
		jsonErr(w, 409, "voiceover already in progress")
		return
	}

	store.SetStatus(id, "voicing", "Generating voiceover...")
	jsonOK(w, map[string]string{"status": "voicing"})

	go func() {
		logf := func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			fmt.Println("[voice]", line)
			store.AppendLog(id, line)
		}

		voicePath := filepath.Join(p.OutputDir, "voiceover.mp3")
		tsPath := filepath.Join(p.OutputDir, "voiceover_timestamps.json")

		// skip if already exists
		if _, err := os.Stat(voicePath); err == nil {
			logf("Voiceover already exists, loading...")
		} else {
			logf("Calling OpenAI TTS...")
			_, genErr := pipeline.GenerateVoiceover(p.Script, voicePath, voiceCfg)
			if genErr != nil {
				store.SetStatus(id, "error", fmt.Sprintf("TTS failed: %v", genErr))
				return
			}
			logf("Voiceover saved.")
		}

		// Whisper transcription for precise word timestamps
		if _, err := os.Stat(tsPath); err == nil {
			logf("Timestamps already exist, skipping Whisper.")
		} else {
			logf("Transcribing with Whisper for word timestamps...")
			wts, tsErr := pipeline.TranscribeWithTimestamps(cfg.OpenAIAPIKey, voicePath)
			if tsErr != nil {
				logf("WARNING: Whisper failed (%s), will use even timing during assembly", shortErr(tsErr))
			} else {
				data, _ := json.MarshalIndent(wts, "", "  ")
				os.WriteFile(tsPath, data, 0644)
				logf("Transcribed %d words.", len(wts))
			}
		}

		store.SetStatus(id, "voice_done", "Voiceover ready. You can now assemble the video.")
	}()
}

// handleAssemble starts the final video assembly using the user-selected clips.
func handleAssemble(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.Status == "assembling" {
		jsonErr(w, 409, "assembly already in progress")
		return
	}
	if p.Script == "" {
		jsonErr(w, 400, "script not generated yet")
		return
	}

	store.SetStatus(id, "assembling", "Starting assembly...")
	jsonOK(w, map[string]string{"status": "assembling"})

	go func() {
		logf := func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			fmt.Println("[asm]", line)
			store.AppendLog(id, line)
		}

		proj, err := store.GetProject(id)
		if err != nil {
			return
		}

		// 1. Load or generate voiceover
		voicePath := filepath.Join(proj.OutputDir, "voiceover.mp3")
		if _, err := os.Stat(voicePath); os.IsNotExist(err) {
			logf("Generating voiceover...")
			if _, genErr := pipeline.GenerateVoiceover(proj.Script, voicePath, voiceCfg); genErr != nil {
				store.SetStatus(id, "error", fmt.Sprintf("TTS failed: %v", genErr))
				return
			}
			logf("Voiceover done.")
		}

		// 2. Load voiceover result with word timestamps
		voiceResult, loadErr := pipeline.LoadVoiceoverResult(voicePath, proj.Script)
		if loadErr != nil {
			store.SetStatus(id, "error", fmt.Sprintf("failed to load voiceover: %v", loadErr))
			return
		}
		tsPath := filepath.Join(proj.OutputDir, "voiceover_timestamps.json")
		if data, err := os.ReadFile(tsPath); err == nil {
			var wts []pipeline.WordTimestamp
			if json.Unmarshal(data, &wts) == nil {
				voiceResult.WordTimestamps = wts
				logf("Loaded %d word timestamps.", len(wts))
			}
		}
		if len(voiceResult.WordTimestamps) == 0 {
			logf("No timestamps cached — transcribing now...")
			wts, tsErr := pipeline.TranscribeWithTimestamps(cfg.OpenAIAPIKey, voicePath)
			if tsErr == nil {
				voiceResult.WordTimestamps = wts
				data, _ := json.MarshalIndent(wts, "", "  ")
				os.WriteFile(tsPath, data, 0644)
				logf("Transcribed %d words.", len(wts))
			} else {
				logf("WARNING: Whisper failed, using even timing: %s", shortErr(tsErr))
			}
		}

		// 3. Build clip groups from cue selections; fall back to auto-download for unselected cues
		clipGroups, cues := buildClipPaths(proj, logf)
		if len(clipGroups) == 0 {
			store.SetStatus(id, "error", "No clips available — select at least one image per cue")
			return
		}

		// 4. Assemble
		logf("Assembling video (%d cues)...", len(clipGroups))
		videoPath := filepath.Join(proj.OutputDir, "draft_video.mp4")
		if asmErr := pipeline.AssembleVideo(proj.Title, voiceResult, proj.Script, cues, clipGroups, videoPath); asmErr != nil {
			store.SetStatus(id, "error", fmt.Sprintf("Assembly failed: %v", asmErr))
			return
		}
		logf("Draft video assembled.")

		// Clean up intermediate files left in the output directory:
		//   clip_01_1.mp4, clip_01_2.mp4  — image→video conversions
		//   clip_01_seq.mp4               — multi-clip sequence concat
		// Kept: clip_01.mp4 (selected clip), original .jpg/.png images, voiceover, draft video.
		if entries, err := os.ReadDir(proj.OutputDir); err == nil {
			for _, e := range entries {
				n := e.Name()
				if strings.HasSuffix(n, "_seq.mp4") || intermediateClipRe.MatchString(n) {
					os.Remove(filepath.Join(proj.OutputDir, n))
				}
			}
		}

		// 5. Build overlays: auto hook-text + user-defined
		autoOverlays := buildAutoOverlays(proj, voiceResult)
		allOverlays := append(autoOverlays, proj.Overlays...)
		if len(allOverlays) > 0 {
			logf("Burning %d overlay(s) (%d auto hook-text, %d user-defined)...", len(allOverlays), len(autoOverlays), len(proj.Overlays))
			withOverlays := filepath.Join(proj.OutputDir, "final_video.mp4")
			if ovErr := burnOverlays(videoPath, withOverlays, allOverlays); ovErr != nil {
				logf("WARNING: overlay burn failed (%s) — using draft video", shortErr(ovErr))
			} else {
				videoPath = withOverlays
				logf("Overlays applied.")
			}
		}

		// 6. Copy final video to exports/ folder for easy access without browser download
		exportsDir := filepath.Join(store.RootDir(), "exports")
		os.MkdirAll(exportsDir, 0755)
		exportName := sanitize(proj.Title) + ".mp4"
		exportPath := filepath.Join(exportsDir, exportName)
		if cpErr := copyFile(videoPath, exportPath); cpErr == nil {
			logf("Video saved to: %s", exportPath)
		}

		proj, _ = store.GetProject(id)
		proj.VideoPath = videoPath
		proj.Status = "done"
		proj.StatusLog = append(proj.StatusLog, fmt.Sprintf("Done! Video saved to: %s", exportPath))
		store.SaveProject(proj)
	}()
}

// ensureVideo returns a video path for the given clip. If the clip is already an
// MP4 it is returned as-is. Images (.jpg/.jpeg/.png/.webp) are converted to a
// short MP4 alongside the original file (cached — skipped if already done).
func ensureVideo(clipPath, outputDir string, logf func(string, ...any)) string {
	ext := strings.ToLower(filepath.Ext(clipPath))
	if ext == ".mp4" {
		return clipPath
	}
	videoPath := strings.TrimSuffix(clipPath, filepath.Ext(clipPath)) + ".mp4"
	if _, err := os.Stat(videoPath); err == nil {
		return videoPath // already converted
	}
	logf("Converting image → video: %s", filepath.Base(clipPath))
	if err := imageToVideoStatic(clipPath, videoPath, 30.0); err != nil {
		logf("WARNING: image→video failed (%s), using image path", shortErr(err))
		return clipPath
	}
	return videoPath
}

// buildClipPaths returns a clip group per cue and their ClipCues.
// Each group holds 1–3 video paths for that cue; AssembleVideo sub-divides the phrase
// duration evenly across the group so every clip gets equal screen time.
func buildClipPaths(proj *Project, logf func(string, ...any)) ([][]string, []pipeline.ClipCue) {
	var clipGroups [][]string
	var cues []pipeline.ClipCue

	for i, dc := range proj.Cues {
		cues = append(cues, dc.ClipCue)

		// Collect valid clips from the sequence, converting images to video on demand
		var valid []string
		for _, c := range dc.SelectedClips {
			if _, err := os.Stat(c.ClipPath); err != nil {
				continue
			}
			p := ensureVideo(c.ClipPath, proj.OutputDir, logf)
			valid = append(valid, p)
		}
		// Backward compat: fall back to legacy single-clip field
		if len(valid) == 0 && dc.SelectedClipPath != "" {
			if _, err := os.Stat(dc.SelectedClipPath); err == nil {
				p := ensureVideo(dc.SelectedClipPath, proj.OutputDir, logf)
				valid = []string{p}
			}
		}

		if len(valid) > 0 {
			clipGroups = append(clipGroups, valid)
			logf("Cue %d: %d clip(s) — each gets equal sub-duration", i+1, len(valid))
			continue
		}

		// auto-download for unselected cue
		logf("Cue %d: no selection, auto-downloading for %q...", i+1, dc.Keyword)
		clipPath := filepath.Join(proj.OutputDir, fmt.Sprintf("clip_%02d.mp4", i+1))
		src, autoErr := pipeline.GetBestClipPublic(footageCfg, dc.ClipCue, clipPath, false, proj.Research)
		if autoErr != nil {
			logf("  WARNING cue %d: auto-download failed (%s), using placeholder", i+1, shortErr(autoErr))
		} else {
			logf("  Cue %d: auto [%s]", i+1, src.Source)
		}
		clipGroups = append(clipGroups, []string{clipPath})
	}
	return clipGroups, cues
}

// concatenateClips joins multiple MP4 clips into one using FFmpeg concat filter.
// Each clip gets equal screen time within the cue's duration (trimClip handles final timing).
func concatenateClips(clips []string, outputPath string) error {
	args := []string{"-y"}
	for _, c := range clips {
		args = append(args, "-i", c)
	}
	var inputs string
	for i := range clips {
		inputs += fmt.Sprintf("[%d:v]", i)
	}
	filter := fmt.Sprintf("%sconcat=n=%d:v=1:a=0[outv]", inputs, len(clips))
	args = append(args,
		"-filter_complex", filter,
		"-map", "[outv]",
		"-c:v", "libx264", "-preset", "ultrafast", "-an",
		outputPath,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// burnOverlays re-encodes the video with all user-defined text overlays.
func burnOverlays(inputPath, outputPath string, overlays []Overlay) error {
	// build a single -vf filter chain from all overlays
	var parts []string
	for _, ov := range overlays {
		text := strings.ReplaceAll(ov.Text, "'", "\\'")
		text = strings.ReplaceAll(text, ":", "\\:")
		text = strings.ReplaceAll(text, "%", "\\%")

		var xExpr string
		if ov.Centered {
			xExpr = "(w-tw)/2"
		} else {
			xExpr = fmt.Sprintf("w*%.4f/100", ov.X)
		}
		yExpr := fmt.Sprintf("h*%.4f/100", ov.Y)

		fontStyle := "Liberation Sans"
		if ov.Bold {
			fontStyle = "Liberation Sans Bold"
		}
		color := ov.Color
		if color == "" {
			color = "white"
		}
		fs := ov.FontSize
		if fs == 0 {
			fs = 40
		}
		bgOpacity := ov.BgOpacity
		if bgOpacity <= 0 {
			bgOpacity = 0
		}

		dt := fmt.Sprintf(
			"drawtext=text='%s':fontcolor=%s:fontsize=%d:font=%s:x=%s:y=%s:enable='between(t,%.2f,%.2f)'",
			text, color, fs, fontStyle, xExpr, yExpr, ov.StartTime, ov.EndTime,
		)
		if bgOpacity > 0 {
			dt += fmt.Sprintf(":box=1:boxcolor=black@%.2f:boxborderw=10", bgOpacity)
		}
		parts = append(parts, dt)
	}

	vf := strings.Join(parts, ",")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-i", inputPath,
		"-vf", vf,
		"-c:v", "libx264", "-preset", "fast",
		"-c:a", "copy",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg overlay failed: %w\n%s", err, string(out))
	}
	return nil
}

// handleDownload streams the assembled video file to the browser.
func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.VideoPath == "" || p.Status != "done" {
		jsonErr(w, 400, "video not ready yet")
		return
	}
	f, err := os.Open(p.VideoPath)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("cannot open video: %v", err))
		return
	}
	defer f.Close()

	info, _ := f.Stat()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp4\"", sanitize(p.Title)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	io.Copy(w, f)
}

func sanitize(title string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(title) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.Trim(s, "-")
}

// downloadClipFile downloads a remote file to destPath.
func downloadClipFile(fileURL, destPath string) error {
	// Strip UTM/tracking query params from Wikimedia URLs — they trigger stricter rate limits
	if strings.Contains(fileURL, "upload.wikimedia.org") {
		if u, err := url.Parse(fileURL); err == nil {
			u.RawQuery = ""
			fileURL = u.String()
		}
	}

	// Derive referer from the image's own origin — hotlink-protected hosts reject foreign referers.
	referer := "https://commons.wikimedia.org/"
	if parsed, err := url.Parse(fileURL); err == nil {
		referer = parsed.Scheme + "://" + parsed.Host + "/"
	}

	client := &http.Client{Timeout: 5 * time.Minute, Transport: dohTransport}
	delays := []time.Duration{0, 5 * time.Second, 20 * time.Second, 45 * time.Second}
	for attempt, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}
		req, err := http.NewRequest("GET", fileURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "image/jpeg,image/png,image/webp,*/*")
		req.Header.Set("Referer", referer)
		resp, err := client.Do(req)
		if err != nil {
			if attempt == len(delays)-1 {
				return err
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt == len(delays)-1 {
				return fmt.Errorf("download rate limited (429) after %d retries — try again in a minute", len(delays))
			}
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return fmt.Errorf("image blocked by source site (403) — pick a different result")
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, fileURL)
		}
		out, err := os.Create(destPath)
		if err != nil {
			resp.Body.Close()
			return err
		}
		_, err = io.Copy(out, resp.Body)
		resp.Body.Close()
		out.Close()
		return err
	}
	return fmt.Errorf("download failed after retries: %s", fileURL)
}

// imageToVideoStatic converts a still image to a static MP4 (30fps 1fps trick).
// Duration should be generous (30s) — trimClip will cut it to the right length during assembly.
func imageToVideoStatic(imagePath, outputPath string, duration float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	filter := "scale=1920:1080:force_original_aspect_ratio=decrease:flags=lanczos,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-loop", "1",
		"-i", imagePath,
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

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

// buildAutoOverlays creates centered hook-text overlays from cues that have HookText set.
// Each overlay displays for 3 seconds starting at the cue's phrase time.
func buildAutoOverlays(proj *Project, voiceResult *pipeline.VoiceoverResult) []Overlay {
	if len(voiceResult.WordTimestamps) == 0 {
		return nil
	}
	var overlays []Overlay
	searchFrom := 0
	for i, dc := range proj.Cues {
		if dc.HookText == "" {
			t, next := pipeline.FindPhraseTimeFromWords(dc.Phrase, voiceResult.WordTimestamps, searchFrom)
			searchFrom = next
			_ = t
			continue
		}
		t, next := pipeline.FindPhraseTimeFromWords(dc.Phrase, voiceResult.WordTimestamps, searchFrom)
		searchFrom = next
		if t < 0 {
			continue
		}
		overlays = append(overlays, Overlay{
			ID:        fmt.Sprintf("auto_%d", i),
			Text:      dc.HookText,
			X:         50,
			Y:         78,
			StartTime: t,
			EndTime:   t + 3.0,
			FontSize:  60,
			Color:     "white",
			Bold:      true,
			BgOpacity: 0.65,
			Centered:  true,
		})
	}
	return overlays
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
