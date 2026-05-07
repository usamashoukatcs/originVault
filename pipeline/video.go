package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// computeClipDurations returns the duration each clip should play.
// Uses Whisper word timestamps to match each cue phrase, giving clips their natural
// speech duration. Falls back to even division if timestamps are missing or a phrase
// can't be matched. Interpolates linearly over any unmatched gaps.
func computeClipDurations(result *VoiceoverResult, cues []ClipCue, numClips int, totalDuration float64) []float64 {
	even := totalDuration / float64(numClips)
	durations := make([]float64, numClips)

	if len(result.WordTimestamps) == 0 {
		for i := range durations {
			durations[i] = even
		}
		fmt.Printf("  No word timestamps — even split: %.1fs per clip\n", even)
		return durations
	}

	// Find start time for every phrase.
	starts := make([]float64, numClips)
	found := make([]bool, numClips)
	searchFrom := 0
	for i := 0; i < numClips && i < len(cues); i++ {
		t, next := FindPhraseTimeFromWords(cues[i].Phrase, result.WordTimestamps, searchFrom)
		if t >= 0 {
			starts[i] = t
			found[i] = true
			searchFrom = next
		}
	}

	// Interpolate any unmatched phrases between two known anchors.
	for i := 0; i < numClips; i++ {
		if found[i] {
			continue
		}
		// Find previous and next found anchor.
		prevT, prevI := 0.0, -1
		for j := i - 1; j >= 0; j-- {
			if found[j] {
				prevT, prevI = starts[j], j
				break
			}
		}
		nextT, nextI := totalDuration, numClips
		for j := i + 1; j < numClips; j++ {
			if found[j] {
				nextT, nextI = starts[j], j
				break
			}
		}
		// Linear interpolation between anchors.
		span := float64(nextI - prevI)
		step := (nextT - prevT) / span
		starts[i] = prevT + step*float64(i-prevI)
	}

	// Convert start times to durations.
	for i := 0; i < numClips; i++ {
		var d float64
		if i < numClips-1 {
			d = starts[i+1] - starts[i]
		} else {
			d = totalDuration - starts[i]
		}
		if d < 0.5 {
			d = 0.5
		}
		durations[i] = d
	}

	matched := 0
	for _, f := range found {
		if f {
			matched++
		}
	}
	fmt.Printf("  Phrase timing: %d/%d matched, durations %.1f–%.1fs\n",
		matched, numClips,
		func() float64 { mn := durations[0]; for _, d := range durations { if d < mn { mn = d } }; return mn }(),
		func() float64 { mx := durations[0]; for _, d := range durations { if d > mx { mx = d } }; return mx }(),
	)
	return durations
}

