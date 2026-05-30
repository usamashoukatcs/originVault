# OriginVault

A fully automated YouTube video production pipeline for faceless channels. OriginVault handles everything from topic research to final video assembly — script, voiceover, footage selection, and thumbnail prompts — through a web dashboard.

**Channel niche**: Untold origin stories of inventions and companies that shaped the world.

---

## Features

### Research & Ideas
- **Trending topic suggestions** — pulls live YouTube autocomplete + Google Trends daily RSS to suggest topics people are searching right now
- **Idea manager** — generate, save, expand, and convert ideas into projects
- **SEO title & description generation** — Claude researches YouTube autocomplete for your topic and writes optimized titles and descriptions

### Script Generation
- **AI script writing** — Claude generates full narration scripts with embedded B-roll cue points
- **Cue system** — each cue carries keyword, image query, visual type, person/company/product tags, and a phrase to highlight in the script panel

### Voiceover
- **OpenAI TTS** — converts script to MP3 using the `onyx` voice (deep, authoritative)
- Supports `tts-1` (fast) and `tts-1-hd` (higher quality)

### Cue Picker (Footage Selection)
Multi-source image and video search with infinite scroll and grouped tabs:

| Group | Sources |
|-------|---------|
| **SerpAPI** | Google Images, Bing Images, Yahoo Images, YouTube |
| **Free** | DuckDuckGo, Wikimedia Commons, Google via Serper |
| **Stock** | Pexels (photo + video), Pixabay (photo + video), Unsplash, Flickr, Google CSE |
| **AI** | Generate custom images with Flux Dev, Imagen 3, or GPT-Image-1 |

- Per-source result cache — switching tabs preserves results
- Infinite scroll — auto-loads next page as you scroll
- Sequence filmstrip — assign up to 3 clips per cue
- Script panel with live phrase highlighting for current cue

### AI Image Generation
- **fal.ai** — Flux Dev model
- **Google Imagen 3**
- **OpenAI GPT-Image-1**

### Video Assembly
- FFmpeg-based assembly — stitches clips + voiceover into final MP4
- Overlay editor — add text/image overlays with position, size, and timing control
- Thumbnail generation

### Thumbnail Prompt Generator
- Generates Imagine Art-optimized prompts directly from the script title and summary
- Three theme modes: **Drama** (red), **Success** (gold), **Tech** (blue)
- Output includes: positive prompt, negative prompt, text overlay suggestion, text placement, Canva tips
- Designed for SD-based models (Ideogram v3, Flux 1.1 Ultra, Recraft v4)

---

## Stack

| Layer | Tech |
|-------|------|
| Backend | Go 1.24, stdlib `net/http` |
| Frontend | React + TypeScript + Vite + Tailwind CSS |
| Script AI | Anthropic Claude |
| Voiceover | OpenAI TTS |
| Image AI | fal.ai, Google Imagen, OpenAI GPT-Image-1 |
| Stock media | Pexels, Pixabay, Unsplash |
| Image search | SerpAPI, DuckDuckGo, Wikimedia, Google CSE, Serper |
| Video | FFmpeg |

---

## Setup

### 1. Clone and configure

```bash
cp config.example.yaml config.yaml
```

Fill in `config.yaml` with your API keys (see table below).

### 2. Build and run

```bash
# Build frontend
cd web && npm install && npm run build && cd ..

# Run server (serves API + React app on port 8080)
cd server && go run .
```

Open [http://localhost:8080](http://localhost:8080)

---

## API Keys

| Key | Where to get | Free tier |
|-----|-------------|-----------|
| `anthropic_api_key` | console.anthropic.com | Pay per use |
| `openai_api_key` | platform.openai.com | Pay per use |
| `pexels_api_key` | pexels.com/api | Free |
| `pixabay_api_key` | pixabay.com/api/docs | Free |
| `unsplash_access_key` | unsplash.com/developers | 50 req/hour free |
| `fal_api_key` | fal.ai | Pay per use |
| `imagen_api_key` | Google AI Studio | Pay per use |
| `news_api_key` | newsapi.org | 100 req/day free |
| `search.serp_api_key` | serpapi.com | 250 searches/month free |
| `search.google_api_key` | Google Cloud Console → Custom Search API | 100 queries/day free |
| `search.google_cse_id` | cse.google.com | Free |
| `research.serper_api_key` | serper.dev | 2,500 searches/month free |

---

## Project Structure

```
originvault/
├── main.go                  # CLI entry point
├── config.yaml              # API keys and settings
├── config.example.yaml      # Template (commit this, not config.yaml)
├── pipeline/
│   ├── research.go          # Topic research, trending context, SEO data
│   ├── script.go            # Claude script generation
│   ├── voiceover.go         # OpenAI TTS
│   ├── footage.go           # Stock footage fetch
│   ├── multisearch.go       # Multi-source image search
│   ├── imagegen.go          # AI image generation
│   ├── thumbprompt.go       # Thumbnail prompt generation
│   ├── assembly.go          # FFmpeg video assembly
│   ├── video.go             # Video utilities
│   ├── shorts.go            # Shorts pipeline
│   └── excel.go             # Excel logging
├── server/
│   ├── main.go              # HTTP server + routes
│   ├── projects.go          # Project pipeline handlers
│   ├── search.go            # Unified image search endpoint
│   ├── ideas.go             # Ideas handlers
│   ├── overlays.go          # Overlay handlers
│   ├── cues.go              # Cue clip selection handlers
│   └── store.go             # File-based project store
└── web/                     # React frontend (Vite)
    └── src/
        ├── pages/
        │   ├── CuePicker.tsx    # Footage selection UI
        │   ├── Assembly.tsx     # Assembly + thumbnail prompt UI
        │   └── ...
        ├── api.ts               # API client
        └── types.ts             # TypeScript types
```

---

## Workflow

1. **Ideas** → Generate trending topic ideas → pick one → create project
2. **Script** → Claude writes script with cue points + SEO title/description
3. **Cue Picker** → Search and assign footage/images to each cue
4. **Voice** → Generate voiceover from script
5. **Assembly** → Assemble final video with FFmpeg
6. **Thumbnail** → Generate Imagine Art prompt → create thumbnail manually
7. **Upload** → Upload to YouTube manually

---

## Pending / Roadmap

- [ ] Shorts auto-assembly pipeline
- [ ] YouTube upload API integration
- [ ] Blog post generation for repurposing scripts
