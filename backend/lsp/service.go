package lsp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// lspInstallCmds maps language alias to the install command.
// Languages without auto-install (rust-analyzer, clangd, jdtls, etc.) are omitted.
// npm commands use --prefix $HOME/.npm-global to avoid permission issues on /usr/local.
var lspInstallCmds = map[string][]string{
	"go":         {"go", "install", "golang.org/x/tools/gopls@latest"},
	"typescript": {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "typescript-language-server", "typescript"},
	"javascript": {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "typescript-language-server", "typescript"},
	"html":       {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "vscode-langservers-extracted"},
	"css":        {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "vscode-langservers-extracted"},
	"json":       {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "vscode-langservers-extracted"},
	"yaml":       {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "yaml-language-server"},
	"python":     {"pip", "install", "--user", "python-lsp-server"},
	"bash":       {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "bash-language-server"},
	"shell":      {"npm", "install", "-g", "--prefix", "$HOME/.npm-global", "bash-language-server"},
	"lua":        {"brew", "install", "lua-language-server"},
}

// knownLSPs maps extension ID / language alias to the LSP launch command.
var knownLSPs = map[string][]string{
	// Extension IDs
	"golang.go":                              {"gopls"},
	"rust-lang.rust-analyzer":               {"rust-analyzer"},
	"ms-python.python":                       {"pylsp"},
	"ms-python.pylance":                      {"pyright-langserver", "--stdio"},
	"llvm-vs-code-extensions.vscode-clangd": {"clangd"},
	"haskell.haskell":                        {"haskell-language-server-wrapper", "--lsp"},
	"scala-lang.scala":                       {"metals"},
	"vue.volar":                              {"vue-language-server", "--stdio"},
	"svelte.svelte-vscode":                   {"svelteserver", "--stdio"},
	"dart-code.dart-code":                    {"dart", "language-server"},
	"ziglang.vscode-zig":                     {"zls"},
	"ocamllabs.ocaml-platform":               {"ocamllsp"},
	"elixir-lsp.vscode-elixir-ls":            {"elixir-ls"},
	"julialang.language-julia":               {"julia", "--startup-file=no", "--history-file=no", "-e", "using LanguageServer; runserver()"},
	// Language aliases (used when no matching extension is installed)
	"go":         {"gopls"},
	"rust":       {"rust-analyzer"},
	"python":     {"pylsp"},
	"c":          {"clangd"},
	"cpp":        {"clangd"},
	"typescript": {"typescript-language-server", "--stdio"},
	"javascript": {"typescript-language-server", "--stdio"},
	"html":       {"vscode-html-language-server", "--stdio"},
	"css":        {"vscode-css-language-server", "--stdio"},
	"json":       {"vscode-json-language-server", "--stdio"},
	"yaml":       {"yaml-language-server", "--stdio"},
	"lua":        {"lua-language-server"},
	"kotlin":     {"kotlin-language-server"},
	"java":       {"jdtls"},
	"bash":       {"bash-language-server", "start"},
	"shell":      {"bash-language-server", "start"},
}

// lspProcess represents a running LSP server process.
type lspProcess struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.Reader
	clients map[chan []byte]bool
	cancel  context.CancelFunc
}

// Service manages LSP server processes and exposes a WebSocket bridge.
type Service struct {
	mu      sync.Mutex
	port    int
	running bool
	procs   map[string]*lspProcess // key: "lang:rootPath"

	// binDir is the user-configured LSP binary directory (default ~/.tiancan/lsp-bin).
	// Set via SetBinDir after config is loaded.
	binDir    string
	langPaths map[string]string // per-language binary overrides
}

// NewService creates a new LSP service.
func NewService() *Service {
	return &Service{
		procs:     make(map[string]*lspProcess),
		langPaths: make(map[string]string),
	}
}

// SetBinDir updates the LSP binary directory and per-language overrides.
// Called by main after config loads and on every settings save.
func (s *Service) SetBinDir(binDir string, langPaths map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binDir = binDir
	if langPaths != nil {
		s.langPaths = langPaths
	} else {
		s.langPaths = make(map[string]string)
	}
}

