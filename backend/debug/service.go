package debug

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const dlvVersion = "1.24.2"

// debugTmpBase is the managed directory for debug binaries.
// Keeping them here avoids polluting project roots with __debug_bin* files.
func debugTmpBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "debug-tmp")
}

// debugOutputPath returns the path for the compiled debug binary,
// prefixed with the project directory name for easy identification.
// e.g. ~/.tiancan-ide/debug-tmp/myproject__debug_bin
func debugOutputPath(rootPath string) string {
	base := filepath.Base(rootPath)
	return filepath.Join(debugTmpBase(), base+"__debug_bin")
}

// startCleanupTicker launches a background goroutine that periodically
// removes debug binaries older than 1 hour from the managed temp dir.
func (s *Service) startCleanupTicker() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanOldDebugFiles()
		}
	}()
}

// cleanOldDebugFiles removes files in debug-tmp/ older than 1 hour,
// but skips files belonging to currently active debug sessions.
func (s *Service) cleanOldDebugFiles() {
	dir := debugTmpBase()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Collect active output paths so we don't delete in-use binaries.
	s.mu.Lock()
	activePaths := make(map[string]bool, len(s.sessions))
	for root := range s.sessions {
		activePaths[debugOutputPath(root)] = true
	}
	s.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if activePaths[path] {
			continue // in use by active session
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				log.Printf("debug: cleaned old temp file %s", path)
			}
		}
	}
}

// Service manages debug sessions via DAP adapters + WebSocket proxy.
// Supported adapters: dlv (Go), lldb-dap (C/C++/Rust).
type Service struct {
	mu       sync.Mutex
	sessions map[string]*debugSession
	wsPort   int
	running  bool
}

// AdapterType identifies which DAP debug adapter to use.
type AdapterType string

const (
	AdapterDlv       AdapterType = "dlv"        // Go via Delve
	AdapterLldbDap   AdapterType = "lldb-dap"   // C/C++/Obj-C/Rust/Swift via LLDB DAP
	AdapterDebugpy   AdapterType = "debugpy"    // Python via debugpy
	AdapterJsDebug   AdapterType = "js-debug"   // Node.js/JavaScript via js-debug-dap
	AdapterJavaDebug AdapterType = "java-debug" // Java via java-debug
	AdapterDart      AdapterType = "dart"       // Dart/Flutter via SDK DAP
)

type debugSession struct {
	adapter  AdapterType // which DAP adapter is running
	cmd      *exec.Cmd   // the adapter process
	port     int         // adapter's DAP TCP port
	done     chan struct{}
	stderrCh chan string // adapter stderr lines, forwarded to WebSocket as DAP output events
	pgid     int         // process group ID; used to kill orphaned children after adapter exits
}

func NewService() *Service {
	s := &Service{sessions: make(map[string]*debugSession)}
	// Ensure temp dir exists and start periodic cleanup.
	os.MkdirAll(debugTmpBase(), 0o755) //nolint:errcheck
	s.startCleanupTicker()
	return s
}

func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func debugPath() string {
	cur := os.Getenv("PATH")
	extras := []string{
		filepath.Join(os.Getenv("HOME"), "go", "bin"),
		"/usr/local/go/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(cur, ":") {
		seen[p] = true
	}
	for _, p := range extras {
		if !seen[p] {
			cur = p + ":" + cur
			seen[p] = true
		}
	}
	return cur
}

// managedDlvPath returns the path where we cache our own dlv copy.
func managedDlvPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "bin", "dlv")
}

// ensureDlv returns an absolute path to a working dlv binary.
// Priority: managed cache → ~/go/bin/dlv → PATH → auto-download.
func ensureDlv() (string, error) {
	// 1. our own cached copy
	managed := managedDlvPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return managed, nil
	}
	// 2. ~/go/bin/dlv
	gobin := filepath.Join(os.Getenv("HOME"), "go", "bin", "dlv")
	if fi, err := os.Stat(gobin); err == nil && !fi.IsDir() {
		return gobin, nil
	}
	// 3. PATH
	if p, err := exec.LookPath("dlv"); err == nil {
		return p, nil
	}
	// 4. auto-download prebuilt binary
	return downloadDlv()
}

// dlvMirrors returns candidate download URLs for the dlv zip, fastest-first.
// Multiple mirrors are tried in parallel; the first successful response wins.
func dlvMirrors(zipName string) []string {
	ghPath := fmt.Sprintf("https://github.com/go-delve/delve/releases/download/v%s/%s", dlvVersion, zipName)
	return []string{
		"https://ghproxy.com/" + ghPath,
		"https://gh-proxy.com/" + ghPath,
		"https://mirror.ghproxy.com/" + ghPath,
		ghPath, // direct GitHub as last resort
	}
}

type dlvDownloadResult struct {
	data []byte
	url  string
}

