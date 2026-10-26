package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/originvault/pipeline"
)

// handleGenerateBlogPost adapts the project's script into a WordPress-ready blog post.
func handleGenerateBlogPost(w http.ResponseWriter, r *http.Request) {
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

	result, err := pipeline.GenerateBlogPost(cfg.AnthropicAPIKey, cfg.Channel.Niche, p.Title, p.Summary, p.Script)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to generate blog post: %v", err))
		return
	}

	proj, _ := store.GetProject(id)
	proj.BlogPost = result
	if err := store.SaveProject(proj); err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to save project: %v", err))
		return
	}
	jsonOK(w, result)
}

// handlePublishBlogPost publishes the project's generated blog post to WordPress.
func handlePublishBlogPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.BlogPost == nil {
		jsonErr(w, 400, "generate a blog post first")
		return
	}

	var body struct {
		Status string `json:"status"` // "draft" | "publish"
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Status == "" {
		body.Status = "draft"
	}

	link, postID, err := pipeline.PublishWordPressPost(wpCfg, *p.BlogPost, body.Status)
	if err != nil {
		jsonErr(w, 502, fmt.Sprintf("failed to publish: %v", err))
		return
	}

	proj, _ := store.GetProject(id)
	proj.BlogPost.PublishedURL = link
	proj.BlogPost.PublishedID = postID
	if err := store.SaveProject(proj); err != nil {
		jsonErr(w, 500, fmt.Sprintf("failed to save project: %v", err))
		return
	}
	jsonOK(w, map[string]any{"url": link, "post_id": postID, "status": body.Status})
}