// effectiveDirs returns the ordered search dirs for LSP binaries.
// Priority: user per-lang override dir → binDir → binDir/bin → ~/go/bin →
// ~/.npm-global/bin → /usr/local/bin → /opt/homebrew/bin → system PATH dirs.
func (s *Service) effectiveDirs() []string {
	home := os.Getenv("HOME")
	dirs := []string{}
	if s.binDir != "" {
		dirs = append(dirs, s.binDir, filepath.Join(s.binDir, "bin"))
	}
	dirs = append(dirs,
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		"/usr/local/bin",
		"/opt/homebrew/bin",
	)
	for _, d := range strings.Split(os.Getenv("PATH"), ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// resolvedEnv returns an env slice with PATH pre-pended with all search dirs.
func (s *Service) resolvedEnv() []string {
	allDirs := s.effectiveDirs()
	newPath := strings.Join(allDirs, ":")
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	env = append(env, "PATH="+newPath)
	return env
}

// resolveBinary finds the full path of a binary by searching effectiveDirs.
// This is necessary because exec.Command only searches the parent process PATH,
// not the PATH we pass in c.Env.
func (s *Service) resolveBinary(name string) (string, error) {
	// If already an absolute path, just verify it exists.
	if filepath.IsAbs(name) {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("not found: %s", name)
	}
	for _, dir := range s.effectiveDirs() {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("binary %q not found in any of: %v", name, s.effectiveDirs())
}

// StartAndGetPort starts the WebSocket LSP bridge server (idempotent) and returns the port.
func (s *Service) StartAndGetPort() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.port, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("cannot bind LSP server: %w", err)
	}
	s.port = l.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/lsp", s.handleLSP)

	go func() {
		if err := http.Serve(l, mux); err != nil {
			log.Printf("LSP bridge server stopped: %v", err)
		}
	}()

	s.running = true
	return s.port, nil
}

// LookupCommand returns the LSP command for a given language or extension ID.
// If the user has configured a per-language binary path, it is substituted as cmd[0].
func (s *Service) LookupCommand(langOrExtID string) []string {
	cmd, ok := knownLSPs[langOrExtID]
	if !ok {
		return nil
	}
	s.mu.Lock()
	override := s.langPaths[langOrExtID]
	s.mu.Unlock()
	if override != "" {
		result := make([]string, len(cmd))
		copy(result, cmd)
		result[0] = override
		return result
	}
	return cmd
}

func (s *Service) handleLSP(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	rootPath := r.URL.Query().Get("rootPath")
	if lang == "" {
		http.Error(w, "missing lang parameter", http.StatusBadRequest)
		return
	}

	cmd := s.LookupCommand(lang)
	if cmd == nil {
		http.Error(w, fmt.Sprintf("no LSP configured for language: %s", lang), http.StatusNotFound)
		return
	}

	// Accept WebSocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Get or start LSP process
	procKey := lang + ":" + rootPath
	proc, err := s.getOrStartProcess(procKey, cmd, rootPath)
	if err != nil {
		conn.Write(ctx, websocket.MessageText, []byte(
			fmt.Sprintf(`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":1,"message":"LSP start error: %s"}}`, err.Error()),
		))
		return
	}

	// Register a channel for messages from this LSP process to this client
	msgCh := make(chan []byte, 256)
	proc.mu.Lock()
	proc.clients[msgCh] = true
	proc.mu.Unlock()

	defer func() {
		proc.mu.Lock()
		delete(proc.clients, msgCh)
		proc.mu.Unlock()
	}()

	// LSP process → WebSocket
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// WebSocket → LSP process stdin
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Write JSON-RPC message with Content-Length header
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
		proc.mu.Lock()
		_, werr := io.WriteString(proc.stdin, header)
		if werr == nil {
			_, werr = proc.stdin.Write(data)
		}
		proc.mu.Unlock()
		if werr != nil {
			return
		}
	}
}

func (s *Service) getOrStartProcess(key string, cmd []string, rootPath string) (*lspProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if proc, ok := s.procs[key]; ok {
		if proc.cmd.ProcessState == nil {
			return proc, nil // still running
		}
		delete(s.procs, key)
	}

	// Resolve the binary to a full path using our extended dirs.
	// exec.CommandContext only searches the *parent* process PATH, not c.Env["PATH"],
	// so we must resolve the binary ourselves before calling exec.Command.
	binaryPath, resolveErr := s.resolveBinary(cmd[0])
	if resolveErr != nil {
		log.Printf("[LSP] cannot resolve binary %q: %v", cmd[0], resolveErr)
		return nil, fmt.Errorf("LSP binary %q not found: %w", cmd[0], resolveErr)
	}
	log.Printf("[LSP] resolved %q → %s", cmd[0], binaryPath)

	fullCmd := append([]string{binaryPath}, cmd[1:]...)
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, fullCmd[0], fullCmd[1:]...)
	if rootPath != "" {
		c.Dir = rootPath
	}
	c.Env = s.resolvedEnv()

	stdin, err := c.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %q: %w", cmd[0], err)
	}

	proc := &lspProcess{
		cmd:     c,
		stdin:   stdin,
		stdout:  stdout,
		clients: make(map[chan []byte]bool),
		cancel:  cancel,
	}

	// Drain LSP stderr to prevent pipe blocking
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				log.Printf("[LSP:%s] stderr: %s", key, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Read LSP stdout and fan-out to all connected clients
	go func() {
		defer cancel()
		reader := bufio.NewReader(stdout)
		for {
			// Parse Content-Length header
			length := 0
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				if strings.HasPrefix(line, "Content-Length:") {
					val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
					length, _ = strconv.Atoi(val)
				}
			}
			if length == 0 {
				continue
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(reader, body); err != nil {
				return
			}
			// Broadcast to all clients
			proc.mu.Lock()
			for ch := range proc.clients {
				select {
				case ch <- body:
				default:
				}
			}
			proc.mu.Unlock()
		}
	}()

	s.procs[key] = proc
	return proc, nil
}

