package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/originvault/pipeline"
)

// Project is the full project record stored in {outputDir}/project.json.
type Project struct {
	ID          string         `json:"id"`
	Topic       string         `json:"topic"`
	Title       string         `json:"title"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Script      string         `json:"script"`
	Keywords    []string       `json:"keywords"`
	Hashtags    []string       `json:"hashtags"`
	Status      string         `json:"status"` // pending | generating | script_done | voicing | voice_done | assembling | done | error
	StatusLog   []string       `json:"status_log"`
	OutputDir   string         `json:"output_dir"`
	VideoPath     string         `json:"video_path"`
	ThumbnailPath string         `json:"thumbnail_path,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Cues        []DashboardCue `json:"cues"`
	Overlays    []Overlay      `json:"overlays"`
	Research    []pipeline.ResearchResult `json:"research,omitempty"`
}

// SelectedClipInfo holds one clip in a cue's sequence.
type SelectedClipInfo struct {
	ClipPath string `json:"clip_path"`
	Source   string `json:"source"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url"`
}

// DashboardCue extends ClipCue with the user's selected clip sequence (up to 3 clips).
// Legacy single-clip fields mirror SelectedClips[0] for backward compat.
type DashboardCue struct {
	pipeline.ClipCue
	SelectedClips    []SelectedClipInfo `json:"selected_clips,omitempty"`
	SelectedClipPath string             `json:"selected_clip_path"` // mirrors SelectedClips[0].ClipPath
	SelectedSource   string             `json:"selected_source"`
	SelectedURL      string             `json:"selected_url"`
	SelectedThumbURL string             `json:"selected_thumb_url"`
}

// Overlay is a text overlay burned into the final video.
type Overlay struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	X         float64 `json:"x"`          // 0–100 percentage of video width
	Y         float64 `json:"y"`          // 0–100 percentage of video height
	StartTime float64 `json:"start_time"` // seconds
	EndTime   float64 `json:"end_time"`   // seconds
	FontSize  int     `json:"fontsize"`
	Color     string  `json:"color"`
	Bold      bool    `json:"bold"`
	BgOpacity float64 `json:"bg_opacity"` // 0 = no box, 1 = opaque box
	Centered  bool    `json:"centered"`   // if true, horizontally center the text (x is ignored)
}

// ProjectSummary is the lightweight entry in the projects index.
type ProjectSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Topic     string    `json:"topic"`
	Status    string    `json:"status"`
	OutputDir string    `json:"output_dir"`
	CreatedAt time.Time `json:"created_at"`
}

// Store manages reading and writing project files.
type Store struct {
	mu      sync.RWMutex
	rootDir string // e.g. "/home/.../output"
}

func NewStore(rootDir string) *Store {
	os.MkdirAll(rootDir, 0755)
	s := &Store{rootDir: rootDir}
	s.resetStuckIdeas()
	return s
}

// resetStuckIdeas resets any ideas left in "expanding" status back to "idea"
// so they can be re-expanded after a server restart killed the goroutine.
func (s *Store) resetStuckIdeas() {
	ideas, err := s.readIdeas()
	if err != nil {
		return
	}
	changed := false
	for i, idea := range ideas {
		if idea.Status == "expanding" {
			ideas[i].Status = "idea"
			changed = true
		}
	}
	if changed {
		s.saveIdeas(ideas)
	}
}

func (s *Store) RootDir() string { return s.rootDir }

func (s *Store) indexPath() string {
	return filepath.Join(s.rootDir, "projects-index.json")
}

func (s *Store) projectPath(outputDir string) string {
	return filepath.Join(outputDir, "project.json")
}

// ListProjects returns summaries of all known projects, newest first.
func (s *Store) ListProjects() ([]ProjectSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readIndex()
}

func (s *Store) readIndex() ([]ProjectSummary, error) {
	data, err := os.ReadFile(s.indexPath())
	if os.IsNotExist(err) {
		return []ProjectSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	var index []ProjectSummary
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return index, nil
}

func (s *Store) writeIndex(index []ProjectSummary) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath(), data, 0644)
}

// CreateProject creates a new project directory and saves the initial record.
func (s *Store) CreateProject(topic string) (*Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	id := now.Format("20060102-150405")

	// pick a unique output directory under rootDir/date/
	dateDir := filepath.Join(s.rootDir, now.Format("2006-01-02"))
	outputDir := s.nextVideoDir(dateDir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	p := &Project{
		ID:        id,
		Topic:     topic,
		Status:    "pending",
		OutputDir: outputDir,
		CreatedAt: now,
		UpdatedAt: now,
		Cues:      []DashboardCue{},
		Overlays:  []Overlay{},
		Keywords:  []string{},
		Hashtags:  []string{},
		StatusLog: []string{},
	}

	if err := s.saveProject(p); err != nil {
		return nil, err
	}

	// update index
	index, _ := s.readIndex()
	index = append([]ProjectSummary{{
		ID:        p.ID,
		Title:     p.Title,
		Topic:     p.Topic,
		Status:    p.Status,
		OutputDir: p.OutputDir,
		CreatedAt: p.CreatedAt,
	}}, index...)
	s.writeIndex(index)

	return p, nil
}

func (s *Store) nextVideoDir(dateDir string) string {
	for i := 1; ; i++ {
		dir := filepath.Join(dateDir, fmt.Sprintf("video_%03d", i))
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir
		}
	}
}

// GetProject loads the full project record by ID.
func (s *Store) GetProject(id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	for _, entry := range index {
		if entry.ID == id {
			return s.loadProject(entry.OutputDir)
		}
	}
	return nil, fmt.Errorf("project %s not found", id)
}

