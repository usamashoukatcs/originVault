package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/originvault/pipeline"
)

type selectBody struct {
	DownloadURL string `json:"download_url"`
	ThumbURL    string `json:"thumb_url"`
	PageURL     string `json:"page_url"`
	Source      string `json:"source"`
	IsVideo     bool   `json:"is_video"`
}

// handleSelectClip downloads the chosen image/video and appends it to the cue's clip sequence.
// Each cue holds up to 3 clips; they are concatenated during assembly.
func handleSelectClip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idxStr := r.PathValue("idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		jsonErr(w, 400, "invalid cue index")
		return
	}

	var body selectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DownloadURL == "" {
		jsonErr(w, 400, "download_url is required")
		return
	}

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if idx < 0 || idx >= len(p.Cues) {
		jsonErr(w, 400, fmt.Sprintf("cue index %d out of range (0–%d)", idx, len(p.Cues)-1))
		return
	}

	existing := p.Cues[idx].SelectedClips
	seqNum := len(existing) + 1

	ext := ".mp4"
	if !body.IsVideo {
		ext = ".jpg"
	}
	clipPath := filepath.Join(p.OutputDir, fmt.Sprintf("clip_%02d_%d%s", idx+1, seqNum, ext))

	if err := downloadClipFile(body.DownloadURL, clipPath); err != nil {
		jsonErr(w, 502, fmt.Sprintf("download failed: %v", err))
		return
	}

	newClip := SelectedClipInfo{
		ClipPath: clipPath,
		Source:   body.Source,
		URL:      body.PageURL,
		ThumbURL: body.ThumbURL,
	}

	if len(existing) >= 3 {
		// sequence full — replace the last clip
		existing[len(existing)-1] = newClip
	} else {
		existing = append(existing, newClip)
	}
	p.Cues[idx].SelectedClips = existing
	syncLegacyFields(&p.Cues[idx])

	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to save project: %v", err))
		return
	}
	jsonOK(w, map[string]string{"clip_path": clipPath})
}

// handleRemoveClipFromSequence removes one clip from a cue's sequence by its position.
// DELETE /api/projects/{id}/cues/{idx}/clips/{clipIdx}
func handleRemoveClipFromSequence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idx, err1 := strconv.Atoi(r.PathValue("idx"))
	clipIdx, err2 := strconv.Atoi(r.PathValue("clipIdx"))
	if err1 != nil || err2 != nil {
		jsonErr(w, 400, "invalid index")
		return
	}

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if idx < 0 || idx >= len(p.Cues) {
		jsonErr(w, 400, "cue index out of range")
		return
	}

	clips := p.Cues[idx].SelectedClips
	if clipIdx < 0 || clipIdx >= len(clips) {
		jsonErr(w, 400, "clip index out of range")
		return
	}

	clips = append(clips[:clipIdx], clips[clipIdx+1:]...)
	p.Cues[idx].SelectedClips = clips
	syncLegacyFields(&p.Cues[idx])

	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "removed"})
}

// handleClearClip removes all selected clips for a cue.
func handleClearClip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idxStr := r.PathValue("idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		jsonErr(w, 400, "invalid cue index")
		return
	}

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if idx < 0 || idx >= len(p.Cues) {
		jsonErr(w, 400, "cue index out of range")
		return
	}

	p.Cues[idx].SelectedClips = nil
	p.Cues[idx].SelectedClipPath = ""
	p.Cues[idx].SelectedSource = ""
	p.Cues[idx].SelectedURL = ""
	p.Cues[idx].SelectedThumbURL = ""

	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "cleared"})
}