// AssembleVideo builds the final long-form video.
// clipGroups[i] is the ordered list of clips for cue i. Each clip in a group gets equal
// screen time within the cue's phrase duration (sub-trimmed then concatenated).
// Falls back to even timing when Whisper word timestamps are unavailable.
func AssembleVideo(title string, result *VoiceoverResult, script string, cues []ClipCue, clipGroups [][]string, outputPath string) error {
	if len(clipGroups) == 0 {
		return fmt.Errorf("no clips provided")
	}

	totalDuration, err := getAudioDuration(result.AudioPath)
	if err != nil {
		return fmt.Errorf("failed to get voiceover duration: %w", err)
	}
	fmt.Printf("  Voiceover duration: %.1f seconds\n", totalDuration)

	numCues := len(clipGroups)
	if numCues > len(cues) {
		numCues = len(cues)
	}

	clipDurations := computeClipDurations(result, cues, numCues, totalDuration)

	tmpDir := filepath.Dir(outputPath) + "/tmp_clips"
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	var trimmedClips []string
	for i := 0; i < numCues; i++ {
		group := clipGroups[i]
		if len(group) == 0 {
			group = []string{}
		}
		lt := ""
		if i < len(cues) {
			lt = cues[i].LowerThird
		}
		pipPath := ""
		if i < len(cues) && cues[i].PipQuery != "" {
			candidate := filepath.Join(filepath.Dir(outputPath), fmt.Sprintf("pip_%02d.jpg", i+1))
			if _, statErr := os.Stat(candidate); statErr == nil {
				pipPath = candidate
			}
		}
		d := clipDurations[i]
		trimmed := filepath.Join(tmpDir, fmt.Sprintf("trimmed_%02d.mp4", i+1))

		var trimErr error
		if len(group) <= 1 {
			// Single clip: existing trim logic
			clipPath := ""
			if len(group) == 1 {
				clipPath = group[0]
			}
			if i == 0 {
				trimErr = trimFirstClip(clipPath, trimmed, d, lt, pipPath)
			} else {
				trimErr = trimClip(clipPath, trimmed, d, lt, pipPath)
			}
		} else {
			// Multi-clip group: give each clip equal sub-duration within this cue.
			// Trim each clip to d/N, then concat the sub-trimmed clips.
			subDur := d / float64(len(group))
			if subDur < 0.5 {
				subDur = 0.5
			}
			var subTrimmed []string
			for j, clip := range group {
				sub := filepath.Join(tmpDir, fmt.Sprintf("trimmed_%02d_%02d.mp4", i+1, j+1))
				var subErr error
				if i == 0 && j == 0 {
					subErr = trimFirstClip(clip, sub, subDur, lt, pipPath)
				} else {
					subErr = trimClip(clip, sub, subDur, "", "")
				}
				if subErr != nil {
					fmt.Printf("  WARNING: sub-trim cue %d clip %d failed: %v\n", i+1, j+1, subErr)
					if bErr := createBlackClip(sub, subDur); bErr != nil {
						return fmt.Errorf("failed to create placeholder: %w", bErr)
					}
				}
				abs, _ := filepath.Abs(sub)
				subTrimmed = append(subTrimmed, abs)
			}
			trimErr = concatCopied(subTrimmed, trimmed)
		}

		if trimErr != nil {
			fmt.Printf("  WARNING: trim cue %d failed, using placeholder: %v\n", i+1, trimErr)
			if err2 := createBlackClip(trimmed, d); err2 != nil {
				return fmt.Errorf("failed to create placeholder clip: %w", err2)
			}
		}
		abs, _ := filepath.Abs(trimmed)
		trimmedClips = append(trimmedClips, abs)
	}

	concatFile := filepath.Join(tmpDir, "concat.txt")
	var sb strings.Builder
	for _, clip := range trimmedClips {
		sb.WriteString(fmt.Sprintf("file '%s'\n", clip))
	}
	if err := os.WriteFile(concatFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write concat file: %w", err)
	}

	silentVideo := filepath.Join(tmpDir, "silent_video.mp4")
	concatCmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", concatFile,
		"-c", "copy",
		silentVideo,
	)
	if out, err := concatCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("FFmpeg concat failed: %w\n%s", err, string(out))
	}

	// Merge voiceover. -t caps output at exact audio duration.
	// -c:v copy avoids re-encoding; no subtitle filter needed for long videos.
	mergeCmd := exec.Command("ffmpeg", "-y",
		"-i", silentVideo,
		"-i", result.AudioPath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-t", fmt.Sprintf("%.3f", totalDuration),
		"-c:v", "copy",
		"-c:a", "aac",
		"-af", "afade=t=in:st=0:d=0.15",
		outputPath,
	)
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("FFmpeg merge failed: %w\n%s", err, string(out))
	}

	return nil
}