// fetchFastest tries all URLs in parallel and returns the body of the first
// successful 200 response.
func fetchFastest(urls []string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ch := make(chan dlvDownloadResult, len(urls))
	for _, u := range urls {
		go func(url string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					resp.Body.Close()
				}
				return
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				ch <- dlvDownloadResult{data: data, url: url}
			}
		}(u)
	}

	select {
	case r := <-ch:
		cancel()
		return r.data, r.url, nil
	case <-ctx.Done():
		return nil, "", fmt.Errorf("所有下载源均超时或失败")
	}
}

// downloadDlv fetches the prebuilt dlv binary from the fastest mirror and
// caches it at managedDlvPath().
func downloadDlv() (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	zipName := fmt.Sprintf("dlv_%s_%s_%s.zip", dlvVersion, goos, goarch)

	log.Printf("debug: downloading dlv v%s (%s/%s) via fastest mirror...", dlvVersion, goos, goarch)
	data, src, err := fetchFastest(dlvMirrors(zipName))
	if err != nil {
		return "", fmt.Errorf("下载 dlv 失败: %w", err)
	}
	log.Printf("debug: downloaded dlv from %s (%d bytes)", src, len(data))

	// Unzip – find the "dlv" or "dlv.exe" entry
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("解压 dlv zip 失败: %w", err)
	}
	target := "dlv"
	if goos == "windows" {
		target = "dlv.exe"
	}
	var dlvData []byte
	for _, f := range zr.File {
		if filepath.Base(f.Name) == target {
			rc, err2 := f.Open()
			if err2 != nil {
				return "", fmt.Errorf("打开 dlv zip 条目失败: %w", err2)
			}
			dlvData, err2 = io.ReadAll(rc)
			rc.Close()
			if err2 != nil {
				return "", fmt.Errorf("读取 dlv zip 条目失败: %w", err2)
			}
			break
		}
	}
	if dlvData == nil {
		return "", fmt.Errorf("zip 中未找到 dlv 可执行文件")
	}

	dest := managedDlvPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(dest, dlvData, 0o755); err != nil {
		return "", fmt.Errorf("写入 dlv 失败: %w", err)
	}
	log.Printf("debug: dlv cached at %s", dest)
	return dest, nil
}

// IsDlvInstalled reports whether dlv is available (managed cache, ~/go/bin, or PATH).
func (s *Service) IsDlvInstalled() bool {
	managed := managedDlvPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return true
	}
	gobin := filepath.Join(os.Getenv("HOME"), "go", "bin", "dlv")
	if fi, err := os.Stat(gobin); err == nil && !fi.IsDir() {
		return true
	}
	_, err := exec.LookPath("dlv")
	return err == nil
}

// InstallDlv ensures dlv is available, downloading it if necessary.
func (s *Service) InstallDlv() (string, error) {
	path, err := ensureDlv()
	if err != nil {
		return "", err
	}
	return "dlv 已就绪: " + path, nil
}

// isWailsProject returns true if rootPath looks like a Wails application.
// Detection: wails.json exists, OR go.mod references github.com/wailsapp/wails/v3.
func isWailsProject(rootPath string) bool {
	if _, err := os.Stat(filepath.Join(rootPath, "wails.json")); err == nil {
		return true
	}
	gomod, err := os.ReadFile(filepath.Join(rootPath, "go.mod"))
	if err == nil && strings.Contains(string(gomod), "wailsapp/wails") {
		return true
	}
	return false
}

// ScanMainPackages walks rootPath and returns relative paths (from rootPath) of
// every directory that contains at least one file declaring "package main".
// Returns an error if the project is a Wails app (requires frontend to debug).
func (s *Service) ScanMainPackages(rootPath string) ([]string, error) {
	// Detect Wails project — cannot be debugged without a running frontend.
	if isWailsProject(rootPath) {
		return nil, fmt.Errorf("wails-project")
	}
	var results []string
	seen := map[string]bool{}
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		// Skip hidden dirs, vendor, testdata, node_modules
		if d.IsDir() && (name == "vendor" || name == "testdata" || name == "node_modules" ||
			(len(name) > 0 && name[0] == '.')) {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(name) != ".go" {
			return nil
		}
		dir := filepath.Dir(path)
		if seen[dir] {
			return nil
		}
		data, err2 := os.ReadFile(path)
		if err2 != nil {
			return nil
		}
		// Quick text search – avoids full Go parser overhead
		content := string(data)
		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "package main" || strings.HasPrefix(trimmed, "package main ") || strings.HasPrefix(trimmed, "package main\t") {
				seen[dir] = true
				rel, _ := filepath.Rel(rootPath, dir)
				if rel == "" || rel == "." {
					rel = "."
				}
				results = append(results, rel)
				break
			}
		}
		return nil
	})
	return results, err
}

// StartAndGetPort starts the WebSocket proxy server (idempotent) and returns its port.
func (s *Service) StartAndGetPort() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.wsPort, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("debug proxy bind: %w", err)
	}
	s.wsPort = l.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/dap", s.handleDAP)
	go func() {
		if err := http.Serve(l, mux); err != nil {
			log.Printf("debug proxy stopped: %v", err)
		}
	}()
	s.running = true
	return s.wsPort, nil
}

