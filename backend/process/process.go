package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Service manages running project processes.
type Service struct {
	mu        sync.Mutex
	procs     map[string]*managedProcess
	portCache map[string][]string // rootPath → ports seen in last run
}

type managedProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	outBuf []string
}

const maxOutLines = 300

func (p *managedProcess) appendLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outBuf = append(p.outBuf, line)
	if len(p.outBuf) > maxOutLines {
		p.outBuf = p.outBuf[len(p.outBuf)-maxOutLines:]
	}
}

func (s *Service) getProcs() map[string]*managedProcess {
	if s.procs == nil {
		s.procs = make(map[string]*managedProcess)
	}
	return s.procs
}

func (s *Service) addPort(rootPath, port string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.portCache == nil {
		s.portCache = make(map[string][]string)
	}
	for _, p := range s.portCache[rootPath] {
		if p == port {
			return
		}
	}
	s.portCache[rootPath] = append(s.portCache[rootPath], port)
}

func (s *Service) cachedPorts(rootPath string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.portCache == nil {
		return nil
	}
	return append([]string(nil), s.portCache[rootPath]...)
}

// RunConfig describes a detected or user-defined launch configuration.
type RunConfig struct {
	Label string `json:"label"`
	Cmd   string `json:"cmd"`
}

// RunEnvConfig holds per-project runtime environment settings.
type RunEnvConfig struct {
	PreCmd     string            `json:"preCmd"`     // shell command to run before the main cmd
	Env        map[string]string `json:"env"`        // extra environment variables
	BuildFlags string            `json:"buildFlags"` // extra flags passed to go build / dlv launch
}

var (
	runEnvMu      sync.Mutex
	runEnvConfigs = map[string]RunEnvConfig{}
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasGoMain returns true if path is a .go file with `package main` and a `func main`.
func hasGoMain(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hasMain, hasPkg := false, false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package main") {
			hasPkg = true
		}
		if strings.HasPrefix(line, "func main()") {
			hasMain = true
		}
		if hasMain && hasPkg {
			return true
		}
	}
	return false
}

// scanGoEntrypoints finds all directories containing package-main Go files,
// searching up to maxDepth levels below rootPath.
func scanGoEntrypoints(rootPath string, maxDepth int) []string {
	var dirs []string
	seen := map[string]bool{}
	filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(rootPath, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxDepth {
				return filepath.SkipDir
			}
			// skip hidden / vendor / node_modules
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if hasGoMain(path) {
			dir := filepath.Dir(path)
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
		return nil
	})
	return dirs
}

func detectCommand(rootPath string) (string, []string, error) {
	switch {
	case fileExists(filepath.Join(rootPath, "go.mod")):
		return "go", []string{"run", "."}, nil
	case fileExists(filepath.Join(rootPath, "package.json")):
		return "npm", []string{"run", "dev"}, nil
	case fileExists(filepath.Join(rootPath, "Cargo.toml")):
		return "cargo", []string{"run"}, nil
	case fileExists(filepath.Join(rootPath, "pom.xml")):
		return "mvn", []string{"spring-boot:run"}, nil
	case fileExists(filepath.Join(rootPath, "requirements.txt")):
		return "python3", []string{"-m", "flask", "run"}, nil
	default:
		// Scan subdirectories for Go main packages
		entries := scanGoEntrypoints(rootPath, 3)
		if len(entries) > 0 {
			rel, err := filepath.Rel(rootPath, entries[0])
			if err != nil || rel == "." {
				return "go", []string{"run", "."}, nil
			}
			return "go", []string{"run", "./" + filepath.ToSlash(rel)}, nil
		}
		return "", nil, fmt.Errorf("未识别的项目类型，请检查项目根目录")
	}
}

// SetRunEnvConfig stores the runtime env config for a project.
func (s *Service) SetRunEnvConfig(rootPath string, cfg RunEnvConfig) error {
	runEnvMu.Lock()
	defer runEnvMu.Unlock()
	runEnvConfigs[rootPath] = cfg
	return nil
}

// GetRunEnvConfig returns the stored runtime env config for a project.
func (s *Service) GetRunEnvConfig(rootPath string) RunEnvConfig {
	runEnvMu.Lock()
	defer runEnvMu.Unlock()
	return runEnvConfigs[rootPath]
}

