package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/originvault/pipeline"
)

func handleListIdeas(w http.ResponseWriter, r *http.Request) {
	ideas, err := store.ListIdeas()
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to list ideas: %v", err))
		return
	}
	if ideas == nil {
		ideas = []Idea{}
	}
	jsonOK(w, ideas)
}

func handleGenerateIdeas(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Count int `json:"count"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	count := body.Count
	if count <= 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}

	niche := cfg.Channel.Niche
	if niche == "" {
		jsonErr(w, 400, "channel.niche is not set in config.yaml")
		return
	}

	existing := store.IdeaSubjects()
	subjects, err := pipeline.SuggestSubjectsBatch(cfg.AnthropicAPIKey, niche, count, existing)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to generate ideas: %v", err))
		return
	}

	now := time.Now()
	newIdeas := make([]Idea, len(subjects))
	for i, subject := range subjects {
		newIdeas[i] = Idea{
			ID:        fmt.Sprintf("idea-%d-%02d", now.UnixMilli(), i),
			Subject:   subject,
			Niche:     niche,
			Status:    "idea",
			Keywords:  []string{},
			Hashtags:  []string{},
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
			UpdatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
	}
	if err := store.AddIdeas(newIdeas); err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to save ideas: %v", err))
		return
	}
	ideas, _ := store.ListIdeas()
	if ideas == nil {
		ideas = []Idea{}
	}
	jsonOK(w, ideas)
}

func handleExpandIdea(w http.ResponseWriter, r *http.Request) {
	iid := r.PathValue("iid")
	idea, err := store.GetIdea(iid)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if idea.Status == "expanding" {
		jsonErr(w, 409, "expansion already in progress")
		return
	}

	idea.Status = "expanding"
	idea.ErrorMsg = ""
	store.UpdateIdea(*idea)
	jsonOK(w, idea)

	go func() {
		minLen := cfg.Channel.VideoLengthMin
		maxLen := cfg.Channel.VideoLengthMax
		if minLen <= 0 {
			minLen = 8
		}
		if maxLen <= minLen {
			maxLen = minLen
		}
		videoLength := minLen + rand.Intn(maxLen-minLen+1)

		research, _ := pipeline.ResearchTopic(idea.Subject, researchCfg)

		script, err := pipeline.GenerateScript(cfg.AnthropicAPIKey, cfg.Channel.Niche, videoLength, research)
		if err != nil {
			if current, _ := store.GetIdea(iid); current != nil {
				current.Status = "error"
				current.ErrorMsg = err.Error()
				store.UpdateIdea(*current)
			}
			return
		}

		current, _ := store.GetIdea(iid)
		if current == nil {
			return
		}
		current.Title = script.Title
		current.Summary = script.Summary
		current.Description = script.Description
		current.Script = script.Script
		current.Keywords = script.Keywords
		if current.Keywords == nil {
			current.Keywords = []string{}
		}
		current.Hashtags = script.Hashtags
		if current.Hashtags == nil {
			current.Hashtags = []string{}
		}
		current.Research = research
		current.Cues = make([]DashboardCue, len(script.ClipCues))
		for i, cue := range script.ClipCues {
			current.Cues[i] = DashboardCue{ClipCue: cue}
		}
		current.Status = "ready"
		store.UpdateIdea(*current)
	}()
}

func handleDeleteIdea(w http.ResponseWriter, r *http.Request) {
	iid := r.PathValue("iid")
	if err := store.DeleteIdea(iid); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

func handleExportIdeas(w http.ResponseWriter, r *http.Request) {
	ideas, err := store.ListIdeas()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ideas.csv"`)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Subject", "Title", "Summary", "Description", "Keywords", "Hashtags", "Script", "Status", "Created At"})
	for _, idea := range ideas {
		_ = cw.Write([]string{
			idea.Subject,
			idea.Title,
			idea.Summary,
			idea.Description,
			strings.Join(idea.Keywords, ", "),
			strings.Join(idea.Hashtags, " "),
			idea.Script,
			idea.Status,
			idea.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	cw.Flush()
}

// handleCreateProjectFromIdea creates a full project from an idea.
// If the idea is already expanded (ready), the project starts at script_done with all data populated.
// Otherwise it starts at pending and the user triggers script generation manually.
func handleCreateProjectFromIdea(w http.ResponseWriter, r *http.Request) {
	iid := r.PathValue("iid")
	idea, err := store.GetIdea(iid)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}

	p, err := store.CreateProject(idea.Subject)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to create project: %v", err))
		return
	}

	if idea.Status == "ready" && idea.Script != "" {
		p.Title = idea.Title
		p.Summary = idea.Summary
		p.Description = idea.Description
		p.Script = idea.Script
		p.Keywords = idea.Keywords
		p.Hashtags = idea.Hashtags
		p.Cues = idea.Cues
		p.Research = idea.Research
		p.Status = "script_done"

		scriptOut := &pipeline.ScriptOutput{
			Title:       idea.Title,
			Summary:     idea.Summary,
			Description: idea.Description,
			Script:      idea.Script,
			Keywords:    idea.Keywords,
			Hashtags:    idea.Hashtags,
		}
		for _, c := range idea.Cues {
			scriptOut.ClipCues = append(scriptOut.ClipCues, c.ClipCue)
		}
		saveScriptCompat(p.OutputDir, scriptOut, idea.Research)
		store.SaveProject(p)
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, p)
}