// killOrphanedDebugBins kills any previously orphaned dlv-compiled debug binaries
// (whose parent dlv process already exited but the binary is still running)
// and removes leftover files from the managed temp dir and project roots.
func (s *Service) killOrphanedDebugBins() {
	// Kill any running __debug_bin processes.
	exec.Command("pkill", "-9", "-f", "__debug_bin").Run() //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	// Clean files from managed temp dir.
	s.cleanDebugBinFiles()
}

// cleanDebugBinFiles removes all non-active debug binaries from the managed temp dir.
func (s *Service) cleanDebugBinFiles() {
	dir := debugTmpBase()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Collect active output paths so we don't delete in-use binaries.
	s.mu.Lock()
	activePaths := make(map[string]bool, len(s.sessions))
	for root := range s.sessions {
		activePaths[debugOutputPath(root)] = true
	}
	s.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if activePaths[path] {
			continue
		}
		if err := os.Remove(path); err == nil {
			log.Printf("debug: removed orphan %s", path)
		}
	}
}

// CleanLegacyDebugBins scans all known project roots for legacy __debug_bin*
// files left behind by older versions that wrote directly into the project dir.
// Call once on startup.
func (s *Service) CleanLegacyDebugBins() {
	s.mu.Lock()
	roots := make([]string, 0, len(s.sessions))
	for r := range s.sessions {
		roots = append(roots, r)
	}
	s.mu.Unlock()
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "__debug_bin") {
				path := filepath.Join(root, e.Name())
				if err := os.Remove(path); err == nil {
					log.Printf("debug: removed legacy debug bin %s", path)
				}
			}
		}
	}
}

// StartGoDebug launches dlv dap for the given project root and returns the dlv port.
func (s *Service) StartGoDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	// Clean up any orphaned debuggee binaries from sessions started before this
	// app instance (e.g. after a crash/restart where killSession was never called).
	s.killOrphanedDebugBins()

	// Ensure dlv is available (downloads if needed, ~10s first time).
	dlvBin, err := ensureDlv()
	if err != nil {
		return 0, fmt.Errorf("dlv 不可用: %w", err)
	}

	s.mu.Lock()
	dlvPort, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	// Redirect compiled debug binary to managed temp dir with project name prefix.
	outputPath := debugOutputPath(rootPath)
	cmd := exec.Command(dlvBin, "dap",
		"--headless",
		fmt.Sprintf("--listen=127.0.0.1:%d", dlvPort),
		"--log=false",
		"--only-same-user=false",
		"--output", outputPath,
	)
	cmd.Dir = rootPath
	// Build env with PATH replaced (not appended) to avoid duplicate-key ambiguity.
	baseEnv := os.Environ()
	newEnv := make([]string, 0, len(baseEnv)+1)
	for _, e := range baseEnv {
		if !strings.HasPrefix(e, "PATH=") {
			newEnv = append(newEnv, e)
		}
	}
	newEnv = append(newEnv, "PATH="+debugPath())
	cmd.Env = newEnv
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Pipe both stdout and stderr so we can forward all dlv/debuggee output.
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start dlv: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	sess := &debugSession{adapter: AdapterDlv, cmd: cmd, port: dlvPort, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	pipeToChannel := func(pipe io.Reader, prefix string) {
		if pipe == nil {
			return
		}
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("%s: %s", prefix, line)
			select {
			case stderrCh <- line:
			default:
			}
		}
	}
	go func() {
		go pipeToChannel(stdoutPipe, "adapter stdout")
		pipeToChannel(stderrPipe, "adapter stderr")
		cmd.Wait() //nolint:errcheck
		log.Printf("debug adapter exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return dlvPort, nil
}

// ── LLDB DAP support for C/C++/Rust ──────────────────────────────

// managedLldbDapPath returns the path where we cache our own lldb-dap copy.
func managedLldbDapPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "bin", "lldb-dap")
}

// ensureLldbDap returns an absolute path to a working lldb-dap binary.
// Priority: managed cache → Xcode/LLDB → PATH → auto-download.
func ensureLldbDap() (string, error) {
	// 1. our own cached copy
	managed := managedLldbDapPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return managed, nil
	}
	// 2. Xcode bundled lldb-dap (macOS)
	xcPath := "/Applications/Xcode.app/Contents/Developer/usr/bin/lldb-dap"
	if fi, err := os.Stat(xcPath); err == nil && !fi.IsDir() {
		return xcPath, nil
	}
	// 3. Homebrew LLVM
	brewPath := "/opt/homebrew/opt/llvm/bin/lldb-dap"
	if fi, err := os.Stat(brewPath); err == nil && !fi.IsDir() {
		return brewPath, nil
	}
	// 4. PATH
	if p, err := exec.LookPath("lldb-dap"); err == nil {
		return p, nil
	}
	// 5. auto-download prebuilt binary
	return downloadLldbDap()
}

const lldbDapVersion = "0.2.0"

