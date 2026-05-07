package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func handleListOverlays(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	if p.Overlays == nil {
		p.Overlays = []Overlay{}
	}
	jsonOK(w, p.Overlays)
}

func handleCreateOverlay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}

	var ov Overlay
	if err := json.NewDecoder(r.Body).Decode(&ov); err != nil {
		jsonErr(w, 400, "invalid overlay body")
		return
	}
	if ov.Text == "" {
		jsonErr(w, 400, "text is required")
		return
	}

	// defaults
	if ov.Color == "" {
		ov.Color = "white"
	}
	if ov.FontSize == 0 {
		ov.FontSize = 40
	}
	if ov.EndTime <= ov.StartTime {
		ov.EndTime = ov.StartTime + 5
	}
	if ov.BgOpacity == 0 {
		ov.BgOpacity = 0.6
	}
	ov.ID = fmt.Sprintf("ov-%d", time.Now().UnixMilli())

	p.Overlays = append(p.Overlays, ov)
	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, ov)
}

func handleUpdateOverlay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	oid := r.PathValue("oid")

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}

	var updated Overlay
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		jsonErr(w, 400, "invalid body")
		return
	}

	found := false
	for i, ov := range p.Overlays {
		if ov.ID == oid {
			updated.ID = oid
			p.Overlays[i] = updated
			found = true
			break
		}
	}
	if !found {
		jsonErr(w, 404, fmt.Sprintf("overlay %s not found", oid))
		return
	}

	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, updated)
}

func handleDeleteOverlay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	oid := r.PathValue("oid")

	p, err := store.GetProject(id)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}

	var filtered []Overlay
	for _, ov := range p.Overlays {
		if ov.ID != oid {
			filtered = append(filtered, ov)
		}
	}
	p.Overlays = filtered

	if err := store.SaveProject(p); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}