// handleGenerateAIImage generates an AI image for a cue and appends it to the sequence.
// Body: { prompt?: string, model?: "flux-dev" | "imagen3" | "gpt-image-1" }
func handleGenerateAIImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idxStr := r.PathValue("idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		jsonErr(w, 400, "invalid cue index")
		return
	}

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if idx < 0 || idx >= len(p.Cues) {
		jsonErr(w, 400, fmt.Sprintf("cue index %d out of range", idx))
		return
	}

	var body struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if body.Prompt == "" {
		cue := p.Cues[idx]
		body.Prompt = cue.ImageQuery
		if body.Prompt == "" {
			body.Prompt = cue.Keyword
		}
	}
	if body.Prompt == "" {
		jsonErr(w, 400, "prompt is required")
		return
	}

	model := body.Model
	if model == "" {
		switch {
		case footageCfg.FalAPIKey != "":
			model = "flux-dev"
		case footageCfg.ImagenAPIKey != "":
			model = "imagen3"
		case footageCfg.DALLEAPIKey != "":
			model = "gpt-image-1"
		default:
			jsonErr(w, 400, "no AI image API key configured")
			return
		}
	}

	prompt := pipeline.BuildImagePromptPublic(body.Prompt)
	seqNum := len(p.Cues[idx].SelectedClips) + 1
	imgPath := filepath.Join(p.OutputDir, fmt.Sprintf("ai-gen-%d-%d-%d.png", idx, seqNum, time.Now().UnixMilli()))

	var genErr error
	switch model {
	case "flux-dev":
		if footageCfg.FalAPIKey == "" {
			jsonErr(w, 400, "fal_api_key not configured")
			return
		}
		genErr = pipeline.GenerateImageFalPublic(prompt, footageCfg.FalAPIKey, imgPath, false)
	case "imagen3":
		if footageCfg.ImagenAPIKey == "" {
			jsonErr(w, 400, "imagen_api_key not configured")
			return
		}
		genErr = pipeline.GenerateImageImagen3Public(prompt, footageCfg.ImagenAPIKey, imgPath, false)
	case "gpt-image-1":
		if footageCfg.DALLEAPIKey == "" {
			jsonErr(w, 400, "openai_api_key not configured")
			return
		}
		genErr = pipeline.GenerateImageGPT1Public(prompt, footageCfg.DALLEAPIKey, imgPath, false)
	default:
		jsonErr(w, 400, "unknown model: "+model)
		return
	}

	if genErr != nil {
		jsonErr(w, 500, fmt.Sprintf("AI image generation failed (%s): %v", model, genErr))
		return
	}

	// Keep the PNG — video conversion is deferred to assembly time.
	// Compute a URL that the browser can use as a thumbnail.
	relPath, _ := filepath.Rel(store.RootDir(), imgPath)
	thumbURL := "/output/" + filepath.ToSlash(relPath)

	p2, _ := store.GetProject(id)
	if p2 == nil || idx >= len(p2.Cues) {
		os.Remove(imgPath)
		jsonErr(w, 500, "project not found after generation")
		return
	}

	newClip := SelectedClipInfo{
		ClipPath: imgPath,
		Source:   "ai-" + model,
		URL:      "AI Generated (" + model + ")",
		ThumbURL: thumbURL,
	}
	existing := p2.Cues[idx].SelectedClips
	if len(existing) >= 3 {
		existing[len(existing)-1] = newClip
	} else {
		existing = append(existing, newClip)
	}
	p2.Cues[idx].SelectedClips = existing
	syncLegacyFields(&p2.Cues[idx])

	if err := store.SaveProject(p2); err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to save project: %v", err))
		return
	}
	jsonOK(w, map[string]string{"clip_path": imgPath, "source": "ai-" + model, "thumb_url": thumbURL})
}

// syncLegacyFields keeps the flat SelectedClipPath/Source/URL/ThumbURL fields in sync
// with SelectedClips[0] so old assembly code keeps working.
func syncLegacyFields(cue *DashboardCue) {
	if len(cue.SelectedClips) > 0 {
		cue.SelectedClipPath = cue.SelectedClips[0].ClipPath
		cue.SelectedSource = cue.SelectedClips[0].Source
		cue.SelectedURL = cue.SelectedClips[0].URL
		cue.SelectedThumbURL = cue.SelectedClips[0].ThumbURL
	} else {
		cue.SelectedClipPath = ""
		cue.SelectedSource = ""
		cue.SelectedURL = ""
		cue.SelectedThumbURL = ""
	}
}
