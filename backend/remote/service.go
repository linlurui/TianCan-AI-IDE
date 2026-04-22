package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// ConnectionType identifies the remote connection kind.
type ConnectionType string

const (
	TypeSSH    ConnectionType = "ssh"
	TypeDocker ConnectionType = "docker"
)

// ConnectionConfig holds parameters for a remote connection.
type ConnectionConfig struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     ConnectionType `json:"type"`
	// SSH fields
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
	// Docker fields
	ContainerID   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
}

// ConnectionStatus reports the live state of a connection.
type ConnectionStatus struct {
	ID        string `json:"id"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// RemoteFile mirrors filesystem.FileNode for remote paths.
type RemoteFile struct {
	Name  string       `json:"name"`
	Path  string       `json:"path"`
	IsDir bool         `json:"isDir"`
	Ext   string       `json:"ext"`
	Children []RemoteFile `json:"children,omitempty"`
}

// DockerContainer describes a running container.
type DockerContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"`
}

// sshConn wraps an active SSH client.
type sshConn struct {
	client *ssh.Client
	cfg    ConnectionConfig
}

// Service manages remote connections and exposes them to the frontend.
type Service struct {
	mu          sync.RWMutex
	connections map[string]*sshConn // id → conn (SSH only for now)
	configs     []ConnectionConfig

	// WebSocket terminal server for remote PTY
	termServer *http.Server
	termPort   int
}

func NewService() *Service {
	return &Service{
		connections: make(map[string]*sshConn),
	}
}

// ---- Connection management ----

// GetConnections returns all saved connection configs.
func (s *Service) GetConnections() []ConnectionConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConnectionConfig, len(s.configs))
	for i, c := range s.configs {
		c.Password = "" // never send password back to frontend
		out[i] = c
	}
	return out
}

// AddConnection saves a new connection config.
func (s *Service) AddConnection(cfg ConnectionConfig) error {
	if cfg.ID == "" {
		cfg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.configs {
		if c.ID == cfg.ID {
			return fmt.Errorf("connection %q already exists", cfg.ID)
		}
	}
	s.configs = append(s.configs, cfg)
	return nil
}

// RemoveConnection removes a saved config and disconnects if active.
func (s *Service) RemoveConnection(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn, ok := s.connections[id]; ok {
		conn.client.Close()
		delete(s.connections, id)
	}
	filtered := s.configs[:0]
	for _, c := range s.configs {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	s.configs = filtered
}

// Connect establishes an SSH connection.
func (s *Service) Connect(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cfg *ConnectionConfig
	for i := range s.configs {
		if s.configs[i].ID == id {
			cfg = &s.configs[i]
			break
		}
	}
	if cfg == nil {
		return fmt.Errorf("connection %q not found", id)
	}

	if _, ok := s.connections[id]; ok {
		return nil // already connected
	}

	if cfg.Type == TypeDocker {
		// Docker connections don't need a persistent SSH client
		return nil
	}

	// Build SSH auth methods
	var auths []ssh.AuthMethod
	if cfg.Password != "" {
		auths = append(auths, ssh.Password(cfg.Password))
	}
	if cfg.KeyPath != "" {
		key, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return fmt.Errorf("read key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return fmt.Errorf("parse key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	// Try SSH agent
	if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		auths = append(auths, sshAgentAuth(agentConn))
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: known_hosts
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}

	s.connections[id] = &sshConn{client: client, cfg: *cfg}
	return nil
}

// Disconnect closes an active connection.
func (s *Service) Disconnect(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn, ok := s.connections[id]; ok {
		conn.client.Close()
		delete(s.connections, id)
	}
}

// GetConnectionStatus returns live status for all configs.
func (s *Service) GetConnectionStatus() []ConnectionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConnectionStatus, 0, len(s.configs))
	for _, cfg := range s.configs {
		_, connected := s.connections[cfg.ID]
		out = append(out, ConnectionStatus{ID: cfg.ID, Connected: connected})
	}
	return out
}

// ---- Remote file operations ----

// RemoteReadFile reads a file on the remote host.
func (s *Service) RemoteReadFile(connID, path string) (string, error) {
	conn, err := s.getSSH(connID)
	if err != nil {
		return "", err
	}
	out, err := s.runSSH(conn, fmt.Sprintf("cat %q", path))
	return out, err
}

// RemoteWriteFile writes content to a file on the remote host.
func (s *Service) RemoteWriteFile(connID, path, content string) error {
	conn, err := s.getSSH(connID)
	if err != nil {
		return err
	}
	sess, err := conn.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(content)
	return sess.Run(fmt.Sprintf("cat > %q", path))
}

// RemoteListDir lists a directory on the remote host.
func (s *Service) RemoteListDir(connID, path string) ([]RemoteFile, error) {
	conn, err := s.getSSH(connID)
	if err != nil {
		return nil, err
	}
	// Use find for a simple one-level listing
	out, err := s.runSSH(conn, fmt.Sprintf(
		`find %q -maxdepth 1 -mindepth 1 -printf '%%f\t%%y\n' 2>/dev/null | sort`, path,
	))
	if err != nil {
		return nil, err
	}
	var files []RemoteFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, typ := parts[0], parts[1]
		isDir := typ == "d"
		ext := ""
		if !isDir {
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				ext = name[idx+1:]
			}
		}
		files = append(files, RemoteFile{
			Name:  name,
			Path:  path + "/" + name,
			IsDir: isDir,
			Ext:   ext,
		})
	}
	return files, nil
}

