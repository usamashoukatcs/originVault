package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/originvault/pipeline"
)

// handleSearch is the unified image/video search endpoint.
//
// Query params:
//
//	source   = pexels | pexels-photo | pixabay-video | pixabay-photo | unsplash | commons | duckduckgo
//	query    = search string
//	portrait = "true" for vertical/short clips
//	page     = 1-based page number (default 1) — pass 2, 3, etc. to get fresh results
func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	query := q.Get("query")
	portrait := q.Get("portrait") == "true"
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	if query == "" {
		jsonErr(w, 400, "query is required")
		return
	}
	if source == "" {
		source = "pexels"
	}

	orientation := "landscape"
	if portrait {
		orientation = "portrait"
	}

	var results []pipeline.SearchResult
	var err error

	switch source {
	case "pexels":
		results, err = pipeline.SearchPexelsMulti(footageCfg.PexelsAPIKey, query, orientation)
	case "pexels-photo":
		results, err = pipeline.SearchPexelsPhotosMulti(footageCfg.PexelsAPIKey, query)
	case "pixabay-video":
		results, err = pipeline.SearchPixabayVideoMulti(footageCfg.PixabayAPIKey, query, orientation)
	case "pixabay-photo":
		results, err = pipeline.SearchPixabayPhotoMulti(footageCfg.PixabayAPIKey, query)
	case "unsplash":
		results, err = pipeline.SearchUnsplashMulti(footageCfg.UnsplashAccessKey, query, page)
	case "commons":
		results, err = pipeline.SearchCommonsMulti(query)
	case "duckduckgo":
		results, err = pipeline.SearchDDGMulti(query)
	default:
		jsonErr(w, 400, fmt.Sprintf("unknown source: %s", source))
		return
	}

	if err != nil {
		jsonErr(w, 502, fmt.Sprintf("search failed: %v", err))
		return
	}
	if results == nil {
		results = []pipeline.SearchResult{}
	}
	jsonOK(w, map[string]any{"results": results})
}