func createBlackClip(outputPath string, durationSecs float64) error {
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=black:size=1920x1080:rate=30:duration=%.2f", durationSecs),
		"-threads", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// trimFirstClip trims the first clip with cinematic grading + OriginVault channel branding
// overlay in the bottom-right for the first 4 seconds (Company Man intro style).
// pipPath: path to a secondary image file for a corner PiP overlay (empty = no PiP).
func trimFirstClip(inputPath, outputPath string, durationSecs float64, lowerThird, pipPath string) error {
	filterParts := []string{
		"scale=1920:1080:force_original_aspect_ratio=decrease:flags=lanczos",
		"pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black",
		"eq=contrast=1.08:brightness=-0.02:saturation=1.25",
		"unsharp=5:5:0.8:5:5:0.0",
		"drawtext=text='OriginVault':fontcolor=white@0.85:fontsize=36:x=w-text_w-40:y=h-text_h-40:font=Liberation Sans Bold:enable='between(t,0,4)'",
	}
	if lowerThird != "" {
		filterParts = append(filterParts, lowerThirdFilter(lowerThird))
	}

	if pipPath != "" {
		return trimClipWithPip(inputPath, pipPath, outputPath, durationSecs, strings.Join(filterParts, ","))
	}

	cmd := exec.Command("ffmpeg", "-y",
		"-i", inputPath,
		"-t", fmt.Sprintf("%.2f", durationSecs),
		"-vf", strings.Join(filterParts, ","),
		"-r", "30",
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

// trimClip trims a clip with cinematic grading, optional lower-third, and optional PiP overlay.
// Hard cuts — no fades. Fade-out → black → fade-in produces a visible black flash in fast
// cutting, which is worse than the hard cut itself. Captions provide visual continuity.
func trimClip(inputPath, outputPath string, durationSecs float64, lowerThird, pipPath string) error {
	filterParts := []string{
		"scale=1920:1080:force_original_aspect_ratio=decrease:flags=lanczos",
		"pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black",
		"eq=contrast=1.08:brightness=-0.02:saturation=1.25",
		"unsharp=5:5:0.8:5:5:0.0",
	}
	if lowerThird != "" {
		filterParts = append(filterParts, lowerThirdFilter(lowerThird))
	}

	if pipPath != "" {
		return trimClipWithPip(inputPath, pipPath, outputPath, durationSecs, strings.Join(filterParts, ","))
	}

	cmd := exec.Command("ffmpeg", "-y",
		"-i", inputPath,
		"-t", fmt.Sprintf("%.2f", durationSecs),
		"-vf", strings.Join(filterParts, ","),
		"-r", "30",
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

// trimClipWithPip composites a secondary PiP image into the top-right corner of the main clip.
// The PiP image is scaled to 400x225 (landscape, 16:9), placed 20px from top-right with a
// rounded dark border, and fades in over 0.3s after the clip starts (prevents a jarring pop).
func trimClipWithPip(inputPath, pipPath, outputPath string, durationSecs float64, mainFilter string) error {
	// filtergraph: [0:v] → apply main grade → [base]; [1:v] → scale PiP → add border → [pip]; overlay pip on base
	fg := fmt.Sprintf(
		"[0:v]%s[base];"+
			"[1:v]scale=400:225:force_original_aspect_ratio=decrease:flags=lanczos,"+
			"pad=400:225:(ow-iw)/2:(oh-ih)/2:color=black,"+
			"drawbox=x=0:y=0:w=400:h=225:color=black@0.6:t=4,"+
			"fade=t=in:st=0.3:d=0.2[pip];"+
			"[base][pip]overlay=x=W-w-20:y=20",
		mainFilter,
	)
	cmd := exec.Command("ffmpeg", "-y",
		"-i", inputPath,
		"-i", pipPath,
		"-t", fmt.Sprintf("%.2f", durationSecs),
		"-filter_complex", fg,
		"-r", "30",
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

// lowerThirdFilter returns FFmpeg drawtext filter(s) for a pop-up title card animation.
// Text format: "Main Label — Sub Label" (split on " — ").
// Both lines slide up from below the screen in 0.15s (fast snap) and hold for 1.25s.
// No alpha expression — FFmpeg 6.x has issues parsing nested commas inside alpha='if(...)'.
// Plain enable='between(t,0,1.25)' is reliable and the snap-in/snap-out looks clean.
func lowerThirdFilter(text string) string {
	parts := strings.SplitN(text, " — ", 2)
	// Replace apostrophes with Unicode right single quote so they don't conflict
	// with FFmpeg's single-quoted value syntax (e.g. "Tote'm" would break parsing).
	mainLabel := strings.ReplaceAll(escaped(parts[0]), "'", "’")

	mainY   := "if(lt(t,0.15),h+50-140*t/0.15,h-90)"
	subY    := "if(lt(t,0.15),h+90-140*t/0.15,h-50)"
	enable  := "between(t,0,1.25)"
	centerX := "(w-text_w)/2"

	main := fmt.Sprintf(
		"drawtext=text='%s':fontcolor=white:fontsize=40:font=Liberation Sans Bold:x='%s':y='%s':box=1:boxcolor=black@0.75:boxborderw=20:enable='%s'",
		mainLabel, centerX, mainY, enable,
	)
	if len(parts) == 2 {
		subLabel := strings.ReplaceAll(escaped(parts[1]), "'", "’")
		sub := fmt.Sprintf(
			"drawtext=text='%s':fontcolor=#DDDDDD:fontsize=26:font=Liberation Sans:x='%s':y='%s':box=1:boxcolor=black@0.75:boxborderw=12:enable='%s'",
			subLabel, centerX, subY, enable,
		)
		return main + "," + sub
	}
	return main
}

func escaped(text string) string {
	text = strings.ReplaceAll(text, "'", "\\'")
	text = strings.ReplaceAll(text, ":", "\\:")
	return text
}

// concatCopied joins pre-trimmed clips using FFmpeg concat demuxer (stream copy, no re-encode).
// All inputs must have identical codec/resolution/SAR — call this only on clips produced by trimClip.
func concatCopied(clips []string, outputPath string) error {
	tmpList := outputPath + ".list"
	var sb strings.Builder
	for _, c := range clips {
		abs, _ := filepath.Abs(c)
		sb.WriteString(fmt.Sprintf("file '%s'\n", abs))
	}
	if err := os.WriteFile(tmpList, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write concat list: %w", err)
	}
	defer os.Remove(tmpList)
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", tmpList,
		"-c", "copy",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("concat copy failed: %w\n%s", err, string(out))
	}
	return nil
}

func getAudioDuration(audioPath string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		audioPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse duration: %w", err)
	}
	return duration, nil
}
