package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/originvault/pipeline"
	"gopkg.in/yaml.v3"
)

// Config mirrors the CLI config struct.
type Config struct {
	AnthropicAPIKey   string `yaml:"anthropic_api_key"`
	PexelsAPIKey      string `yaml:"pexels_api_key"`
	PixabayAPIKey     string `yaml:"pixabay_api_key"`
	UnsplashAccessKey string `yaml:"unsplash_access_key"`
	FalAPIKey         string `yaml:"fal_api_key"`
	OpenAIAPIKey      string `yaml:"openai_api_key"`
	ImagenAPIKey      string `yaml:"imagen_api_key"`
	Voice             struct {
		Voice string `yaml:"voice"`
		Model string `yaml:"model"`
	} `yaml:"voice"`
	Channel struct {
		Niche          string `yaml:"niche"`
		VideoLengthMin int    `yaml:"video_length_min"`
		VideoLengthMax int    `yaml:"video_length_max"`
	} `yaml:"channel"`
	Research struct {
		SerperAPIKey string `yaml:"serper_api_key"`
		NewsAPIKey   string `yaml:"news_api_key"`
	} `yaml:"research"`
	OutputDir string `yaml:"output_dir"`
	Server    struct {
		Port int `yaml:"port"`
	} `yaml:"server"`
}

var (
	cfg         *Config
	store       *Store
	voiceCfg    pipeline.VoiceConfig
	footageCfg  pipeline.FootageConfig
	researchCfg pipeline.ResearchConfig
)

func main() {
	var err error
	// look for config.yaml in cwd, then parent (so `./server` binary works from project root)
	cfgPath := "config.yaml"
	if _, e := os.Stat(cfgPath); os.IsNotExist(e) {
		cfgPath = filepath.Join("..", "config.yaml")
	}
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		fmt.Printf("ERROR loading config: %v\n", err)
		os.Exit(1)
	}

	voiceCfg = pipeline.VoiceConfig{
		APIKey: cfg.OpenAIAPIKey,
		Voice:  cfg.Voice.Voice,
		Model:  cfg.Voice.Model,
	}
	footageCfg = pipeline.FootageConfig{
		PexelsAPIKey:      cfg.PexelsAPIKey,
		PixabayAPIKey:     cfg.PixabayAPIKey,
		UnsplashAccessKey: cfg.UnsplashAccessKey,
		FalAPIKey:         cfg.FalAPIKey,
		ImagenAPIKey:      cfg.ImagenAPIKey,
		DALLEAPIKey:       cfg.OpenAIAPIKey,
	}
	researchCfg = pipeline.ResearchConfig{
		SerperAPIKey: cfg.Research.SerperAPIKey,
		NewsAPIKey:   cfg.Research.NewsAPIKey,
	}

	outDir := cfg.OutputDir
	if outDir == "" {
		outDir = "output"
	}
	store = NewStore(outDir)

	port := 8080
	if cfg.Server.Port > 0 {
		port = cfg.Server.Port
	}

	mux := http.NewServeMux()

	// ── Projects ──────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/projects", handleListProjects)
	mux.HandleFunc("POST /api/projects", handleCreateProject)
	mux.HandleFunc("GET /api/projects/{id}", handleGetProject)
	mux.HandleFunc("DELETE /api/projects/{id}", handleDeleteProject)

	// ── Async pipeline steps ──────────────────────────────────────────────────
	mux.HandleFunc("GET /api/projects/{id}/status", handleProjectStatus)
	mux.HandleFunc("POST /api/projects/{id}/generate", handleGenerateScript)
	mux.HandleFunc("POST /api/projects/{id}/voice", handleGenerateVoice)
	mux.HandleFunc("POST /api/projects/{id}/assemble", handleAssemble)
	mux.HandleFunc("GET /api/projects/{id}/download", handleDownload)
	mux.HandleFunc("POST /api/projects/{id}/thumbnail", handleGenerateThumbnail)

	// ── Cue clip selection ────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/projects/{id}/cues/{idx}/select", handleSelectClip)
	mux.HandleFunc("DELETE /api/projects/{id}/cues/{idx}/select", handleClearClip)
	mux.HandleFunc("DELETE /api/projects/{id}/cues/{idx}/clips/{clipIdx}", handleRemoveClipFromSequence)
	mux.HandleFunc("POST /api/projects/{id}/cues/{idx}/ai-generate", handleGenerateAIImage)

	// ── Overlays ──────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/projects/{id}/overlays", handleListOverlays)
	mux.HandleFunc("POST /api/projects/{id}/overlays", handleCreateOverlay)
	mux.HandleFunc("PUT /api/projects/{id}/overlays/{oid}", handleUpdateOverlay)
	mux.HandleFunc("DELETE /api/projects/{id}/overlays/{oid}", handleDeleteOverlay)

	// ── Ideas ─────────────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/ideas", handleListIdeas)
	mux.HandleFunc("POST /api/ideas/generate", handleGenerateIdeas)
	mux.HandleFunc("GET /api/ideas/export", handleExportIdeas)
	mux.HandleFunc("POST /api/ideas/{iid}/expand", handleExpandIdea)
	mux.HandleFunc("DELETE /api/ideas/{iid}", handleDeleteIdea)
	mux.HandleFunc("POST /api/ideas/{iid}/project", handleCreateProjectFromIdea)

	// ── Image search ──────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/search", handleSearch)

	// ── Serve generated output files (AI images, thumbnails) ─────────────────
	mux.Handle("/output/", http.StripPrefix("/output/", http.FileServer(http.Dir(store.RootDir()))))

	// ── Serve React static files ──────────────────────────────────────────────
	webDist := "web/dist"
	if _, err := os.Stat(webDist); err == nil {
		mux.Handle("/assets/", http.FileServer(http.Dir(webDist)))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				http.ServeFile(w, r, filepath.Join(webDist, "index.html"))
			}
		})
	}

	handler := corsMiddleware(mux)
	fmt.Printf("OriginVault dashboard → http://localhost:%d\n", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

// corsMiddleware allows the Vite dev server (port 5173) to call the API (port 8080).
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("cannot parse config: %w", err)
	}
	return &c, nil
}