// DetectRunEnv inspects the project and returns suggested runtime environment.
// If a vendor directory exists, it suggests a pre-run command to fix it.
func (s *Service) DetectRunEnv(rootPath string) RunEnvConfig {
	runEnvMu.Lock()
	existing := runEnvConfigs[rootPath]
	runEnvMu.Unlock()

	// Always auto-detect BuildFlags if not explicitly configured by the user.
	if existing.BuildFlags == "" && fileExists(filepath.Join(rootPath, "frontend", "dist")) && fileExists(filepath.Join(rootPath, "go.mod")) {
		existing.BuildFlags = "-tags production"
	}

	// Return user-defined config if already set
	if existing.PreCmd != "" || len(existing.Env) > 0 || existing.BuildFlags != "" {
		return existing
	}

	suggested := RunEnvConfig{Env: map[string]string{}}
	if fileExists(filepath.Join(rootPath, "vendor")) && fileExists(filepath.Join(rootPath, "go.mod")) {
		suggested.PreCmd = "go mod tidy && go mod vendor"
	}
	return suggested
}

// StartProject detects the project type and starts the default run command.
func (s *Service) StartProject(rootPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	procs := s.getProcs()

	// Kill existing process for this project
	if p, ok := procs[rootPath]; ok {
		select {
		case <-p.done:
		default:
			killPGroup(p.cmd)
		}
		<-p.done
	}
	delete(procs, rootPath)

	name, args, err := detectCommand(rootPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = rootPath
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	done := make(chan struct{})
	procs[rootPath] = &managedProcess{cmd: cmd, done: done}

	go func() {
		cmd.Wait() // nolint: errcheck
		close(done)
	}()

	return nil
}

// portConflictKeyRe detects that a line is about a port conflict.
var portConflictKeyRe = regexp.MustCompile(`address already in use|EADDRINUSE`)

// portNumberRe extracts the first port-like number (prefixed by ':') from a line.
var portNumberRe = regexp.MustCompile(`:([0-9]{2,5})(?:[^0-9]|$)`)

// extractConflictPort returns the port from a port-conflict output line, or "".
func extractConflictPort(line string) string {
	if !portConflictKeyRe.MatchString(line) {
		return ""
	}
	// Find the last :PORT in the line (Node puts it at end; Go puts it in middle).
	all := portNumberRe.FindAllStringSubmatch(line, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1]
}

// portBindRe detects successful port bindings, e.g. "0.0.0.0:8808", "localhost:8080".
var portBindRe = regexp.MustCompile(`(?:0\.0\.0\.0|127\.0\.0\.1|localhost|\[::\]):([0-9]{2,5})(?:[^0-9]|$)`)

// killPort kills any process listening on the given port number (string, e.g. "8808").
func killPort(port string) {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano").Output()
		if err != nil {
			return
		}
		pidRe := regexp.MustCompile(`(?:0\.0\.0\.0|127\.0\.0\.1|\[::\]):` + port + `\s+\S+\s+LISTENING\s+(\d+)`)
		if pm := pidRe.FindSubmatch(out); len(pm) >= 2 {
			exec.Command("taskkill", "/F", "/PID", string(pm[1])).Run() //nolint:errcheck
		}
	} else {
		exec.Command("sh", "-c", "lsof -ti :"+port+" 2>/dev/null | xargs kill -9 2>/dev/null; true").Run() //nolint:errcheck
	}
	time.Sleep(300 * time.Millisecond)
}

// tryKillPort extracts a port from a conflict line and calls killPort.
func tryKillPort(line string) {
	if port := extractConflictPort(line); port != "" {
		killPort(port)
	}
}

func killPGroup(cmd *exec.Cmd) {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		cmd.Process.Kill()
		return
	}
	syscall.Kill(-pgid, syscall.SIGKILL)
}

// StopProject kills the running process for the given project.
func (s *Service) StopProject(rootPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	procs := s.getProcs()
	p, ok := procs[rootPath]
	if !ok {
		return nil
	}

	select {
	case <-p.done:
	default:
		killPGroup(p.cmd)
		<-p.done
	}
	delete(procs, rootPath)
	return nil
}

