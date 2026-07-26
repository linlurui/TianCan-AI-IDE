package project

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ProjectRoot holds the current project root path (set from frontend).
var ProjectRoot string

// GetProjectRoot returns the current project root path.
// Falls back to the parent directory of the binary or current working directory.
func GetProjectRoot() string {
	if ProjectRoot != "" {
		return ProjectRoot
	}
	// Try to detect from environment
	if envRoot := os.Getenv("TIANCAN_PROJECT_ROOT"); envRoot != "" {
		return envRoot
	}
	// Fallback: use cwd
	cwd, err := os.Getwd()
	if err == nil {
		return cwd
	}
	return ""
}

// SetProjectRoot sets the current project root path.
func SetProjectRoot(path string) {
	ProjectRoot = path
}

// DetectResult describes a detected project environment.
type DetectResult struct {
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	SetupCmd    string `json:"setupCmd"`
	Detected    bool   `json:"detected"`
}

// Service provides project detection and setup.
type Service struct{}

// NewService creates a new project Service.
func NewService() *Service { return &Service{} }

// GetCurrentProjectRoot returns the current project root path.
func (s *Service) GetCurrentProjectRoot() string {
	return GetProjectRoot()
}

// SetCurrentProjectRoot sets the current project root path.
func (s *Service) SetCurrentProjectRoot(path string) {
	SetProjectRoot(path)
}

var allTypes = []DetectResult{
	{Type: "maven", DisplayName: "Maven (Java)", Icon: "☕", Description: "mvn dependency:resolve 下载 JAR 依赖", SetupCmd: "mvn dependency:resolve -q"},
	{Type: "gradle", DisplayName: "Gradle (Java/Kotlin)", Icon: "🐘", Description: "gradle dependencies 下载依赖", SetupCmd: "gradle dependencies -q"},
	{Type: "go", DisplayName: "Go Modules", Icon: "🐹", Description: "go mod download 拉取模块", SetupCmd: "go mod download"},
	{Type: "flutter", DisplayName: "Flutter", Icon: "🦋", Description: "flutter pub get 安装 Dart/Flutter 依赖", SetupCmd: "flutter pub get"},
	{Type: "npm", DisplayName: "npm (Node.js)", Icon: "📦", Description: "npm install 安装 node_modules", SetupCmd: "npm install"},
	{Type: "yarn", DisplayName: "Yarn (Node.js)", Icon: "🧶", Description: "yarn install 安装 node_modules", SetupCmd: "yarn install"},
	{Type: "pnpm", DisplayName: "pnpm (Node.js)", Icon: "📦", Description: "pnpm install 安装 node_modules", SetupCmd: "pnpm install"},
	{Type: "pip", DisplayName: "pip (Python)", Icon: "🐍", Description: "pip install -r requirements.txt", SetupCmd: "pip install -r requirements.txt"},
	{Type: "poetry", DisplayName: "Poetry (Python)", Icon: "🐍", Description: "poetry install 安装 Python 依赖", SetupCmd: "poetry install"},
	{Type: "cargo", DisplayName: "Cargo (Rust)", Icon: "🦀", Description: "cargo fetch 下载 crate 依赖", SetupCmd: "cargo fetch"},
	{Type: "dotnet", DisplayName: ".NET", Icon: "💜", Description: "dotnet restore 还原 NuGet 包", SetupCmd: "dotnet restore"},
	{Type: "ruby", DisplayName: "Bundler (Ruby)", Icon: "💎", Description: "bundle install 安装 gem 依赖", SetupCmd: "bundle install"},
	{Type: "composer", DisplayName: "Composer (PHP)", Icon: "🐘", Description: "composer install 安装 PHP 依赖", SetupCmd: "composer install"},
}

// DetectProjectTypes scans a directory and returns detected + all available project types.
func (s *Service) DetectProjectTypes(dirPath string) ([]DetectResult, error) {
	indicators := map[string]string{
		"pom.xml":          "maven",
		"build.gradle":     "gradle",
		"build.gradle.kts": "gradle",
		"go.mod":           "go",
		"requirements.txt": "pip",
		"pyproject.toml":   "poetry",
		"setup.py":         "pip",
		"Cargo.toml":       "cargo",
		"Gemfile":          "ruby",
		"composer.json":    "composer",
	}

	detected := map[string]bool{}

	// Check standard indicator files
	for file, projectType := range indicators {
		if _, err := os.Stat(filepath.Join(dirPath, file)); err == nil {
			detected[projectType] = true
		}
	}

	// Flutter projects are identified by a pubspec with the Flutter SDK dependency.
	if data, err := os.ReadFile(filepath.Join(dirPath, "pubspec.yaml")); err == nil {
		content := string(data)
		if strings.Contains(content, "sdk: flutter") || strings.Contains(content, "\n  flutter:") {
			detected["flutter"] = true
		}
	}

	// Node.js: prefer pnpm > yarn > npm based on lock file
	if _, err := os.Stat(filepath.Join(dirPath, "package.json")); err == nil {
		if _, e := os.Stat(filepath.Join(dirPath, "pnpm-lock.yaml")); e == nil {
			detected["pnpm"] = true
		} else if _, e := os.Stat(filepath.Join(dirPath, "yarn.lock")); e == nil {
			detected["yarn"] = true
		} else {
			detected["npm"] = true
		}
	}

	// .NET: scan for *.csproj / *.sln
	entries, _ := os.ReadDir(dirPath)
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".sln") {
			detected["dotnet"] = true
			break
		}
	}

	results := make([]DetectResult, 0, len(allTypes))
	for _, t := range allTypes {
		r := t
		r.Detected = detected[t.Type]
		results = append(results, r)
	}
	return results, nil
}

