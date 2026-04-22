package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ── Model types ──────────────────────────────────────────────

type AuthType string

const (
	AuthPassword AuthType = "password"
	AuthKey      AuthType = "key"
)

type ServerConfig struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	AuthType AuthType `json:"authType"`
	Password string   `json:"password"`
	KeyPath  string   `json:"keyPath"`
	Tags     []string `json:"tags"`
}

type DeployType string

const (
	DeployShell  DeployType = "shell"
	DeployDocker DeployType = "docker"
	DeployK8s    DeployType = "k8s"
)

type DeployConfig struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Type         DeployType `json:"type"`
	ServerIDs    []string   `json:"serverIds"`
	BuildCmd     string     `json:"buildCmd"`
	LocalDir     string     `json:"localDir"`
	RemoteDir    string     `json:"remoteDir"`
	Script       string     `json:"script"`
	DockerImage  string     `json:"dockerImage"`
	Dockerfile   string     `json:"dockerfile"`
	RegistryURL  string     `json:"registryURL"`
	K8sManifest  string     `json:"k8sManifest"`
	K8sNamespace string     `json:"k8sNamespace"`
	K8sContext   string     `json:"k8sContext"`
}

type TaskStatus string

const (
	StatusIdle      TaskStatus = "idle"
	StatusBuilding  TaskStatus = "building"
	StatusUploading TaskStatus = "uploading"
	StatusDeploying TaskStatus = "deploying"
	StatusDone      TaskStatus = "done"
	StatusError     TaskStatus = "error"
)

type DeployTask struct {
	ID         string     `json:"id"`
	ConfigID   string     `json:"configId"`
	ConfigName string     `json:"configName"`
	Status     TaskStatus `json:"status"`
	StartTime  string     `json:"startTime"`
	EndTime    string     `json:"endTime"`
	Log        string     `json:"log"`
}

// ── Service ──────────────────────────────────────────────────

type Service struct {
	mu      sync.RWMutex
	servers map[string]*ServerConfig
	configs map[string]*DeployConfig
	tasks   map[string]*DeployTask
	logs    map[string]*strings.Builder
}

func NewService() *Service {
	return &Service{
		servers: make(map[string]*ServerConfig),
		configs: make(map[string]*DeployConfig),
		tasks:   make(map[string]*DeployTask),
		logs:    make(map[string]*strings.Builder),
	}
}

func uid() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ── Server management ─────────────────────────────────────────

func (s *Service) AddServer(cfg ServerConfig) ServerConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.ID == "" {
		cfg.ID = uid()
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	s.servers[cfg.ID] = &cfg
	return cfg
}

func (s *Service) UpdateServer(cfg ServerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[cfg.ID]; !ok {
		return fmt.Errorf("server %s not found", cfg.ID)
	}
	s.servers[cfg.ID] = &cfg
	return nil
}

func (s *Service) RemoveServer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[id]; !ok {
		return fmt.Errorf("server %s not found", id)
	}
	delete(s.servers, id)
	return nil
}

func (s *Service) ListServers() []ServerConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]ServerConfig, 0, len(s.servers))
	for _, sv := range s.servers {
		cp := *sv
		cp.Password = ""
		list = append(list, cp)
	}
	return list
}

func (s *Service) TestConnection(id string) error {
	s.mu.RLock()
	sv, ok := s.servers[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("server %s not found", id)
	}
	client, err := sshDial(sv)
	if err != nil {
		return err
	}
	client.Close()
	return nil
}

// ── Deploy config management ──────────────────────────────────

func (s *Service) AddConfig(cfg DeployConfig) DeployConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.ID == "" {
		cfg.ID = uid()
	}
	s.configs[cfg.ID] = &cfg
	return cfg
}

func (s *Service) UpdateConfig(cfg DeployConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.configs[cfg.ID]; !ok {
		return fmt.Errorf("config %s not found", cfg.ID)
	}
	s.configs[cfg.ID] = &cfg
	return nil
}

func (s *Service) RemoveConfig(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, id)
	return nil
}

func (s *Service) ListConfigs() []DeployConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]DeployConfig, 0, len(s.configs))
	for _, c := range s.configs {
		list = append(list, *c)
	}
	return list
}

// ── Deployment ────────────────────────────────────────────────

func (s *Service) Deploy(configID string) (string, error) {
	s.mu.RLock()
	cfg, ok := s.configs[configID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("config %s not found", configID)
	}

	taskID := uid()
	task := &DeployTask{
		ID:         taskID,
		ConfigID:   configID,
		ConfigName: cfg.Name,
		Status:     StatusBuilding,
		StartTime:  time.Now().Format(time.RFC3339),
	}
	lb := &strings.Builder{}
	s.mu.Lock()
	s.tasks[taskID] = task
	s.logs[taskID] = lb
	s.mu.Unlock()

	cfgCopy := *cfg
	go s.runDeploy(taskID, &cfgCopy, lb)
	return taskID, nil
}

