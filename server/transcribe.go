package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/originvault/pipeline"
)

// handleTranscribe accepts a multipart audio file upload and returns the Whisper transcript.
func handleTranscribe(w http.ResponseWriter, r *http.Request) {
	if cfg.OpenAIAPIKey == "" {
		jsonErr(w, 400, "OpenAI API key not configured")
		return
	}

	// 30MB max upload
	if err := r.ParseMultipartForm(30 << 20); err != nil {
		jsonErr(w, 400, "file too large (max 30MB)")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, 400, "missing file field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonErr(w, 500, "failed to read file")
		return
	}

	result, err := pipeline.TranscribeAudio(cfg.OpenAIAPIKey, data, header.Filename)
	if err != nil {
		jsonErr(w, 502, fmt.Sprintf("transcription failed: %v", err))
		return
	}

	jsonOK(w, map[string]any{
		"text":     result.Text,
		"duration": result.Duration,
		"language": result.Language,
	})
}