func downloadLldbDap() (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	binName := "lldb-dap"
	if goos == "windows" {
		binName = "lldb-dap.exe"
	}
	zipName := fmt.Sprintf("lldb-dap-%s-%s-%s.zip", lldbDapVersion, goos, goarch)

	ghPath := fmt.Sprintf("https://github.com/llvm/llvm-project/releases/download/lldb-dap-v%s/%s", lldbDapVersion, zipName)
	urls := []string{
		"https://ghproxy.com/" + ghPath,
		"https://gh-proxy.com/" + ghPath,
		ghPath,
	}

	log.Printf("debug: downloading lldb-dap v%s (%s/%s)...", lldbDapVersion, goos, goarch)
	data, src, err := fetchFastest(urls)
	if err != nil {
		return "", fmt.Errorf("下载 lldb-dap 失败: %w", err)
	}
	log.Printf("debug: downloaded lldb-dap from %s (%d bytes)", src, len(data))

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("解压 lldb-dap zip 失败: %w", err)
	}
	var binData []byte
	for _, f := range zr.File {
		if filepath.Base(f.Name) == binName {
			rc, err2 := f.Open()
			if err2 != nil {
				return "", fmt.Errorf("打开 lldb-dap zip 条目失败: %w", err2)
			}
			binData, err2 = io.ReadAll(rc)
			rc.Close()
			if err2 != nil {
				return "", fmt.Errorf("读取 lldb-dap zip 条目失败: %w", err2)
			}
			break
		}
	}
	if binData == nil {
		return "", fmt.Errorf("zip 中未找到 lldb-dap 可执行文件")
	}

	dest := managedLldbDapPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(dest, binData, 0o755); err != nil {
		return "", fmt.Errorf("写入 lldb-dap 失败: %w", err)
	}
	log.Printf("debug: lldb-dap cached at %s", dest)
	return dest, nil
}

// IsLldbDapInstalled reports whether lldb-dap is available.
func (s *Service) IsLldbDapInstalled() bool {
	managed := managedLldbDapPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return true
	}
	xcPath := "/Applications/Xcode.app/Contents/Developer/usr/bin/lldb-dap"
	if fi, err := os.Stat(xcPath); err == nil && !fi.IsDir() {
		return true
	}
	brewPath := "/opt/homebrew/opt/llvm/bin/lldb-dap"
	if fi, err := os.Stat(brewPath); err == nil && !fi.IsDir() {
		return true
	}
	_, err := exec.LookPath("lldb-dap")
	return err == nil
}

// StartLldbDapDebug launches lldb-dap for the given project root and returns the adapter port.
func (s *Service) StartLldbDapDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	lldbBin, err := ensureLldbDap()
	if err != nil {
		return 0, fmt.Errorf("lldb-dap 不可用: %w", err)
	}

	s.mu.Lock()
	port, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	cmd := exec.Command(lldbBin, "--port", strconv.Itoa(port))
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start lldb-dap: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	sess := &debugSession{adapter: AdapterLldbDap, cmd: cmd, port: port, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	pipeToChannel := func(pipe io.Reader, prefix string) {
		if pipe == nil {
			return
		}
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("%s: %s", prefix, line)
			select {
			case stderrCh <- line:
			default:
			}
		}
	}
	go func() {
		go pipeToChannel(stdoutPipe, "lldb-dap stdout")
		pipeToChannel(stderrPipe, "lldb-dap stderr")
		cmd.Wait() //nolint:errcheck
		log.Printf("lldb-dap exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return port, nil
}

// ── Python debugpy adapter ──────────────────────────────────────────

// ensureDebugpy returns a path to a working debugpy DAP adapter.
// Priority: pip-installed debugpy → auto-download.
func ensureDebugpy() (string, error) {
	// 1. pip-installed debugpy
	if p, err := exec.LookPath("debugpy-adapter"); err == nil {
		return p, nil
	}
	// 2. python -m debugpy adapter
	if p, err := exec.LookPath("python3"); err == nil {
		// Verify debugpy is installed
		cmd := exec.Command(p, "-c", "import debugpy")
		if err := cmd.Run(); err == nil {
			return p, nil // will use "python3 -m debugpy --adapter" as command
		}
	}
	if p, err := exec.LookPath("python"); err == nil {
		cmd := exec.Command(p, "-c", "import debugpy")
		if err := cmd.Run(); err == nil {
			return p, nil
		}
	}
	// 3. Try auto-install
	pythonBin := ""
	if p, err := exec.LookPath("python3"); err == nil {
		pythonBin = p
	} else if p, err := exec.LookPath("python"); err == nil {
		pythonBin = p
	}
	if pythonBin != "" {
		log.Printf("debug: installing debugpy via pip...")
		cmd := exec.Command(pythonBin, "-m", "pip", "install", "debugpy", "--quiet")
		if err := cmd.Run(); err == nil {
			return pythonBin, nil
		}
	}
	return "", fmt.Errorf("debugpy 不可用: 请安装 Python 和 debugpy (pip install debugpy)")
}

// IsDebugpyInstalled reports whether debugpy is available.
func (s *Service) IsDebugpyInstalled() bool {
	_, err := ensureDebugpy()
	return err == nil
}

// StartDebugpyDebug launches debugpy DAP adapter for the given project root.
func (s *Service) StartDebugpyDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	pythonBin, err := ensureDebugpy()
	if err != nil {
		return 0, fmt.Errorf("debugpy 不可用: %w", err)
	}

	s.mu.Lock()
	port, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	// debugpy adapter mode: python -m debugpy --adapter --port PORT
	cmd := exec.Command(pythonBin, "-m", "debugpy", "--adapter", "--port", strconv.Itoa(port))
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start debugpy: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	sess := &debugSession{adapter: AdapterDebugpy, cmd: cmd, port: port, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	pipeToChannel := func(pipe io.Reader, prefix string) {
		if pipe == nil {
			return
		}
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("%s: %s", prefix, line)
			select {
			case stderrCh <- line:
			default:
			}
		}
	}
	go func() {
		go pipeToChannel(stdoutPipe, "debugpy stdout")
		pipeToChannel(stderrPipe, "debugpy stderr")
		cmd.Wait() //nolint:errcheck
		log.Printf("debugpy exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return port, nil
}

// ── Node.js js-debug-dap adapter ──────────────────────────────────

// managedJsDebugPath returns the path where we cache js-debug-dap.
func managedJsDebugPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "bin", "js-debug-dap")
}

