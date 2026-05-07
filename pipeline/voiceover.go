package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type VoiceConfig struct {
	APIKey string
	Voice  string // onyx, fable, echo, alloy, nova, shimmer
	Model  string // tts-1 or tts-1-hd
}

type VoiceoverResult struct {
	AudioPath      string
	Characters     []string
	StartTimes     []float64
	EndTimes       []float64
	WordTimestamps []WordTimestamp // real per-word timestamps from Whisper (nil if unavailable)
}

type openAITTSRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
}

const maxCharsPerRequest = 4000 // OpenAI TTS limit is 4096, keep buffer

// GenerateVoiceover generates audio using OpenAI TTS.
// Splits long scripts into chunks and merges audio via FFmpeg.
func GenerateVoiceover(text, outputPath string, cfg VoiceConfig) (*VoiceoverResult, error) {
	chunks := splitIntoChunks(text, maxCharsPerRequest)

	var chunkPaths []string
	tmpDir := outputPath + "_chunks"
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	for i, chunk := range chunks {
		chunkPath := fmt.Sprintf("%s/chunk_%03d.mp3", tmpDir, i)
		if err := generateTTSChunk(chunk, chunkPath, cfg); err != nil {
			return nil, fmt.Errorf("chunk %d failed: %w", i+1, err)
		}
		chunkPaths = append(chunkPaths, chunkPath)
	}

	// merge chunks if more than one
	if len(chunkPaths) == 1 {
		data, err := os.ReadFile(chunkPaths[0])
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			return nil, err
		}
	} else {
		if err := mergeAudioChunks(chunkPaths, outputPath); err != nil {
			return nil, fmt.Errorf("failed to merge audio chunks: %w", err)
		}
	}

	// build placeholder timing (scaled to real duration in AssembleVideo)
	chars := strings.Split(text, "")
	startTimes := make([]float64, len(chars))
	endTimes := make([]float64, len(chars))
	for i := range chars {
		startTimes[i] = float64(i)
		endTimes[i] = float64(i + 1)
	}

	return &VoiceoverResult{
		AudioPath:  outputPath,
		Characters: chars,
		StartTimes: startTimes,
		EndTimes:   endTimes,
	}, nil
}

func generateTTSChunk(text, outputPath string, cfg VoiceConfig) error {
	reqBody := openAITTSRequest{
		Model:          cfg.Model,
		Input:          text,
		Voice:          cfg.Voice,
		ResponseFormat: "mp3",
		Speed:          0.95,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/speech", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("OpenAI TTS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenAI TTS error %d: %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read audio: %w", err)
	}

	return os.WriteFile(outputPath, audioData, 0644)
}

