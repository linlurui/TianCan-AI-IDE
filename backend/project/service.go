package project

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

func NewService() *Service { return &Service{} }

var allTypes = []DetectResult{
	{Type: "maven", DisplayName: "Maven (Java)", Icon: "☕", Description: "mvn dependency:resolve 下载 JAR 依赖", SetupCmd: "mvn dependency:resolve -q"},
	{Type: "gradle", DisplayName: "Gradle (Java/Kotlin)", Icon: "🐘", Description: "gradle dependencies 下载依赖", SetupCmd: "gradle dependencies -q"},
	{Type: "go", DisplayName: "Go Modules", Icon: "🐹", Description: "go mod download 拉取模块", SetupCmd: "go mod download"},
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
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = dirPath

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("配置失败: %w", err)
	}
	return buf.String(), nil
}