const jsDebugVersion = "1.96.0"

// ensureJsDebug returns a path to a working js-debug-dap binary.
func ensureJsDebug() (string, error) {
	// 1. cached copy
	managed := managedJsDebugPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return managed, nil
	}
	// 2. PATH
	if p, err := exec.LookPath("js-debug-dap"); err == nil {
		return p, nil
	}
	// 3. npm global
	home, _ := os.UserHomeDir()
	npmPath := filepath.Join(home, ".npm-global", "bin", "js-debug-dap")
	if fi, err := os.Stat(npmPath); err == nil && !fi.IsDir() {
		return npmPath, nil
	}
	// 4. auto-install via npm
	if npm, err := exec.LookPath("npm"); err == nil {
		log.Printf("debug: installing js-debug-dap via npm...")
		cmd := exec.Command(npm, "install", "-g", fmt.Sprintf("js-debug@%s", jsDebugVersion), "--silent")
		if err := cmd.Run(); err == nil {
			if p, err := exec.LookPath("js-debug-dap"); err == nil {
				return p, nil
			}
		}
	}
	// 5. auto-download prebuilt
	return downloadJsDebug()
}

func downloadJsDebug() (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	zipName := fmt.Sprintf("js-debug-dap-%s-%s-%s.zip", jsDebugVersion, goos, goarch)
	ghPath := fmt.Sprintf("https://github.com/microsoft/vscode-js-debug/releases/download/v%s/%s", jsDebugVersion, zipName)
	urls := []string{
		"https://ghproxy.com/" + ghPath,
		"https://gh-proxy.com/" + ghPath,
		ghPath,
	}
	log.Printf("debug: downloading js-debug-dap v%s (%s/%s)...", jsDebugVersion, goos, goarch)
	data, src, err := fetchFastest(urls)
	if err != nil {
		return "", fmt.Errorf("下载 js-debug-dap 失败: %w", err)
	}
	log.Printf("debug: downloaded js-debug-dap from %s (%d bytes)", src, len(data))

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("解压 js-debug-dap zip 失败: %w", err)
	}
	binName := "js-debug-dap"
	if goos == "windows" {
		binName = "js-debug-dap.exe"
	}
	var binData []byte
	for _, f := range zr.File {
		if filepath.Base(f.Name) == binName {
			rc, err2 := f.Open()
			if err2 != nil {
				return "", fmt.Errorf("打开 js-debug-dap zip 条目失败: %w", err2)
			}
			binData, err2 = io.ReadAll(rc)
			rc.Close()
			if err2 != nil {
				return "", fmt.Errorf("读取 js-debug-dap zip 条目失败: %w", err2)
			}
			break
		}
	}
	if binData == nil {
		return "", fmt.Errorf("zip 中未找到 js-debug-dap 可执行文件")
	}
	dest := managedJsDebugPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(dest, binData, 0o755); err != nil {
		return "", fmt.Errorf("写入 js-debug-dap 失败: %w", err)
	}
	log.Printf("debug: js-debug-dap cached at %s", dest)
	return dest, nil
}

// IsJsDebugInstalled reports whether js-debug-dap is available.
func (s *Service) IsJsDebugInstalled() bool {
	_, err := ensureJsDebug()
	return err == nil
}

