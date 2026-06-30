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
	"time"
)

type TranscribeResult struct {
	Text     string  `json:"text"`
	Duration float64 `json:"duration"`
	Language string  `json:"language"`
}

type whisperSegment struct {
	Text             string  `json:"text"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	CompressionRatio float64 `json:"compression_ratio"`
	AvgLogprob       float64 `json:"avg_logprob"`
}

type whisperVerbose struct {
	Duration float64          `json:"duration"`
	Language string           `json:"language"`
	Segments []whisperSegment `json:"segments"`
}

var whisperSupported = map[string]bool{
	".flac": true, ".m4a": true, ".mp3": true, ".mp4": true,
	".mpeg": true, ".mpga": true, ".oga": true, ".ogg": true,
	".wav": true, ".webm": true,
}

var httpClient = &http.Client{Timeout: 120 * time.Second}

// TranscribeAudio transcribes audio using OpenAI Whisper.
// Splits into 5-minute chunks and filters hallucinated segments using confidence scores.
func TranscribeAudio(apiKey string, audioData []byte, filename string) (*TranscribeResult, error) {
	ext := strings.ToLower(filepath.Ext(filename))

	tmpIn, err := os.CreateTemp("", "transcribe-in-*"+ext)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpIn.Name())
	if _, err := tmpIn.Write(audioData); err != nil {
		return nil, err
	}
	tmpIn.Close()

	// Convert unsupported formats (e.g. .aac) to MP3
	mp3Path := tmpIn.Name()
	if !whisperSupported[ext] {
		mp3Path = tmpIn.Name() + ".mp3"
		defer os.Remove(mp3Path)
		cmd := exec.Command("ffmpeg", "-y", "-i", tmpIn.Name(),
			"-ar", "16000", "-ac", "1", "-b:a", "32k", "-vn", mp3Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("ffmpeg convert: %s", out)
		}
	}

	// Split into 5-minute chunks (~7 chunks for a 32-min file)
	chunkDir, err := os.MkdirTemp("", "transcribe-chunks-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(chunkDir)

	chunkPattern := filepath.Join(chunkDir, "chunk%03d.mp3")
	splitCmd := exec.Command("ffmpeg", "-y", "-i", mp3Path,
		"-f", "segment", "-segment_time", "300", "-c", "copy", chunkPattern)
	if out, err := splitCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg split: %s", out)
	}

	entries, err := os.ReadDir(chunkDir)
	if err != nil {
		return nil, err
	}

	var parts []string
	var totalDuration float64
	var lang string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, _ := entry.Info()
		if info != nil && info.Size() < 10*1024 {
			continue // skip silence-only chunks
		}
		chunkData, err := os.ReadFile(filepath.Join(chunkDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		res, err := sendToWhisper(apiKey, chunkData, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("chunk %s: %w", entry.Name(), err)
		}
		if lang == "" {
			lang = res.Language
		}
		totalDuration += res.Duration

		// Filter segments by Whisper's own confidence scores
		var kept []string
		for _, seg := range res.Segments {
			if seg.NoSpeechProb > 0.6 {
				continue // Whisper says: probably no speech
			}
			if seg.CompressionRatio > 2.4 {
				continue // repetition loop detected
			}
			if seg.AvgLogprob < -1.0 {
				continue // very low confidence
			}
			if t := strings.TrimSpace(seg.Text); t != "" {
				kept = append(kept, t)
			}
		}
		if len(kept) > 0 {
			parts = append(parts, strings.Join(kept, " "))
		}
	}

	return &TranscribeResult{
		Text:     strings.Join(parts, "\n\n"),
		Duration: totalDuration,
		Language: lang,
	}, nil
}

func sendToWhisper(apiKey string, audioData []byte, filename string) (*whisperVerbose, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, err := w.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(audioData); err != nil {
		return nil, err
	}
	_ = w.WriteField("model", "whisper-1")
	_ = w.WriteField("response_format", "verbose_json")
	_ = w.WriteField("temperature", "0")
	w.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(body, &e)
		return nil, fmt.Errorf("whisper API error: %s", e.Error.Message)
	}

	var result whisperVerbose
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