// StopAll terminates all running LSP processes (call on app shutdown).
func (s *Service) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, proc := range s.procs {
		proc.cancel()
	}
	s.procs = make(map[string]*lspProcess)
}

// CheckLspInstalled returns true if the LSP binary for the given language is found.
// Searches user binDir, per-lang overrides, and extended PATH.
func (s *Service) CheckLspInstalled(lang string) bool {
	s.mu.Lock()
	override := s.langPaths[lang]
	s.mu.Unlock()

	// If a specific binary path is configured, just check that path.
	if override != "" {
		info, err := os.Stat(override)
		return err == nil && !info.IsDir()
	}

	cmd, ok := knownLSPs[lang]
	if !ok {
		return false
	}
	binary := cmd[0]

	dirs := s.effectiveDirs()
	log.Printf("[LSP] CheckLspInstalled(%q): binary=%q, searching %d dirs: %v", lang, binary, len(dirs), dirs[:min(4, len(dirs))])
	for _, dir := range dirs {
		candidate := filepath.Join(dir, binary)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			log.Printf("[LSP] CheckLspInstalled(%q): found at %s", lang, candidate)
			return true
		}
	}
	log.Printf("[LSP] CheckLspInstalled(%q): not found in any dir", lang)
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// InstallLsp installs the LSP server for the given language into s.binDir.
// Streams log lines to the returned channel; last line is "OK" or "ERROR: <msg>".
func (s *Service) InstallLsp(lang string) (<-chan string, error) {
	tmpl, ok := lspInstallCmds[lang]
	if !ok {
		return nil, fmt.Errorf("no auto-install available for %q — please install manually", lang)
	}

	s.mu.Lock()
	binDir := s.binDir
	s.mu.Unlock()
	if binDir == "" {
		home, _ := os.UserHomeDir()
		binDir = filepath.Join(home, ".tiancan", "lsp-bin")
	}
	_ = os.MkdirAll(binDir, 0o755)
	_ = os.MkdirAll(filepath.Join(binDir, "lib"), 0o755)

	ch := make(chan string, 128)
	go func() {
		defer close(ch)
		home := os.Getenv("HOME")

		// Build env with correct PATH and install-target vars
		env := s.resolvedEnv()
		// Tell go install where to put the binary
		env = append(env, "GOBIN="+binDir)
		// Tell npm where to install (binaries land in binDir/bin/)
		env = append(env, "NPM_CONFIG_PREFIX="+binDir)

		// Substitute $HOME and $BINDIR placeholders in install command
		cmd := make([]string, len(tmpl))
		for i, arg := range tmpl {
			arg = strings.ReplaceAll(arg, "$HOME", home)
			// Replace the old .npm-global prefix with the actual binDir
			arg = strings.ReplaceAll(arg, home+"/.npm-global", binDir)
			arg = strings.ReplaceAll(arg, "$HOME/.npm-global", binDir)
			cmd[i] = arg
		}

		c := exec.Command(cmd[0], cmd[1:]...)
		c.Env = env
		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()

		if err := c.Start(); err != nil {
			ch <- "ERROR: " + err.Error()
			return
		}

		done := make(chan struct{}, 2)
		scan := func(r io.Reader) {
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				ch <- sc.Text()
			}
			done <- struct{}{}
		}
		go scan(stdout)
		go scan(stderr)
		<-done
		<-done

		if err := c.Wait(); err != nil {
			ch <- "ERROR: " + err.Error()
		} else {
			ch <- "OK"
		}
	}()
	return ch, nil
}

// GetLspBinDir returns the effective LSP binary directory shown in settings UI.
func (s *Service) GetLspBinDir() string {
	s.mu.Lock()
	dir := s.binDir
	s.mu.Unlock()
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".tiancan", "lsp-bin")
	}
	return dir
}

// InstallLspStream is the Wails-callable wrapper: runs InstallLsp and returns all output lines at once.
// For real-time streaming, use the WebSocket approach via InstallLspWS.
func (s *Service) InstallLspStream(lang string) ([]string, error) {
	ch, err := s.InstallLsp(lang)
	if err != nil {
		return nil, err
	}
	var lines []string
	for line := range ch {
		lines = append(lines, line)
	}
	return lines, nil
}