// StartJsDebug launches js-debug-dap for the given project root.
func (s *Service) StartJsDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	jsBin, err := ensureJsDebug()
	if err != nil {
		return 0, fmt.Errorf("js-debug-dap 不可用: %w", err)
	}

	s.mu.Lock()
	port, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	cmd := exec.Command(jsBin, strconv.Itoa(port))
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start js-debug-dap: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	sess := &debugSession{adapter: AdapterJsDebug, cmd: cmd, port: port, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	pipeToChannel := func(pipe io.Reader, prefix string) {
		if pipe == nil {
			return
		}
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("%s: %s", prefix, line)
			select {
			case stderrCh <- line:
			default:
			}
		}
	}
	go func() {
		go pipeToChannel(stdoutPipe, "js-debug stdout")
		pipeToChannel(stderrPipe, "js-debug stderr")
		cmd.Wait() //nolint:errcheck
		log.Printf("js-debug-dap exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return port, nil
}

// ── Java java-debug adapter ────────────────────────────────────────

// managedJavaDebugPath returns the path where we cache java-debug JAR.
func managedJavaDebugPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "bin", "java-debug.jar")
}

const javaDebugVersion = "0.53.0"

// ensureJavaDebug returns a path to a working java-debug JAR.
func ensureJavaDebug() (string, error) {
	// 1. cached copy
	managed := managedJavaDebugPath()
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return managed, nil
	}
	// 2. auto-download
	return downloadJavaDebug()
}

func downloadJavaDebug() (string, error) {
	jarName := fmt.Sprintf("com.microsoft.java.debug.plugin-%s.jar", javaDebugVersion)
	ghPath := fmt.Sprintf("https://github.com/Microsoft/java-debug/releases/download/%s/%s", javaDebugVersion, jarName)
	urls := []string{
		"https://ghproxy.com/" + ghPath,
		"https://gh-proxy.com/" + ghPath,
		ghPath,
	}
	log.Printf("debug: downloading java-debug v%s...", javaDebugVersion)
	data, src, err := fetchFastest(urls)
	if err != nil {
		return "", fmt.Errorf("下载 java-debug 失败: %w", err)
	}
	log.Printf("debug: downloaded java-debug from %s (%d bytes)", src, len(data))

	dest := managedJavaDebugPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		return "", fmt.Errorf("写入 java-debug 失败: %w", err)
	}
	log.Printf("debug: java-debug cached at %s", dest)
	return dest, nil
}

// IsJavaDebugInstalled reports whether java-debug is available.
func (s *Service) IsJavaDebugInstalled() bool {
	_, err := ensureJavaDebug()
	return err == nil
}

// StartJavaDebug launches java-debug DAP adapter for the given project root.
// Java debug requires a running JDT Language Server; we launch the debug adapter
// as a standalone process that communicates via DAP.
func (s *Service) StartJavaDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	jarPath, err := ensureJavaDebug()
	if err != nil {
		return 0, fmt.Errorf("java-debug 不可用: %w", err)
	}

	// Find java binary
	javaBin, err := exec.LookPath("java")
	if err != nil {
		return 0, fmt.Errorf("java 不可用: 请安装 JDK")
	}

	s.mu.Lock()
	port, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	// java-debug standalone DAP mode
	cmd := exec.Command(javaBin, "-jar", jarPath, "-Ddebugjadp.port="+strconv.Itoa(port))
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start java-debug: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	sess := &debugSession{adapter: AdapterJavaDebug, cmd: cmd, port: port, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	pipeToChannel := func(pipe io.Reader, prefix string) {
		if pipe == nil {
			return
		}
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("%s: %s", prefix, line)
			select {
			case stderrCh <- line:
			default:
			}
		}
	}
	go func() {
		go pipeToChannel(stdoutPipe, "java-debug stdout")
		pipeToChannel(stderrPipe, "java-debug stderr")
		cmd.Wait() //nolint:errcheck
		log.Printf("java-debug exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return port, nil
}

// ── Dart/Flutter adapter ───────────────────────────────────────────

// ensureDartSdk finds the dart binary from the Flutter SDK or standalone Dart SDK.
func ensureDartSdk() (string, error) {
	// 1. flutter sdk → dart is bundled
	if flutter, err := exec.LookPath("flutter"); err == nil {
		// flutter sdk includes dart at <sdk>/bin/dart
		// resolve the real dart path
		if dartPath, err := exec.LookPath("dart"); err == nil {
			return dartPath, nil
		}
		// fallback: derive from flutter path
		flutterDir := filepath.Dir(filepath.Dir(flutter))
		candidate := filepath.Join(flutterDir, "bin", "dart")
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	// 2. standalone dart
	if dart, err := exec.LookPath("dart"); err == nil {
		return dart, nil
	}
	return "", fmt.Errorf("Dart/Flutter SDK 不可用: 请安装 Flutter 或 Dart SDK")
}

// IsDartInstalled reports whether the Dart SDK is available.
func (s *Service) IsDartInstalled() bool {
	_, err := ensureDartSdk()
	return err == nil
}

// StartDartDebug launches the Dart DAP adapter for the given project root.
// For Flutter projects, it uses "flutter debug_adapter"; for pure Dart, "dart debug_adapter".
func (s *Service) StartDartDebug(rootPath string) (int, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[rootPath]; ok {
		s.mu.Unlock()
		s.killSession(rootPath, sess)
		s.mu.Lock()
	}
	s.mu.Unlock()

	dartBin, err := ensureDartSdk()
	if err != nil {
		return 0, fmt.Errorf("Dart SDK 不可用: %w", err)
	}

	// Detect if this is a Flutter project
	isFlutter := false
	if _, err := os.Stat(filepath.Join(rootPath, "pubspec.yaml")); err == nil {
		data, err := os.ReadFile(filepath.Join(rootPath, "pubspec.yaml"))
		if err == nil && strings.Contains(string(data), "flutter") {
			isFlutter = true
		}
	}

	s.mu.Lock()
	port, err := findFreePort()
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("find port: %w", err)
	}

	var cmd *exec.Cmd
	if isFlutter {
		// Flutter DAP: flutter debug_adapter --port PORT
		flutterBin, err := exec.LookPath("flutter")
		if err != nil {
			s.mu.Unlock()
			return 0, fmt.Errorf("flutter 不可用: 请安装 Flutter SDK")
		}
		cmd = exec.Command(flutterBin, "debug_adapter", "--port", strconv.Itoa(port))
	} else {
		// Pure Dart DAP: dart debug_adapter --port PORT
		cmd = exec.Command(dartBin, "debug_adapter", "--port", strconv.Itoa(port))
	}
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("start dart debug adapter: %w", err)
	}

	done := make(chan struct{})
	stderrCh := make(chan string, 256)
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	adapter := AdapterDart
	sess := &debugSession{adapter: adapter, cmd: cmd, port: port, done: done, stderrCh: stderrCh, pgid: pgid}
	s.sessions[rootPath] = sess
	s.mu.Unlock()

	go func() {
		go func() {
			if stdoutPipe == nil {
				return
			}
			sc := bufio.NewScanner(stdoutPipe)
			for sc.Scan() {
				log.Printf("dart-dap stdout: %s", sc.Text())
			}
		}()
		if stderrPipe != nil {
			sc := bufio.NewScanner(stderrPipe)
			for sc.Scan() {
				line := sc.Text()
				log.Printf("dart-dap stderr: %s", line)
				select {
				case stderrCh <- line:
				default:
				}
			}
		}
		cmd.Wait() //nolint:errcheck
		log.Printf("dart-dap exited for %s", rootPath)
		close(stderrCh)
		close(done)
	}()
	return port, nil
}

