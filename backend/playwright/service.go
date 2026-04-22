package playwright

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Script represents a saved Playwright script.
type Script struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// RunResult holds the result of a script execution.
type RunResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Duration int64  `json:"durationMs"`
	Error    string `json:"error,omitempty"`
}

// Service manages Playwright script execution.
type Service struct {
	mu       sync.Mutex
	app      *application.App
	scripts  map[string]*Script
	runDir   string // directory for temporary script files
	nodePath string // path to node executable
	pwReady  bool   // whether playwright is installed
}

// NewService creates a new Playwright service.
func NewService() *Service {
	s := &Service{
		scripts: make(map[string]*Script),
	}
	// Find node
	if p, err := exec.LookPath("node"); err == nil {
		s.nodePath = p
	}
	// Create temp dir for scripts
	home, _ := os.UserHomeDir()
	s.runDir = filepath.Join(home, ".tiancan-ide", "playwright")
	os.MkdirAll(s.runDir, 0o755)

	// Check if playwright is installed
	s.checkPlaywrightInstalled()
	return s
}

// SetApp sets the Wails app reference for event emission.
func (s *Service) SetApp(app *application.App) {
	s.app = app
}

// checkPlaywrightInstalled verifies that @playwright/test is available.
func (s *Service) checkPlaywrightInstalled() {
	if s.nodePath == "" {
		s.pwReady = false
		return
	}
	cmd := exec.Command(s.nodePath, "-e", "require('playwright')")
	cmd.Dir = s.runDir
	if err := cmd.Run(); err != nil {
		// Try npx playwright
		s.pwReady = false
	} else {
		s.pwReady = true
	}
}

// IsPlaywrightReady returns whether Playwright is available.
func (s *Service) IsPlaywrightReady() bool {
	return s.pwReady && s.nodePath != ""
}

// InstallPlaywright installs Playwright via npm.
func (s *Service) InstallPlaywright() ([]string, error) {
	var logs []string

	// Ensure package.json exists
	pkgPath := filepath.Join(s.runDir, "package.json")
	if _, err := os.Stat(pkgPath); os.IsNotExist(err) {
		pkgJSON := `{"name":"tiancan-playwright","version":"1.0.0","private":true,"dependencies":{}}`
		if err := os.WriteFile(pkgPath, []byte(pkgJSON), 0o644); err != nil {
			return logs, fmt.Errorf("create package.json: %w", err)
		}
		logs = append(logs, "Created package.json")
	}

	// Install playwright
	logs = append(logs, "Installing playwright...")
	cmd := exec.Command("npm", "install", "playwright")
	cmd.Dir = s.runDir
	out, err := cmd.CombinedOutput()
	logs = append(logs, strings.Split(string(out), "\n")...)
	if err != nil {
		return logs, fmt.Errorf("npm install playwright: %w", err)
	}

	// Install browsers
	logs = append(logs, "Installing browsers...")
	cmd = exec.Command("npx", "playwright", "install", "chromium")
	cmd.Dir = s.runDir
	out, err = cmd.CombinedOutput()
	logs = append(logs, strings.Split(string(out), "\n")...)
	if err != nil {
		return logs, fmt.Errorf("playwright install: %w", err)
	}

	s.pwReady = true
	logs = append(logs, "✓ Playwright installed successfully")
	return logs, nil
}

// SaveScript saves a script to the store.
func (s *Service) SaveScript(script Script) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[script.ID] = &script
	return nil
}

// GetScripts returns all saved scripts.
func (s *Service) GetScripts() []Script {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Script, 0, len(s.scripts))
	for _, sc := range s.scripts {
		out = append(out, *sc)
	}
	return out
}