func (s *Store) loadProject(outputDir string) (*Project, error) {
	data, err := os.ReadFile(s.projectPath(outputDir))
	if err != nil {
		return nil, fmt.Errorf("project.json not found in %s: %w", outputDir, err)
	}
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("failed to parse project.json: %w", err)
	}
	return &p, nil
}

// SaveProject writes the full project record to disk and updates the index summary.
func (s *Store) SaveProject(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveProjectAndIndex(p)
}

func (s *Store) saveProjectAndIndex(p *Project) error {
	p.UpdatedAt = time.Now()
	if err := s.saveProject(p); err != nil {
		return err
	}
	// update summary in index
	index, _ := s.readIndex()
	updated := false
	for i, entry := range index {
		if entry.ID == p.ID {
			index[i] = ProjectSummary{
				ID:        p.ID,
				Title:     p.Title,
				Topic:     p.Topic,
				Status:    p.Status,
				OutputDir: p.OutputDir,
				CreatedAt: p.CreatedAt,
			}
			updated = true
			break
		}
	}
	if !updated {
		index = append([]ProjectSummary{{
			ID:        p.ID,
			Title:     p.Title,
			Topic:     p.Topic,
			Status:    p.Status,
			OutputDir: p.OutputDir,
			CreatedAt: p.CreatedAt,
		}}, index...)
	}
	// keep newest first
	sort.Slice(index, func(i, j int) bool {
		return index[i].CreatedAt.After(index[j].CreatedAt)
	})
	return s.writeIndex(index)
}

func (s *Store) saveProject(p *Project) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.projectPath(p.OutputDir), data, 0644)
}

// DeleteProject removes the project from the index and deletes its output directory from disk.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.readIndex()
	if err != nil {
		return err
	}
	var filtered []ProjectSummary
	var outputDir string
	for _, entry := range index {
		if entry.ID == id {
			outputDir = entry.OutputDir
		} else {
			filtered = append(filtered, entry)
		}
	}
	if err := s.writeIndex(filtered); err != nil {
		return err
	}
	if outputDir != "" {
		os.RemoveAll(outputDir)
	}
	return nil
}

// ─── Ideas ────────────────────────────────────────────────────────────────────

// Idea holds an auto-generated topic idea with optional expanded script/metadata.
type Idea struct {
	ID          string                    `json:"id"`
	Subject     string                    `json:"subject"`
	Niche       string                    `json:"niche"`
	Title       string                    `json:"title"`
	Summary     string                    `json:"summary"`
	Description string                    `json:"description"`
	Script      string                    `json:"script"`
	Keywords    []string                  `json:"keywords"`
	Hashtags    []string                  `json:"hashtags"`
	Cues        []DashboardCue            `json:"cues,omitempty"`
	Research    []pipeline.ResearchResult `json:"research,omitempty"`
	Status      string                    `json:"status"` // idea | expanding | ready | error
	ErrorMsg    string                    `json:"error_msg,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

func (s *Store) ideasPath() string {
	return filepath.Join(s.rootDir, "ideas.json")
}

func (s *Store) readIdeas() ([]Idea, error) {
	data, err := os.ReadFile(s.ideasPath())
	if os.IsNotExist(err) {
		return []Idea{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ideas []Idea
	if err := json.Unmarshal(data, &ideas); err != nil {
		return nil, err
	}
	return ideas, nil
}

func (s *Store) saveIdeas(ideas []Idea) error {
	data, err := json.MarshalIndent(ideas, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.ideasPath(), data, 0644)
}

func (s *Store) ListIdeas() ([]Idea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readIdeas()
}

func (s *Store) AddIdeas(newIdeas []Idea) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ideas, _ := s.readIdeas()
	ideas = append(newIdeas, ideas...)
	return s.saveIdeas(ideas)
}

func (s *Store) GetIdea(id string) (*Idea, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ideas, err := s.readIdeas()
	if err != nil {
		return nil, err
	}
	for _, idea := range ideas {
		if idea.ID == id {
			cp := idea
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("idea %s not found", id)
}

func (s *Store) UpdateIdea(updated Idea) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ideas, err := s.readIdeas()
	if err != nil {
		return err
	}
	for i, idea := range ideas {
		if idea.ID == updated.ID {
			updated.UpdatedAt = time.Now()
			ideas[i] = updated
			return s.saveIdeas(ideas)
		}
	}
	return fmt.Errorf("idea %s not found", updated.ID)
}

func (s *Store) DeleteIdea(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ideas, err := s.readIdeas()
	if err != nil {
		return err
	}
	filtered := make([]Idea, 0, len(ideas))
	for _, idea := range ideas {
		if idea.ID != id {
			filtered = append(filtered, idea)
		}
	}
	return s.saveIdeas(filtered)
}

func (s *Store) IdeaSubjects() []string {
	ideas, _ := s.ListIdeas()
	subjects := make([]string, 0, len(ideas))
	for _, idea := range ideas {
		subjects = append(subjects, idea.Subject)
	}
	return subjects
}

// AppendLog appends a log line to the project's status log and saves.
// Safe to call from background goroutines — uses SaveProject's lock.
func (s *Store) AppendLog(id, line string) {
	p, err := s.GetProject(id)
	if err != nil {
		return
	}
	p.StatusLog = append(p.StatusLog, line)
	// keep last 200 lines
	if len(p.StatusLog) > 200 {
		p.StatusLog = p.StatusLog[len(p.StatusLog)-200:]
	}
	s.SaveProject(p)
}

// SetStatus updates the project status and logs a message.
func (s *Store) SetStatus(id, status, logLine string) {
	p, err := s.GetProject(id)
	if err != nil {
		return
	}
	p.Status = status
	if logLine != "" {
		p.StatusLog = append(p.StatusLog, logLine)
	}
	s.SaveProject(p)
}