// ── Unified debug API ──────────────────────────────────────────────

// DebugStatus describes the current debug state for a project.
type DebugStatus struct {
	Active   bool        `json:"active"`
	Adapter  AdapterType `json:"adapter,omitempty"` // "dlv", "lldb-dap", or "" if not active
	Port     int         `json:"port,omitempty"`
	Language string      `json:"language,omitempty"`
}

// DetectAdapterType inspects the project root and returns the best DAP adapter.
// Returns ("", nil) for languages without a DAP adapter (run-fallback).
func (s *Service) DetectAdapterType(rootPath string) (AdapterType, error) {
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return "", nil
	}

	hasRust := false
	hasC := false
	hasSwift := false
	hasPython := false
	hasNode := false
	hasJava := false
	hasDart := false

	for _, e := range entries {
		name := e.Name()
		switch {
		// Go
		case name == "go.mod":
			return AdapterDlv, nil
		// Dart/Flutter — check pubspec.yaml for flutter dependency
		case name == "pubspec.yaml":
			hasDart = true
		// Rust
		case name == "Cargo.toml" || name == "Cargo.lock":
			hasRust = true
		// Swift
		case name == "Package.swift" || strings.HasSuffix(name, ".swift"):
			hasSwift = true
		// C/C++
		case name == "CMakeLists.txt" || name == "Makefile" || name == "configure" ||
			strings.HasSuffix(name, ".c") || strings.HasSuffix(name, ".cpp") ||
			strings.HasSuffix(name, ".cc") || strings.HasSuffix(name, ".cxx") ||
			strings.HasSuffix(name, ".h") || strings.HasSuffix(name, ".hpp") ||
			strings.HasSuffix(name, ".m") || strings.HasSuffix(name, ".mm"):
			hasC = true
		// Python
		case name == "requirements.txt" || name == "pyproject.toml" ||
			name == "Pipfile" || name == "setup.py" || name == "setup.cfg" ||
			name == "manage.py" || strings.HasSuffix(name, ".py"):
			hasPython = true
		// Node.js / JavaScript
		case name == "package.json" || name == "tsconfig.json" ||
			name == ".nvmrc" || strings.HasSuffix(name, ".ts") ||
			strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs"):
			hasNode = true
		// Java
		case name == "pom.xml" || name == "build.gradle" ||
			name == "build.gradle.kts" || name == "settings.gradle" ||
			name == "settings.gradle.kts":
			hasJava = true
		}
	}

	// Priority: specific language markers first
	if hasDart {
		return AdapterDart, nil
	}
	if hasRust {
		return AdapterLldbDap, nil
	}
	if hasSwift {
		return AdapterLldbDap, nil // lldb-dap supports Swift
	}
	if hasC {
		return AdapterLldbDap, nil
	}
	if hasPython {
		return AdapterDebugpy, nil
	}
	if hasNode {
		return AdapterJsDebug, nil
	}
	if hasJava {
		return AdapterJavaDebug, nil
	}
	return "", nil
}

