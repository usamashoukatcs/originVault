package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"

	"github.com/originvault/pipeline"
)

func handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := store.ListProjects()
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to list projects: %v", err))
		return
	}
	if projects == nil {
		projects = []ProjectSummary{}
	}
	jsonOK(w, projects)
}

func handleGetProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	jsonOK(w, p)
}

func handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Topic == "" {
		jsonErr(w, 400, "topic is required")
		return
	}

	p, err := store.CreateProject(body.Topic)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to create project: %v", err))
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, p)
}

func handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := store.DeleteProject(id); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

func handleProjectStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	// return last 50 log lines to keep response small
	log := p.StatusLog
	if len(log) > 50 {
		log = log[len(log)-50:]
	}
	jsonOK(w, map[string]any{
		"status": p.Status,
		"log":    log,
	})
}

// handleGenerateScript kicks off the full script-generation pipeline in the background.
// Flow: subject suggestion → research → GenerateScript → save cues → status=script_done
func handleGenerateScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.Status == "generating" {
		jsonErr(w, 409, "script generation already in progress")
		return
	}

	var body struct {
		VideoLengthMin int    `json:"video_length_min"`
		VideoLengthMax int    `json:"video_length_max"`
		Subject        string `json:"subject"` // optional override
	}
	json.NewDecoder(r.Body).Decode(&body)

	minLen := body.VideoLengthMin
	maxLen := body.VideoLengthMax
	if minLen <= 0 {
		minLen = cfg.Channel.VideoLengthMin
	}
	if maxLen <= 0 {
		maxLen = cfg.Channel.VideoLengthMax
	}
	if minLen <= 0 {
		minLen = 8
	}
	if maxLen <= minLen {
		maxLen = minLen
	}
	videoLength := minLen + rand.Intn(maxLen-minLen+1)

	store.SetStatus(id, "generating", fmt.Sprintf("Starting script generation (%d min video)...", videoLength))
	jsonOK(w, map[string]string{"status": "generating"})

	go func() {
		logf := func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			fmt.Println("[gen]", line)
			store.AppendLog(id, line)
		}

		// Step 1: Subject
		subject := body.Subject
		if subject == "" && p.Topic != "" {
			subject = p.Topic
		}
		if subject == "" {
			logf("Suggesting subject from niche: %s", cfg.Channel.Niche)
			var sugErr error
			subject, sugErr = pipeline.SuggestSubject(cfg.AnthropicAPIKey, cfg.Channel.Niche)
			if sugErr != nil {
				logf("WARNING: could not suggest subject: %v", sugErr)
				subject = p.Topic
			} else {
				logf("Subject: %s", subject)
			}
		}

		// Step 2: Research
		logf("Researching: %s", subject)
		research, resErr := pipeline.ResearchTopic(subject, researchCfg)
		if resErr != nil {
			logf("WARNING: research failed: %v", resErr)
			research = nil
		} else {
			logf("Found %d source(s)", len(research))
			for _, rs := range research {
				logf("  - %s", rs.Title)
			}
		}

		// Step 3: Generate script
		logf("Generating script with Claude...")
		script, scriptErr := pipeline.GenerateScript(cfg.AnthropicAPIKey, cfg.Channel.Niche, videoLength, research)
		if scriptErr != nil {
			store.SetStatus(id, "error", fmt.Sprintf("Script generation failed: %v", scriptErr))
			return
		}
		logf("Script done: %s (%d cues)", script.Title, len(script.ClipCues))

		// Step 4: Save to project
		proj, _ := store.GetProject(id)
		proj.Title = script.Title
		proj.Summary = script.Summary
		proj.Description = script.Description
		proj.Script = script.Script
		proj.Keywords = script.Keywords
		proj.Hashtags = script.Hashtags
		proj.Research = research
		proj.Status = "script_done"

		proj.Cues = make([]DashboardCue, len(script.ClipCues))
		for i, cue := range script.ClipCues {
			proj.Cues[i] = DashboardCue{ClipCue: cue}
		}

		// also save script_output.json for CLI resume compatibility
		saveScriptCompat(proj.OutputDir, script, research)

		if err := store.SaveProject(proj); err != nil {
			logf("ERROR saving project: %v", err)
			store.SetStatus(id, "error", "Failed to save project")
			return
		}
		logf("Script saved. Open the Cue Picker to select images.")
	}()
}

// saveScriptCompat writes script_output.json + script.txt for CLI --resume compatibility.
func saveScriptCompat(outputDir string, script *pipeline.ScriptOutput, research []pipeline.ResearchResult) {
	data, _ := json.MarshalIndent(script, "", "  ")
	_ = writeFile(filepath.Join(outputDir, "script_output.json"), data)

	if len(research) > 0 {
		rd, _ := json.MarshalIndent(research, "", "  ")
		_ = writeFile(filepath.Join(outputDir, "research.json"), rd)
	}
}