// RunScript executes a Playwright script and streams output via events.
func (s *Service) RunScript(scriptID string) error {
	s.mu.Lock()
	script, ok := s.scripts[scriptID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("script %s not found", scriptID)
	}

	if s.nodePath == "" {
		return fmt.Errorf("Node.js not found — please install Node.js")
	}
	if !s.pwReady {
		return fmt.Errorf("Playwright not installed — click Install first")
	}

	// Write script to temp file
	tmpFile := filepath.Join(s.runDir, fmt.Sprintf("run_%s.js", scriptID))
	if err := os.WriteFile(tmpFile, []byte(script.Code), 0o644); err != nil {
		return fmt.Errorf("write script file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Execute with node
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.nodePath, tmpFile)
	cmd.Dir = s.runDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start node: %w", err)
	}

	// Stream stdout lines
	emitLine := func(lineType, text string) {
		if s.app != nil {
			eventData, _ := json.Marshal(map[string]string{
				"scriptId": scriptID,
				"type":     lineType,
				"line":     text,
			})
			s.app.Event.Emit("playwright:output", string(eventData))
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			emitLine("stdout", scanner.Text())
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			emitLine("stderr", scanner.Text())
		}
	}()

	wg.Wait()
	err = cmd.Wait()
	duration := time.Since(start).Milliseconds()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// Emit final result
	if s.app != nil {
		result := RunResult{
			ExitCode: exitCode,
			Duration: duration,
		}
		if err != nil && exitCode == -1 {
			result.Error = err.Error()
		}
		data, _ := json.Marshal(map[string]interface{}{
			"scriptId": scriptID,
			"type":     "done",
			"result":   result,
		})
		s.app.Event.Emit("playwright:output", string(data))
	}

	return nil
}

// StopScript kills the running Playwright process for a script.
func (s *Service) StopScript(scriptID string) error {
	// Find and kill any running node process for this script
	// We track PIDs in a simple map for now
	tmpFile := filepath.Join(s.runDir, fmt.Sprintf("run_%s.js", scriptID))
	// Use pkill to find the process
	cmd := exec.Command("pkill", "-f", tmpFile)
	cmd.Run() // ignore error, process might not exist
	return nil
}

// RunTest runs a Playwright test file and returns structured results.
func (s *Service) RunTest(testPath string) (*TestRunResult, error) {
	if s.nodePath == "" {
		return nil, fmt.Errorf("Node.js not found")
	}
	if !s.pwReady {
		return nil, fmt.Errorf("Playwright not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "npx", "playwright", "test", testPath, "--reporter=json")
	cmd.Dir = s.runDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	result := &TestRunResult{
		ExitCode: exitCode,
		Stderr:   stderr.String(),
	}

	// Parse JSON output
	raw := stdout.String()
	var pwReport PlaywrightJSONReport
	if err := json.Unmarshal([]byte(raw), &pwReport); err == nil {
		result.Suites = pwReport.Suites
		result.Stats = pwReport.Stats
	} else {
		// Fallback: treat as raw output
		result.RawOutput = raw
	}

	return result, nil
}

// TestRunResult holds structured test results.
type TestRunResult struct {
	ExitCode  int         `json:"exitCode"`
	Stderr    string      `json:"stderr"`
	RawOutput string      `json:"rawOutput,omitempty"`
	Suites    []TestSuite `json:"suites,omitempty"`
	Stats     *TestStats  `json:"stats,omitempty"`
}

// TestStats from Playwright JSON reporter.
type TestStats struct {
	Total    int   `json:"total"`
	Passed   int   `json:"passed"`
	Failed   int   `json:"failed"`
	Skipped  int   `json:"skipped"`
	Duration int64 `json:"duration"`
}

// TestSuite represents a test suite in Playwright output.
type TestSuite struct {
	Title string     `json:"title"`
	File  string     `json:"file"`
	Specs []TestSpec `json:"specs"`
}

// TestSpec represents a test spec within a suite.
type TestSpec struct {
	Title    string `json:"title"`
	Status   string `json:"status"` // passed, failed, skipped, timedOut
	Duration int64  `json:"duration"`
	Error    string `json:"error,omitempty"`
}

// PlaywrightJSONReport matches the Playwright JSON reporter output format.
type PlaywrightJSONReport struct {
	Suites []TestSuite `json:"suites"`
	Stats  *TestStats  `json:"stats"`
}