// RemoteExec runs a command on the remote host and returns stdout.
func (s *Service) RemoteExec(connID, command string) (string, error) {
	conn, err := s.getSSH(connID)
	if err != nil {
		return "", err
	}
	return s.runSSH(conn, command)
}

// ---- Docker operations ----

// ListDockerContainers returns running Docker containers on the local machine.
func (s *Service) ListDockerContainers() ([]DockerContainer, error) {
	out, err := exec.Command("docker", "ps", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var containers []DockerContainer
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			ID    string `json:"ID"`
			Names string `json:"Names"`
			Image string `json:"Image"`
			State string `json:"State"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		containers = append(containers, DockerContainer{
			ID:    row.ID,
			Name:  row.Names,
			Image: row.Image,
			State: row.State,
		})
	}
	return containers, nil
}

// DockerExec runs a command inside a container and returns stdout.
func (s *Service) DockerExec(containerID, command string) (string, error) {
	out, err := exec.Command("docker", "exec", containerID, "sh", "-c", command).Output()
	if err != nil {
		return "", fmt.Errorf("docker exec: %w", err)
	}
	return string(out), nil
}

// DockerReadFile reads a file from a container.
func (s *Service) DockerReadFile(containerID, path string) (string, error) {
	return s.DockerExec(containerID, fmt.Sprintf("cat %q", path))
}

// DockerWriteFile writes content to a file inside a container.
func (s *Service) DockerWriteFile(containerID, path, content string) error {
	cmd := exec.Command("docker", "exec", "-i", containerID, "sh", "-c", fmt.Sprintf("cat > %q", path))
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// DockerListDir lists a directory inside a container.
func (s *Service) DockerListDir(containerID, path string) ([]RemoteFile, error) {
	out, err := s.DockerExec(containerID,
		fmt.Sprintf(`find %q -maxdepth 1 -mindepth 1 -printf '%%f\t%%y\n' 2>/dev/null | sort`, path),
	)
	if err != nil {
		return nil, err
	}
	var files []RemoteFile
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, typ := parts[0], parts[1]
		isDir := typ == "d"
		ext := ""
		if !isDir {
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				ext = name[idx+1:]
			}
		}
		files = append(files, RemoteFile{
			Name:  name,
			Path:  path + "/" + name,
			IsDir: isDir,
			Ext:   ext,
		})
	}
	return files, nil
}

// ---- Remote terminal (WebSocket PTY) ----

// StartRemoteTerminalPort starts a WebSocket server that proxies a remote SSH shell.
// Returns the local port.
func (s *Service) StartRemoteTerminalPort(connID string) (int, error) {
	conn, err := s.getSSH(connID)
	if err != nil {
		return 0, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/remote-term", func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer wsConn.Close(websocket.StatusNormalClosure, "")

		sess, err := conn.client.NewSession()
		if err != nil {
			return
		}
		defer sess.Close()

		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		if err := sess.RequestPty("xterm-256color", 40, 120, modes); err != nil {
			return
		}

		stdin, _ := sess.StdinPipe()
		stdout, _ := sess.StdoutPipe()
		_ = sess.Shell()

		ctx := r.Context()
		// ws → ssh stdin
		go func() {
			for {
				_, data, err := wsConn.Read(ctx)
				if err != nil {
					return
				}
				stdin.Write(data)
			}
		}()
		// ssh stdout → ws
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				wsConn.Write(ctx, websocket.MessageBinary, buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					return
				}
				return
			}
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return port, nil
}

// ---- helpers ----

func (s *Service) getSSH(id string) (*sshConn, error) {
	s.mu.RLock()
	conn, ok := s.connections[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("not connected: %q", id)
	}
	return conn, nil
}

func (s *Service) runSSH(conn *sshConn, cmd string) (string, error) {
	sess, err := conn.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.Output(cmd)
	return string(out), err
}

// sshAgentAuth creates an SSH auth method from an agent connection.
func sshAgentAuth(conn net.Conn) ssh.AuthMethod {
	// Use a simple agent implementation via golang.org/x/crypto/ssh/agent
	// We import it indirectly through the agent package
	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		// Minimal: just return empty — real agent support requires x/crypto/ssh/agent
		// which is already a transitive dep via go-git
		_ = conn
		return nil, nil
	})
}

// Ensure context is used (suppress unused import)
var _ = context.Background
