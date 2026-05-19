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
//	source   = google | bing | flickr | google-cse | duckduckgo | commons | pexels | pexels-photo | pixabay-video | pixabay-photo | unsplash
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
		source = "google"
	}

	orientation := "landscape"
	if portrait {
		orientation = "portrait"
	}

	var results []pipeline.SearchResult
	var err error

	switch source {
	// SerpAPI sources
	case "serp-google":
		results, err = pipeline.SearchSerpAPIGoogleImages(cfg.Search.SerpAPIKey, query, page)
	case "serp-bing":
		results, err = pipeline.SearchSerpAPIBingImages(cfg.Search.SerpAPIKey, query, page)
	case "serp-yahoo":
		results, err = pipeline.SearchSerpAPIYahooImages(cfg.Search.SerpAPIKey, query, page)
	case "serp-youtube":
		results, err = pipeline.SearchSerpAPIYouTube(cfg.Search.SerpAPIKey, query)
	// Other sources
	case "google":
		results, err = pipeline.SearchSerperImagesMulti(researchCfg.SerperAPIKey, query, page)
	case "bing":
		results, err = pipeline.SearchBingImagesMulti(cfg.Search.BingAPIKey, query, page)
	case "flickr":
		results, err = pipeline.SearchFlickrMulti(cfg.Search.FlickrAPIKey, query, page)
	case "google-cse":
		results, err = pipeline.SearchGoogleCSEMulti(cfg.Search.GoogleAPIKey, cfg.Search.GoogleCSEID, query, page)
	case "duckduckgo":
		results, err = pipeline.SearchDDGMulti(query)
	case "commons":
		results, err = pipeline.SearchCommonsMulti(query)
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