func (s *Service) GetTask(taskID string) (*DeployTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	cp := *t
	if lb, ok := s.logs[taskID]; ok {
		cp.Log = lb.String()
	}
	return &cp, nil
}

func (s *Service) ListTasks() []DeployTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]DeployTask, 0, len(s.tasks))
	for id, t := range s.tasks {
		cp := *t
		if lb, ok := s.logs[id]; ok {
			cp.Log = lb.String()
		}
		list = append(list, cp)
	}
	return list
}

// ── Internal deploy runner ────────────────────────────────────

func (s *Service) setStatus(taskID string, st TaskStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		t.Status = st
		if st == StatusDone || st == StatusError {
			t.EndTime = time.Now().Format(time.RFC3339)
		}
	}
}

func (s *Service) appendLog(taskID, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lb, ok := s.logs[taskID]; ok {
		lb.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			lb.WriteString("\n")
		}
	}
}

func (s *Service) runDeploy(taskID string, cfg *DeployConfig, lb *strings.Builder) {
	logf := func(format string, args ...any) {
		s.appendLog(taskID, fmt.Sprintf("[%s] "+format, append([]any{time.Now().Format("15:04:05")}, args...)...))
	}

	// 1. Local build
	if cfg.BuildCmd != "" {
		s.setStatus(taskID, StatusBuilding)
		logf("🔨 开始构建: %s", cfg.BuildCmd)
		if err := runLocalCmd(cfg.BuildCmd, cfg.LocalDir, func(line string) { s.appendLog(taskID, line) }); err != nil {
			logf("❌ 构建失败: %v", err)
			s.setStatus(taskID, StatusError)
			return
		}
		logf("✅ 构建完成")
	}

	// 2. Get target servers
	s.mu.RLock()
	servers := make([]*ServerConfig, 0)
	for _, sid := range cfg.ServerIDs {
		if sv, ok := s.servers[sid]; ok {
			servers = append(servers, sv)
		}
	}
	s.mu.RUnlock()

	if len(servers) == 0 && cfg.Type != DeployK8s {
		logf("⚠ 未配置目标服务器，跳过上传步骤")
	}

	for _, sv := range servers {
		logf("🚀 部署到服务器: %s (%s@%s:%d)", sv.Name, sv.User, sv.Host, sv.Port)

		// 3. Upload files (if localDir set)
		if cfg.LocalDir != "" && cfg.RemoteDir != "" {
			s.setStatus(taskID, StatusUploading)
			logf("📦 上传目录 %s → %s:%s", cfg.LocalDir, sv.Host, cfg.RemoteDir)
			if err := scpUpload(sv, cfg.LocalDir, cfg.RemoteDir, func(line string) { s.appendLog(taskID, line) }); err != nil {
				logf("❌ 上传失败: %v", err)
				s.setStatus(taskID, StatusError)
				return
			}
			logf("✅ 上传完成")
		}

		s.setStatus(taskID, StatusDeploying)

		switch cfg.Type {
		case DeployShell:
			if cfg.Script != "" {
				logf("📜 执行部署脚本")
				if err := sshExec(sv, cfg.Script, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ 脚本执行失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
			}

		case DeployDocker:
			img := cfg.DockerImage
			if img == "" {
				img = "app:latest"
			}
			// Build image on remote
			if cfg.Dockerfile != "" {
				buildCmd := fmt.Sprintf("cd %s && docker build -f %s -t %s .", cfg.RemoteDir, cfg.Dockerfile, img)
				logf("🐳 构建 Docker 镜像: %s", img)
				if err := sshExec(sv, buildCmd, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ Docker build 失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
			}
			// Push if registry set
			if cfg.RegistryURL != "" {
				remoteImg := cfg.RegistryURL + "/" + img
				pushCmd := fmt.Sprintf("docker tag %s %s && docker push %s", img, remoteImg, remoteImg)
				logf("📤 推送镜像到 Registry: %s", remoteImg)
				if err := sshExec(sv, pushCmd, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ 推送失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
			}
			// Run container
			if cfg.Script != "" {
				logf("🐳 执行 Docker 命令")
				if err := sshExec(sv, cfg.Script, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ Docker run 失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
			}

		case DeployK8s:
			if cfg.K8sManifest != "" {
				// Write manifest to temp file on remote, apply it
				ns := cfg.K8sNamespace
				if ns == "" {
					ns = "default"
				}
				ctx := ""
				if cfg.K8sContext != "" {
					ctx = "--context=" + cfg.K8sContext
				}
				tmpFile := fmt.Sprintf("/tmp/k8s-manifest-%s.yaml", taskID)
				writeCmd := fmt.Sprintf("cat > %s << 'EOFMANIFEST'\n%s\nEOFMANIFEST", tmpFile, cfg.K8sManifest)
				applyCmd := fmt.Sprintf("kubectl apply -f %s -n %s %s", tmpFile, ns, ctx)
				logf("☸ 写入 K8s manifest")
				if err := sshExec(sv, writeCmd, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ 写入 manifest 失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
				logf("☸ kubectl apply -n %s", ns)
				if err := sshExec(sv, applyCmd, func(line string) { s.appendLog(taskID, line) }); err != nil {
					logf("❌ kubectl apply 失败: %v", err)
					s.setStatus(taskID, StatusError)
					return
				}
			}
		}

		logf("✅ 服务器 %s 部署完成", sv.Name)
	}

	// K8s local kubectl (no server)
	if cfg.Type == DeployK8s && len(servers) == 0 && cfg.K8sManifest != "" {
		s.setStatus(taskID, StatusDeploying)
		ns := cfg.K8sNamespace
		if ns == "" {
			ns = "default"
		}
		ctx := ""
		if cfg.K8sContext != "" {
			ctx = "--context=" + cfg.K8sContext
		}
		tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("k8s-manifest-%s.yaml", taskID))
		if err := os.WriteFile(tmpFile, []byte(cfg.K8sManifest), 0644); err != nil {
			logf("❌ 写入 manifest 失败: %v", err)
			s.setStatus(taskID, StatusError)
			return
		}
		logf("☸ 本地 kubectl apply -n %s %s", ns, ctx)
		cmd := fmt.Sprintf("kubectl apply -f %s -n %s %s", tmpFile, ns, ctx)
		if err := runLocalCmd(cmd, "", func(line string) { s.appendLog(taskID, line) }); err != nil {
			logf("❌ kubectl apply 失败: %v", err)
			s.setStatus(taskID, StatusError)
			return
		}
	}

	s.setStatus(taskID, StatusDone)
	logf("🎉 部署完成！")
}

// ── SSH helpers ───────────────────────────────────────────────

func sshDial(sv *ServerConfig) (*ssh.Client, error) {
	var authMethods []ssh.AuthMethod
	if sv.AuthType == AuthKey && sv.KeyPath != "" {
		key, err := os.ReadFile(sv.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取密钥失败: %v", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析密钥失败: %v", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else {
		authMethods = append(authMethods, ssh.Password(sv.Password))
	}
	cfg := &ssh.ClientConfig{
		User:            sv.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	port := sv.Port
	if port == 0 {
		port = 22
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", sv.Host, port), cfg)
}

func sshExec(sv *ServerConfig, script string, logLine func(string)) error {
	client, err := sshDial(sv)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	var buf bytes.Buffer
	session.Stdout = &lineWriter{fn: logLine, buf: &buf}
	session.Stderr = &lineWriter{fn: logLine, buf: &buf}
	return session.Run(script)
}

func scpUpload(sv *ServerConfig, localDir, remoteDir string, logLine func(string)) error {
	client, err := sshDial(sv)
	if err != nil {
		return err
	}
	defer client.Close()

	// Ensure remote dir exists
	mkSession, _ := client.NewSession()
	mkSession.Run("mkdir -p " + remoteDir)
	mkSession.Close()

	return filepath.WalkDir(localDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(localDir, path)
		remotePath := remoteDir + "/" + filepath.ToSlash(rel)

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		session, err := client.NewSession()
		if err != nil {
			return err
		}
		defer session.Close()

		go func() {
			w, _ := session.StdinPipe()
			defer w.Close()
			fmt.Fprintf(w, "C0644 %d %s\n", len(data), filepath.Base(remotePath))
			w.Write(data)
			fmt.Fprint(w, "\x00")
		}()

		remoteParent := filepath.ToSlash(filepath.Dir(remotePath))
		session.Run(fmt.Sprintf("mkdir -p %s && scp -t %s", remoteParent, remoteParent))
		logLine(fmt.Sprintf("  ↑ %s", rel))
		return nil
	})
}

// ── Local command runner ──────────────────────────────────────

func runLocalCmd(cmdStr, workDir string, logLine func(string)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if strings.Contains(cmdStr, "\n") || strings.Contains(cmdStr, "&&") || strings.Contains(cmdStr, ";") {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	} else {
		parts := strings.Fields(cmdStr)
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				logLine(string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
	}()
	err := cmd.Wait()
	pw.Close()
	return err
}

// ── lineWriter helper ─────────────────────────────────────────

type lineWriter struct {
	fn  func(string)
	buf *bytes.Buffer
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.fn(string(p))
	return len(p), nil
}