// splitIntoChunks splits text at sentence boundaries under maxChars
func splitIntoChunks(text string, maxChars int) []string {
	if len(text) <= maxChars {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > maxChars {
		// find last sentence boundary before maxChars
		splitAt := -1
		for _, sep := range []string{". ", ".\n", "! ", "? ", "!\n", "?\n"} {
			idx := strings.LastIndex(remaining[:maxChars], sep)
			if idx > splitAt {
				splitAt = idx + len(sep)
			}
		}
		// fallback: split at last space
		if splitAt <= 0 {
			splitAt = strings.LastIndex(remaining[:maxChars], " ")
		}
		// hard split if no space found
		if splitAt <= 0 {
			splitAt = maxChars
		}

		chunks = append(chunks, strings.TrimSpace(remaining[:splitAt]))
		remaining = strings.TrimSpace(remaining[splitAt:])
	}

	if remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks
}

// WordTimestamp holds the start and end time (in seconds) for a single spoken word.
type WordTimestamp struct {
	Word  string
	Start float64
	End   float64
}

// TranscribeWithTimestamps sends the generated audio to OpenAI Whisper and
// returns per-word timestamps. Used for accurate subtitle generation in Shorts.
func TranscribeWithTimestamps(apiKey, audioPath string) ([]WordTimestamp, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open audio: %w", err)
	}
	defer f.Close()

	// build a multipart form body
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	filePart, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, fmt.Errorf("cannot create form file: %w", err)
	}
	if _, err := io.Copy(filePart, f); err != nil {
		return nil, fmt.Errorf("cannot write file to form: %w", err)
	}
	_ = writer.WriteField("model", "whisper-1")
	_ = writer.WriteField("response_format", "verbose_json")
	_ = writer.WriteField("timestamp_granularities[]", "word")
	writer.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Whisper request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Whisper error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Words []struct {
			Word  string  `json:"word"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Whisper response: %w", err)
	}

	out := make([]WordTimestamp, len(result.Words))
	for i, w := range result.Words {
		out[i] = WordTimestamp{Word: strings.TrimSpace(w.Word), Start: w.Start, End: w.End}
	}
	return out, nil
}

func mergeAudioChunks(chunkPaths []string, outputPath string) error {
	// write concat file
	concatFile := outputPath + "_concat.txt"
	var sb strings.Builder
	for _, p := range chunkPaths {
		abs, _ := filepath.Abs(p)
		sb.WriteString(fmt.Sprintf("file '%s'\n", abs))
	}
	if err := os.WriteFile(concatFile, []byte(sb.String()), 0644); err != nil {
		return err
	}
	defer os.Remove(concatFile)

	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", concatFile,
		"-c", "copy",
		outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge failed: %w\n%s", err, string(out))
	}
	return nil
}

// LoadVoiceoverResult reconstructs a VoiceoverResult from an existing MP3 file.
// Used by --resume to skip regenerating audio that already exists.
func LoadVoiceoverResult(audioPath, script string) (*VoiceoverResult, error) {
	chars := strings.Split(script, "")
	startTimes := make([]float64, len(chars))
	endTimes := make([]float64, len(chars))
	for i := range chars {
		startTimes[i] = float64(i)
		endTimes[i] = float64(i + 1)
	}
	return &VoiceoverResult{
		AudioPath:  audioPath,
		Characters: chars,
		StartTimes: startTimes,
		EndTimes:   endTimes,
	}, nil
}

// FindPhraseTime returns the estimated start time (seconds) of a phrase
// based on its character position relative to total audio duration.
// Used as fallback when Whisper timestamps are unavailable.
func FindPhraseTime(phrase, script string, startTimes []float64) float64 {
	if len(startTimes) == 0 {
		return -1
	}
	idx := indexCaseInsensitive(script, phrase)
	if idx < 0 || idx >= len(startTimes) {
		return -1
	}
	return startTimes[idx]
}

// normWords lowercases s, strips punctuation, and splits into words.
func normWords(s string) []string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// FindPhraseTimeFromWords finds the start time of a phrase in Whisper word timestamps.
//
// searchFrom is the word index to begin searching from — pass 0 for the first cue,
// then pass the returned wordIdx for each subsequent cue so the search is monotonic
// (each cue can only match at or after the previous cue's match position).
//
// Returns (startTimeSeconds, wordIndexOfMatch). Returns (-1, 0) if not found.
//
// Match strategy — tries progressively shorter windows, longest first for uniqueness:
//   8 words → 6 → 5 → 4 → 3  (no 2-word fallback — too many false positives)
// First pass is monotonic (searchFrom … end). If that fails, retries from 0 so a
// genuinely mismatched searchFrom doesn't permanently lose the cue.
func FindPhraseTimeFromWords(phrase string, words []WordTimestamp, searchFrom int) (float64, int) {
	if len(words) == 0 || phrase == "" {
		return -1, 0
	}

	phraseWords := normWords(phrase)
	if len(phraseWords) == 0 {
		return -1, 0
	}

	// Build normalised transcript word list once.
	tw := make([]string, len(words))
	for i, w := range words {
		nw := normWords(w.Word)
		if len(nw) > 0 {
			tw[i] = nw[0]
		}
	}

	// allMatch checks whether tw[start:start+n] equals phraseWords[:n].
	allMatch := func(start, n int) bool {
		for j := 0; j < n; j++ {
			if tw[start+j] != phraseWords[j] {
				return false
			}
		}
		return true
	}

	if searchFrom < 0 {
		searchFrom = 0
	}

	// Attempt match lengths from longest to shortest.
	for _, matchLen := range []int{8, 6, 5, 4, 3} {
		if matchLen > len(phraseWords) || matchLen > len(tw) {
			continue
		}
		// First pass: monotonic (searchFrom onward).
		for i := searchFrom; i <= len(tw)-matchLen; i++ {
			if allMatch(i, matchLen) {
				return words[i].Start, i
			}
		}
		// Second pass: full search from beginning — recovers when searchFrom overshoots.
		for i := 0; i < searchFrom && i <= len(tw)-matchLen; i++ {
			if allMatch(i, matchLen) {
				return words[i].Start, i
			}
		}
	}

	return -1, 0
}

func indexCaseInsensitive(s, substr string) int {
	sLower := strings.ToLower(s)
	subLower := strings.ToLower(substr)
	return strings.Index(sLower, subLower)
}