// CreateProject creates a new project directory and returns its absolute path.
func (s *Service) CreateProject(parentDir, name string) (string, error) {
	projectPath := filepath.Join(parentDir, name)
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		return "", fmt.Errorf("创建项目目录失败: %w", err)
	}
	return projectPath, nil
}

// RunSetup executes the setup command for a given project type in the specified directory.
// Returns combined stdout+stderr output.
func (s *Service) RunSetup(dirPath, projectType string) (string, error) {
	var setupCmd string
	for _, t := range allTypes {
		if t.Type == projectType {
			setupCmd = t.SetupCmd
			break
		}
	}
	if setupCmd == "" {
		return "", fmt.Errorf("未知的项目类型: %s", projectType)
	}

	parts := strings.Fields(setupCmd)
	if len(parts) == 0 {
		return "", fmt.Errorf("依赖安装命令为空")
	}

	execPath, err := resolveSetupExecutable(dirPath, projectType, parts[0])
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, parts[1:]...)
	cmd.Dir = dirPath
	cmd.Env = setupEnvironment(dirPath)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(buf.String())
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("依赖安装超时，已等待 10 分钟。输出:\n%s", output)
		}
		if output != "" {
			return output, fmt.Errorf("依赖安装失败: %w\n\n%s", err, output)
		}
		return output, fmt.Errorf("依赖安装失败: %w", err)
	}
	return buf.String(), nil
}

func resolveSetupExecutable(dirPath, projectType, name string) (string, error) {
	if projectType == "flutter" {
		if path := findFlutterExecutable(dirPath); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("未找到 Flutter SDK。请先安装 Flutter，或在项目中配置 FVM（.fvm/flutter_sdk），然后重新导入。")
	}

	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range setupPathDirs(dirPath) {
		candidate := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			for _, ext := range []string{".exe", ".cmd", ".bat"} {
				if fileExecutable(candidate + ext) {
					return candidate + ext, nil
				}
			}
		}
		if fileExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到命令 %q，请先安装对应开发环境，或确认它在 PATH 中。", name)
}

func findFlutterExecutable(dirPath string) string {
	candidates := []string{
		filepath.Join(dirPath, ".fvm", "flutter_sdk", "bin", flutterBinaryName()),
		filepath.Join(dirPath, ".fvm", "versions", "stable", "bin", flutterBinaryName()),
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "fvm", "default", "bin", flutterBinaryName()),
			filepath.Join(home, ".fvm", "default", "bin", flutterBinaryName()),
			filepath.Join(home, "development", "flutter", "bin", flutterBinaryName()),
			filepath.Join(home, "Developer", "flutter", "bin", flutterBinaryName()),
			filepath.Join(home, "flutter", "bin", flutterBinaryName()),
		)
	}

	for _, dir := range setupPathDirs(dirPath) {
		candidates = append(candidates, filepath.Join(dir, flutterBinaryName()))
	}
	if path, err := exec.LookPath("flutter"); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		if fileExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

func flutterBinaryName() string {
	if runtime.GOOS == "windows" {
		return "flutter.bat"
	}
	return "flutter"
}

func setupEnvironment(dirPath string) []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	dirs := setupPathDirs(dirPath)
	if path == "" {
		path = strings.Join(dirs, string(os.PathListSeparator))
	} else {
		path = strings.Join(append(dirs, path), string(os.PathListSeparator))
	}
	env = append(env, "PATH="+path)
	return env
}

func setupPathDirs(dirPath string) []string {
	var dirs []string
	add := func(path string) {
		if path != "" {
			dirs = append(dirs, path)
		}
	}

	add(filepath.Join(dirPath, ".fvm", "flutter_sdk", "bin"))
	home, _ := os.UserHomeDir()
	if home != "" {
		add(filepath.Join(home, ".pub-cache", "bin"))
		add(filepath.Join(home, ".fvm", "default", "bin"))
		add(filepath.Join(home, "fvm", "default", "bin"))
		add(filepath.Join(home, "development", "flutter", "bin"))
		add(filepath.Join(home, "Developer", "flutter", "bin"))
		add(filepath.Join(home, "flutter", "bin"))
		add(filepath.Join(home, ".local", "bin"))
	}
	add("/opt/homebrew/bin")
	add("/usr/local/bin")
	add("/usr/bin")
	add("/bin")
	return dirs
}

func fileExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}
