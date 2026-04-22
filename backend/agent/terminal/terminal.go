package terminal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Session represents a persistent terminal session.
type Session struct {
	ID        string
	rootPath  string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	output    strings.Builder
	outputMu  sync.Mutex
	running   bool
	runningMu sync.Mutex
	done      chan struct{}
	cancel    context.CancelFunc
	env       []string
	shell     string
}

// Manager manages multiple terminal sessions.
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
	rootPath string
}

// NewManager creates a new terminal session manager.
func NewManager(rootPath string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		rootPath: rootPath,
	}
}

// NewSession creates and starts a new terminal session.
func (m *Manager) NewSession(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Kill existing session with same ID
	if old, ok := m.sessions[id]; ok {
		old.Kill()
	}

	s, err := createSession(id, m.rootPath)
	if err != nil {
		return nil, err
	}
	m.sessions[id] = s
	return s, nil
}

// GetSession retrieves a session by ID.
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// KillSession kills a session by ID.
func (m *Manager) KillSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	s.Kill()
	delete(m.sessions, id)
	return nil
}

// ListSessions returns all active session IDs.
func (m *Manager) ListSessions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	for id, s := range m.sessions {
		if s.IsRunning() {
			ids = append(ids, id)
		}
	}
	return ids
}

// CloseAll kills all sessions.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.Kill()
		delete(m.sessions, id)
	}
}

// --- Session methods ---

func createSession(id, rootPath string) (*Session, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, shell, "-i")
	cmd.Dir = rootPath
	cmd.SysProcAttr = setPgidAttr()

	// Set up environment — enable color output
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"FORCE_COLOR=1",
		"NO_COLOR=", // override any NO_COLOR inherited from env
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	s := &Session{
		ID:       id,
		rootPath: rootPath,
		cmd:      cmd,
		stdin:    stdinPipe,
		stdout:   stdoutPipe,
		stderr:   stderrPipe,
		done:     make(chan struct{}),
		cancel:   cancel,
		shell:    shell,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start shell: %w", err)
	}
	s.setRunning(true)

	// Read stdout
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0), 64*1024)
		for scanner.Scan() {
			line := scanner.Text()
			s.appendOutput(line + "\n")
		}
	}()

	// Read stderr
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0), 64*1024)
		for scanner.Scan() {
			line := scanner.Text()
			s.appendOutput("[stderr] " + line + "\n")
		}
	}()

	// Wait for process exit
	go func() {
		cmd.Wait()
		s.setRunning(false)
		close(s.done)
	}()

	return s, nil
}

// Run executes a command in the session and waits for output to stabilize.
// Returns the new output since the command was sent.
func (s *Session) Run(command string, timeout time.Duration) (string, error) {
	if !s.IsRunning() {
		return "", fmt.Errorf("session %s is not running", s.ID)
	}

	// Mark start position
	s.outputMu.Lock()
	startLen := s.output.Len()
	s.outputMu.Unlock()

	// Send command
	if _, err := s.stdin.Write([]byte(command + "\n")); err != nil {
		return "", fmt.Errorf("write command: %w", err)
	}

	// Wait for output to stabilize
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return s.waitForOutput(startLen, timeout)
}

// RunOneShot runs a command in a fresh subprocess (not the persistent shell).
// This is better for long-running commands or commands that might hang.
func (s *Session) RunOneShot(command string, timeout time.Duration, cwd string) (string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	dir := s.rootPath
	if cwd != "" {
		if !filepath.IsAbs(cwd) {
			dir = filepath.Join(s.rootPath, cwd)
		} else {
			dir = cwd
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.shell, "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"FORCE_COLOR=1",
		"NO_COLOR=",
	)

	out, err := cmd.CombinedOutput()
	output := string(out)

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v", timeout)
	}
	if err != nil {
		return output, fmt.Errorf("exit code %v: %s", err, output)
	}
	return output, nil
}

// GetOutput returns all accumulated output.
func (s *Session) GetOutput() string {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	return s.output.String()
}

// GetNewOutput returns output added since the given position.
func (s *Session) GetNewOutput(since int) string {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	out := s.output.String()
	if since >= len(out) {
		return ""
	}
	return out[since:]
}

// Kill terminates the session's shell process.
func (s *Session) Kill() {
	s.setRunning(false)
	s.cancel()
	s.stdin.Close()
	// Kill process group
	if s.cmd.Process != nil {
		killProcessGroup(s.cmd.Process.Pid)
	}
}

// IsRunning returns whether the session's shell is still running.
func (s *Session) IsRunning() bool {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	return s.running
}

// WaitForExit blocks until the session exits or context is cancelled.
func (s *Session) WaitForExit(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- internal ---

func (s *Session) setRunning(r bool) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	s.running = r
}

func (s *Session) appendOutput(text string) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	// Cap output at 1MB to prevent memory issues
	const maxOutput = 1 << 20
	if s.output.Len() > maxOutput {
		// Trim oldest output
		content := s.output.String()
		trimPoint := len(content) - maxOutput/2
		if idx := strings.IndexByte(content[trimPoint:], '\n'); idx >= 0 {
			trimPoint += idx + 1
		}
		s.output.Reset()
		s.output.WriteString(content[trimPoint:])
	}
	s.output.WriteString(text)
}

func (s *Session) waitForOutput(startLen int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	stableCount := 0
	lastLen := startLen

	for {
		if time.Now().After(deadline) {
			s.outputMu.Lock()
			out := s.output.String()
			s.outputMu.Unlock()
			if startLen < len(out) {
				return out[startLen:], fmt.Errorf("timeout waiting for output stabilization")
			}
			return "", fmt.Errorf("timeout waiting for output")
		}

		s.outputMu.Lock()
		currentLen := s.output.Len()
		s.outputMu.Unlock()

		if currentLen > lastLen {
			stableCount = 0
			lastLen = currentLen
		} else {
			stableCount++
		}

		// Output is stable after 300ms of no new data
		if stableCount > 6 && currentLen > startLen {
			s.outputMu.Lock()
			out := s.output.String()
			s.outputMu.Unlock()
			if startLen < len(out) {
				return out[startLen:], nil
			}
			return "", nil
		}

		time.Sleep(50 * time.Millisecond)
	}
}