// IsRunning returns whether the project's process is currently active.
func (s *Service) IsRunning(rootPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	procs := s.getProcs()
	p, ok := procs[rootPath]
	if !ok {
		return false
	}

	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// DetectRunCommand returns the detected start command string for display.
func (s *Service) DetectRunCommand(rootPath string) string {
	name, args, err := detectCommand(rootPath)
	if err != nil {
		return ""
	}
	result := name
	for _, a := range args {
		result += " " + a
	}
	return result
}

// ScanRunConfigs returns all detected launch configurations for the project.
func (s *Service) ScanRunConfigs(rootPath string) []RunConfig {
	var configs []RunConfig

	// Primary detection
	name, args, err := detectCommand(rootPath)
	if err == nil {
		cmd := name
		for _, a := range args {
			cmd += " " + a
		}
		configs = append(configs, RunConfig{Label: cmd, Cmd: cmd})
	}

	// For Go: scan ALL main packages in subdirs
	if fileExists(filepath.Join(rootPath, "go.mod")) || err != nil {
		entries := scanGoEntrypoints(rootPath, 3)
		for _, dir := range entries {
			rel, relErr := filepath.Rel(rootPath, dir)
			var cmd string
			if relErr != nil || rel == "." {
				cmd = "go run ."
			} else {
				cmd = "go run ./" + filepath.ToSlash(rel)
			}
			// deduplicate
			dupe := false
			for _, c := range configs {
				if c.Cmd == cmd {
					dupe = true
					break
				}
			}
			if !dupe {
				configs = append(configs, RunConfig{Label: cmd, Cmd: cmd})
			}
		}
	}

	return configs
}

// enrichedPath returns the PATH enriched with common tool directories.
func enrichedPath() string {
	current := os.Getenv("PATH")
	extra := []string{
		"/usr/local/go/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	seen := map[string]bool{}
	parts := strings.Split(current, ":")
	for _, p := range parts {
		seen[p] = true
	}
	for _, p := range extra {
		if !seen[p] {
			current = p + ":" + current
			seen[p] = true
		}
	}
	return current
}

// StartProjectWithCmd starts the project using a user-specified command string.
// It respects the stored RunEnvConfig: runs PreCmd first, then injects extra Env vars.
func (s *Service) StartProjectWithCmd(rootPath string, cmdStr string) error {
	// Collect env config outside the process lock to avoid deadlock
	runEnvMu.Lock()
	envCfg := runEnvConfigs[rootPath]
	runEnvMu.Unlock()

	baseEnv := append(os.Environ(), "PATH="+enrichedPath())
	for k, v := range envCfg.Env {
		baseEnv = append(baseEnv, k+"="+v)
	}

	// Run pre-command synchronously (capture output into a temp buffer)
	if preCmd := strings.TrimSpace(envCfg.PreCmd); preCmd != "" {
		preArgs := []string{"-c", preCmd}
		pre := exec.Command("sh", preArgs...)
		pre.Dir = rootPath
		pre.Env = baseEnv
		out, err := pre.CombinedOutput()
		if err != nil {
			return fmt.Errorf("前置命令失败: %w\n%s", err, string(out))
		}
	}

	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return fmt.Errorf("命令不能为空")
	}

	// launch starts the command and wires up stdout/stderr.
	// It must be called WITHOUT holding s.mu.
	var launch func() error
	launch = func() error {
		cmd := exec.Command(fields[0], fields[1:]...)
		cmd.Dir = rootPath
		cmd.Env = baseEnv
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		stdoutPipe, _ := cmd.StdoutPipe()
		stderrPipe, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动失败: %w", err)
		}

		done := make(chan struct{})
		mp := &managedProcess{cmd: cmd, done: done, outBuf: nil}

		s.mu.Lock()
		s.getProcs()[rootPath] = mp
		s.mu.Unlock()

		var retried int32 // atomic flag: 1 = retry already triggered
		pipeLines := func(r io.Reader) {
			scanner := bufio.NewScanner(r)
			scanner.Buffer(make([]byte, 1<<20), 1<<20)
			for scanner.Scan() {
				line := scanner.Text()
				mp.appendLine(line)
				// Cache any port this process successfully bound to
				if bm := portBindRe.FindStringSubmatch(line); len(bm) >= 2 {
					s.addPort(rootPath, bm[1])
				}
				// Auto-kill port occupant and retry once on conflict
				if port := extractConflictPort(line); port != "" && atomic.CompareAndSwapInt32(&retried, 0, 1) {
					go func(p string) {
						killPort(p)
						killPGroup(cmd)
						<-done
						launch() //nolint:errcheck
					}(port)
				}
			}
		}
		go pipeLines(stdoutPipe)
		go pipeLines(stderrPipe)
		go func() { cmd.Wait(); close(done) }() //nolint:errcheck
		return nil
	}

	// Kill any existing tracked process.
	s.mu.Lock()
	if p, ok := s.getProcs()[rootPath]; ok {
		select {
		case <-p.done:
		default:
			killPGroup(p.cmd)
			s.mu.Unlock()
			<-p.done
			s.mu.Lock()
		}
		delete(s.getProcs(), rootPath)
	}
	s.mu.Unlock()

	// Proactively kill any port this project was known to use.
	for _, port := range s.cachedPorts(rootPath) {
		killPort(port)
	}
	time.Sleep(200 * time.Millisecond) // let OS release ports

	return launch()
}

// GetProcessOutput returns the last N lines of stdout+stderr for the project.
func (s *Service) GetProcessOutput(rootPath string) []string {
	s.mu.Lock()
	p, ok := s.getProcs()[rootPath]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, len(p.outBuf))
	copy(result, p.outBuf)
	return result
}