// StartDebug launches the appropriate DAP adapter for the project and returns the adapter port.
// For projects without a DAP adapter, returns (0, nil) — the frontend should use run-fallback.
func (s *Service) StartDebug(rootPath string) (int, error) {
	adapter, err := s.DetectAdapterType(rootPath)
	if err != nil {
		return 0, err
	}
	switch adapter {
	case AdapterDlv:
		return s.StartGoDebug(rootPath)
	case AdapterLldbDap:
		return s.StartLldbDapDebug(rootPath)
	case AdapterDebugpy:
		return s.StartDebugpyDebug(rootPath)
	case AdapterJsDebug:
		return s.StartJsDebug(rootPath)
	case AdapterJavaDebug:
		return s.StartJavaDebug(rootPath)
	case AdapterDart:
		return s.StartDartDebug(rootPath)
	default:
		return 0, nil // no DAP adapter, frontend should use run-fallback
	}
}

// GetDebugStatus returns the current debug status for a project.
func (s *Service) GetDebugStatus(rootPath string) DebugStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[rootPath]
	if !ok {
		return DebugStatus{}
	}
	select {
	case <-sess.done:
		return DebugStatus{}
	default:
		adapter := sess.adapter
		lang := ""
		switch adapter {
		case AdapterDlv:
			lang = "golang"
		case AdapterLldbDap:
			lang = "c/cpp/rust"
		}
		return DebugStatus{Active: true, Adapter: adapter, Port: sess.port, Language: lang}
	}
}

// StopDebug kills the debug session for the given project.
func (s *Service) StopDebug(rootPath string) error {
	s.mu.Lock()
	sess, ok := s.sessions[rootPath]
	s.mu.Unlock()
	if ok {
		s.killSession(rootPath, sess)
	}
	return nil
}

// IsDebugging reports whether a debug session is active for the project.
func (s *Service) IsDebugging(rootPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[rootPath]
	if !ok {
		return false
	}
	select {
	case <-sess.done:
		return false
	default:
		return true
	}
}

func (s *Service) killSession(rootPath string, sess *debugSession) {
	// Always kill the stored process group – this catches the case where dlv has
	// already exited but its child (the HTTP server) is still running as an orphan.
	if sess.pgid > 0 {
		syscall.Kill(-sess.pgid, syscall.SIGKILL) //nolint:errcheck
	}
	select {
	case <-sess.done:
	default:
		// dlv is still running; also kill via the live process handle as a fallback.
		if sess.cmd.Process != nil {
			sess.cmd.Process.Kill() //nolint:errcheck
		}
		<-sess.done
	}
	// Remove the debug binary from the managed temp dir.
	if sess.adapter == AdapterDlv {
		if p := debugOutputPath(rootPath); p != "" {
			if err := os.Remove(p); err == nil {
				log.Printf("debug: removed temp binary %s", p)
			}
		}
	}
	s.mu.Lock()
	delete(s.sessions, rootPath)
	s.mu.Unlock()
}

// handleDAP proxies WebSocket <-> dlv TCP DAP.
// Query param: port=N specifies dlv's DAP TCP port.
func (s *Service) handleDAP(w http.ResponseWriter, r *http.Request) {
	portStr := r.URL.Query().Get("port")
	if portStr == "" {
		http.Error(w, "missing port", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(32 * 1024 * 1024)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Find the session by port so we can forward its stderr.
	var sess *debugSession
	dlvPortNum, _ := strconv.Atoi(portStr)
	s.mu.Lock()
	for _, se := range s.sessions {
		if se.port == dlvPortNum {
			sess = se
			break
		}
	}
	s.mu.Unlock()

	// Retry connecting to dlv (it may take a moment to start listening)
	var tcp net.Conn
	for i := 0; i < 30; i++ {
		tcp, err = net.DialTimeout("tcp", "127.0.0.1:"+portStr, time.Second)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		conn.Write(ctx, websocket.MessageText, []byte(`{"error":"cannot connect to dlv DAP: `+err.Error()+`"}`)) //nolint:errcheck
		return
	}
	defer tcp.Close()

	// Forward dlv stderr as DAP output events so the frontend can show build/runtime errors.
	if sess != nil {
		go func() {
			for line := range sess.stderrCh {
				body, _ := json.Marshal(map[string]any{
					"seq": 0, "type": "event", "event": "output",
					"body": map[string]string{"category": "stderr", "output": line + "\n"},
				})
				hdr := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
				conn.Write(ctx, websocket.MessageText, []byte(hdr+string(body))) //nolint:errcheck
			}
		}()
	}

	// TCP → WebSocket (dlv response/events)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, readErr := tcp.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageText, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if readErr != nil {
				cancel()
				return
			}
		}
	}()

	// WebSocket → TCP (frontend DAP requests)
	for {
		_, data, readErr := conn.Read(ctx)
		if readErr != nil {
			return
		}
		if _, writeErr := tcp.Write(data); writeErr != nil {
			return
		}
	}
}
