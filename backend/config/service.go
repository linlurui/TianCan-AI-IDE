package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Settings is the main IDE configuration structure.
// Stored at ~/.tiancan/settings.json
type Settings struct {
	// Editor
	FontSize         int    `json:"fontSize"`
	TabSize          int    `json:"tabSize"`
	WordWrap         bool   `json:"wordWrap"`
	Minimap          bool   `json:"minimap"`
	FontFamily       string `json:"fontFamily"`
	LineHeight       int    `json:"lineHeight"`
	RenderWhitespace string `json:"renderWhitespace"`
	// Theme
	Theme string `json:"theme"`
	// AI
	LmStudioURL      string `json:"lmStudioUrl"`
	DefaultModel     string `json:"defaultModel"`
	InlineCompletion bool   `json:"inlineCompletion"`
	// Update
	AutoCheckUpdate bool   `json:"autoCheckUpdate"`
	CheckedVersion  string `json:"checkedVersion"`
	// LSP
	LspBinDir string            `json:"lspBinDir"` // default ~/.tiancan/lsp-bin
	LspPaths  map[string]string `json:"lspPaths"`  // per-lang binary overrides, e.g. {"go": "/usr/local/bin/gopls"}
}

var defaults = Settings{
	FontSize:         14,
	TabSize:          2,
	WordWrap:         false,
	Minimap:          true,
	FontFamily:       "JetBrains Mono, Fira Code, monospace",
	LineHeight:       22,
	RenderWhitespace: "selection",
	Theme:            "tiancan-dark",
	LmStudioURL:      "http://127.0.0.1:1234/v1",
	DefaultModel:     "default",
	InlineCompletion: true,
	AutoCheckUpdate:  true,
	LspBinDir:        "", // empty = use ~/.tiancan/lsp-bin (resolved at runtime)
	LspPaths:         map[string]string{},
}

// UpdateInfo describes an available update.
type UpdateInfo struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	Notes     string `json:"notes"`
	Published string `json:"published"`
}

// Service exposes config management and update-check to the frontend via Wails.
type Service struct {
	mu       sync.RWMutex
	settings Settings
	path     string

	// change notification channel (frontend polls or listens)
	changeCh chan struct{}
	// watcher cancel
	watchCancel context.CancelFunc
}

func NewService() *Service {
	dir, _ := os.UserHomeDir()
	cfgDir := filepath.Join(dir, ".tiancan")
	_ = os.MkdirAll(cfgDir, 0o755)
	path := filepath.Join(cfgDir, "settings.json")

	svc := &Service{
		settings: defaults,
		path:     path,
		changeCh: make(chan struct{}, 4),
	}
	_ = svc.load()
	return svc
}

// ---- CRUD ----

// GetSettings returns current settings.
func (s *Service) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SaveSettings persists settings to disk.
func (s *Service) SaveSettings(settings Settings) error {
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return s.flush()
}

// ResetSettings restores defaults.
func (s *Service) ResetSettings() error {
	s.mu.Lock()
	s.settings = defaults
	s.mu.Unlock()
	return s.flush()
}

// GetSettingsPath returns the path to the settings file.
func (s *Service) GetSettingsPath() string { return s.path }

// GetLspBinDir returns the effective LSP binary directory.
// If the user hasn't configured one, defaults to ~/.tiancan/lsp-bin.
func (s *Service) GetLspBinDir() string {
	s.mu.RLock()
	dir := s.settings.LspBinDir
	s.mu.RUnlock()
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".tiancan", "lsp-bin")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.flush() // create with defaults
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Merge into defaults so new keys always have values
	merged := defaults
	if err := json.Unmarshal(data, &merged); err != nil {
		return err
	}
	s.settings = merged
	return nil
}

func (s *Service) flush() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.settings, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// ---- Hot-reload file watcher ----

// StartWatcher begins polling the settings file for changes.
// Call this once on startup. The frontend can call PollChange to detect updates.
func (s *Service) StartWatcher() {
	if s.watchCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.watchCancel = cancel

	go func() {
		var lastMod time.Time
		if info, err := os.Stat(s.path); err == nil {
			lastMod = info.ModTime()
		}
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(s.path)
				if err != nil {
					continue
				}
				if info.ModTime().After(lastMod) {
					lastMod = info.ModTime()
					if err := s.load(); err == nil {
						select {
						case s.changeCh <- struct{}{}:
						default:
						}
					}
				}
			}
		}
	}()
}

// StopWatcher stops the file watcher.
func (s *Service) StopWatcher() {
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
}

// HasSettingsChanged returns true if the settings file changed since the last call.
// Frontend should poll this periodically (e.g., every 2s) to detect hot-reloads.
func (s *Service) HasSettingsChanged() bool {
	select {
	case <-s.changeCh:
		return true
	default:
		return false
	}
}

// ---- Auto Update ----

const currentVersion = "0.1.0"
const githubReleaseAPI = "https://api.github.com/repos/rocky233/TianCan-AI-IDE/releases/latest"

// GetCurrentVersion returns the running IDE version.
func (s *Service) GetCurrentVersion() string { return currentVersion }

// CheckForUpdate fetches the latest GitHub release and returns info if a newer version exists.
// Returns nil (JSON null) if already up-to-date or check fails.
func (s *Service) CheckForUpdate() (*UpdateInfo, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", githubReleaseAPI, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tiancan-ai-ide/"+currentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var release struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	latestVer := release.TagName
	if latestVer == "" || latestVer == "v"+currentVersion || latestVer == currentVersion {
		return nil, nil // already up-to-date
	}

	return &UpdateInfo{
		Version:   latestVer,
		URL:       release.HTMLURL,
		Notes:     release.Body,
		Published: release.PublishedAt,
	}, nil
}
