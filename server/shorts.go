package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/originvault/pipeline"
)

// handleAssembleShorts assembles every short video queued on the project
// (voiceover + vertical clips + burnt-in subtitles), one by one in the background.
func handleAssembleShorts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if len(p.Shorts) == 0 {
		jsonErr(w, 400, "no shorts queued for this project")
		return
	}
	if p.ShortsStatus == "assembling" {
		jsonErr(w, 409, "shorts assembly already in progress")
		return
	}

	proj := p
	proj.ShortsStatus = "assembling"
	store.SaveProject(proj)
	jsonOK(w, map[string]string{"status": "assembling"})

	go func() {
		logf := func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			fmt.Println("[shorts]", line)
			store.AppendLog(id, line)
		}

		paths := make([]string, len(p.Shorts))
		for i, short := range p.Shorts {
			logf("Assembling short %d/%d: %s", i+1, len(p.Shorts), short.Title)
			path, err := pipeline.GenerateShort(i, short, voiceCfg, footageCfg, p.OutputDir)
			if err != nil {
				logf("WARNING: short %d failed: %s", i+1, shortErr(err))
				continue
			}
			paths[i] = path
			logf("Short %d done: %s", i+1, path)
		}

		proj, err := store.GetProject(id)
		if err != nil {
			return
		}
		proj.ShortPaths = paths
		anyDone := false
		for _, pth := range paths {
			if pth != "" {
				anyDone = true
				break
			}
		}
		if anyDone {
			proj.ShortsStatus = "done"
		} else {
			proj.ShortsStatus = "error"
		}
		store.SaveProject(proj)
		logf("Shorts assembly finished.")
	}()
}

// handleDownloadShort streams one assembled short video to the browser.
func handleDownloadShort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idx := r.PathValue("idx")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}

	i := 0
	if _, err := fmt.Sscanf(idx, "%d", &i); err != nil || i < 0 || i >= len(p.ShortPaths) || p.ShortPaths[i] == "" {
		jsonErr(w, 400, "short not ready")
		return
	}

	path := p.ShortPaths[i]
	f, err := os.Open(path)
	if err != nil {
		jsonErr(w, 500, fmt.Sprintf("cannot open short: %v", err))
		return
	}
	defer f.Close()

	info, _ := f.Stat()
	title := "short"
	if i < len(p.Shorts) {
		title = sanitize(p.Shorts[i].Title)
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp4\"", title))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	http.ServeContent(w, r, title+".mp4", info.ModTime(), f)
}
