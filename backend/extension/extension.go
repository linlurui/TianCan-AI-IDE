package extension

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Extension represents a VS Code extension
type Extension struct {
	ID            string `json:"id"`
	Name          string `json:"name"`          // display name
	TechnicalName string `json:"technicalName"` // technical name used in API paths
	Publisher     string `json:"publisher"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Installed     bool   `json:"installed"`
	Limited       bool   `json:"limited"` // true = relies on Webview/Extension Host, limited functionality
}

// webviewOnlyIDs lists known extensions that only provide Webview UI with no
// static Monaco value (themes/grammars/language configs/snippets/LSP).
var webviewOnlyIDs = map[string]bool{
	// Java tooling (webview project/dependency/test views)
	"vscjava.vscode-java-dependency":   true,
	"vscjava.vscode-java-test":         true,
	"vscjava.vscode-maven":             true,
	"vscjava.vscode-spring-initializr": true,
	"vscjava.vscode-java-pack":         true,
	// Python/Jupyter webviews
	"ms-toolsai.jupyter":                  true,
	"ms-toolsai.jupyter-keymap":           true,
	"ms-toolsai.vscode-jupyter-cell-tags": true,
	// Remote/container
	"ms-vscode-remote.remote-ssh":        true,
	"ms-vscode-remote.remote-wsl":        true,
	"ms-vscode-remote.remote-containers": true,
	// Browser preview / live share
	"ms-vscode.live-server": true,
	"ritwickdey.liveserver": true,
	// Git GUI panels
	"mhutchie.git-graph": true,
	"eamodio.gitlens":    true,
	// Project manager / explorer views
	"alefragnani.project-manager":        true,
	"christian-kohler.path-intellisense": false, // provides completions – keep
	// Docker
	"ms-azuretools.vscode-docker": true,
	// Test explorers
	"hbenl.vscode-test-explorer":       true,
	"ms-vscode.test-adapter-converter": true,
	// Debugger companions / JS debug
	"ms-vscode.js-debug-companion":      true,
	"ms-vscode.js-debug":                true,
	"ms-vscode.vscode-js-profile-flame": true,
	"ms-vscode.vscode-js-profile-table": true,
	// C/C++ / MSVC debugger
	"ms-vscode.cpptools": true,
	// Edge/Chrome devtools
	"ms-edgedevtools.vscode-edge-devtools": true,
}

// isLimitedExtension returns true for extensions that provide some value but
// also rely heavily on Webview panels (shown with a warning, not removed).
var limitedNamespaces = map[string]bool{
	"vscjava":    true,
	"ms-toolsai": true,
}

// ExtensionContributes holds the parsed contributes from a VSIX package.json.
type ExtensionContributes struct {
	Themes    []ThemeContrib    `json:"themes"`
	Languages []LanguageContrib `json:"languages"`
	Grammars  []GrammarContrib  `json:"grammars"`
	Snippets  []SnippetContrib  `json:"snippets"`
	LSPCmd    []string          `json:"lspCmd"` // non-empty if a known LSP command was detected
}

// ThemeContrib is one entry under contributes.themes.
type ThemeContrib struct {
	Label   string `json:"label"`
	UITheme string `json:"uiTheme"` // "vs", "vs-dark", "hc-black"
	Data    string `json:"data"`    // raw JSON content of the theme file
}

// LanguageContrib is one entry under contributes.languages.
type LanguageContrib struct {
	ID            string   `json:"id"`
	Extensions    []string `json:"extensions"`
	Filenames     []string `json:"filenames"`
	Configuration string   `json:"configuration"` // raw JSON of language-configuration.json
}

// GrammarContrib is one entry under contributes.grammars.
type GrammarContrib struct {
	Language  string `json:"language"`
	ScopeName string `json:"scopeName"`
	Data      string `json:"data"` // raw JSON of .tmLanguage.json (plist grammars skipped)
}

// SnippetContrib is one entry under contributes.snippets.
type SnippetContrib struct {
	Language string `json:"language"`
	Data     string `json:"data"` // raw JSON of .code-snippets file
}

// knownLSPCmds maps extension ID to the LSP server command.
var knownLSPCmds = map[string][]string{
	"golang.go":                             {"gopls"},
	"rust-lang.rust-analyzer":               {"rust-analyzer"},
	"ms-python.python":                      {"pylsp"},
	"ms-python.pylance":                     {"pyright-langserver", "--stdio"},
	"llvm-vs-code-extensions.vscode-clangd": {"clangd"},
	"haskell.haskell":                       {"haskell-language-server-wrapper", "--lsp"},
	"scala-lang.scala":                      {"metals"},
	"vue.volar":                             {"vue-language-server", "--stdio"},
	"svelte.svelte-vscode":                  {"svelteserver", "--stdio"},
	"dart-code.dart-code":                   {"dart", "language-server"},
	"ziglang.vscode-zig":                    {"zls"},
	"ocamllabs.ocaml-platform":              {"ocamllsp"},
	"elixir-lsp.vscode-elixir-ls":           {"elixir-ls"},
	"julialang.language-julia":              {"julia", "--startup-file=no", "--history-file=no", "-e", "using LanguageServer; runserver()"},
}

// Service provides extension management functionality
type Service struct {
	extensionsDir string
}

// NewService creates a new extension service
func NewService() *Service {
	homeDir, _ := os.UserHomeDir()
	extensionsDir := filepath.Join(homeDir, ".tiancan", "extensions")
	os.MkdirAll(extensionsDir, 0755)
	return &Service{extensionsDir: extensionsDir}
}

// resolveNLS replaces %key% placeholders with values from package.nls.json.
// Falls back to the original value if the key is not found.
func resolveNLS(extDir string, value string) string {
	if !strings.HasPrefix(value, "%") || !strings.HasSuffix(value, "%") || len(value) < 3 {
		return value
	}
	key := value[1 : len(value)-1]
	// Try package.nls.json first, then package.nls.en.json
	for _, candidate := range []string{"package.nls.json", "package.nls.en.json"} {
		data, err := os.ReadFile(filepath.Join(extDir, candidate))
		if err != nil {
			continue
		}
		var nls map[string]string
		if err := json.Unmarshal(data, &nls); err != nil {
			continue
		}
		if v, ok := nls[key]; ok {
			return v
		}
	}
	return value
}

// GetInstalledExtensions returns list of installed extensions
func (s *Service) GetInstalledExtensions() ([]Extension, error) {
	extensions := []Extension{}
	entries, err := os.ReadDir(s.extensionsDir)
	if err != nil {
		return extensions, nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		extDir := filepath.Join(s.extensionsDir, entry.Name(), "extension")
		packagePath := filepath.Join(extDir, "package.json")
		data, err := os.ReadFile(packagePath)
		if err != nil {
			continue
		}
		var pkg struct {
			Name        string `json:"name"`
			Publisher   string `json:"publisher"`
			Version     string `json:"version"`
			Description string `json:"description"`
			DisplayName string `json:"displayName"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			continue
		}
		displayName := resolveNLS(extDir, pkg.DisplayName)
		if displayName == "" {
			displayName = pkg.Name
		}
		extensions = append(extensions, Extension{
			ID:            fmt.Sprintf("%s.%s", pkg.Publisher, pkg.Name),
			Name:          displayName,
			TechnicalName: pkg.Name,
			Publisher:     pkg.Publisher,
			Version:       pkg.Version,
			Description:   resolveNLS(extDir, pkg.Description),
			Installed:     true,
		})
	}
	return extensions, nil
}

// classifyExtension returns (excluded, limited) for a marketplace extension.
// excluded = should be completely removed from results.
// limited  = show with a warning badge (has some value but heavy Webview reliance).
func classifyExtension(id string, categories []string) (excluded bool, limited bool) {
	// Hard-block known webview-only IDs
	if webviewOnlyIDs[id] {
		return true, false
	}

	usefulCategories := map[string]bool{
		"Programming Languages": true,
		"Snippets":              true,
		"Linters":               true,
		"Themes":                true,
		"Formatters":            true,
		"Language Packs":        true,
		"Data Science":          true,
		"Machine Learning":      true,
		"Education":             true,
		"Visualization":         true,
	}
	neverUseful := map[string]bool{
		"Debuggers":       true,
		"Testing":         true,
		"Keymaps":         true,
		"Other":           true,
		"Extension Packs": true,
	}

	hasUseful := false
	allNeverUseful := len(categories) > 0
	for _, c := range categories {
		if usefulCategories[c] {
			hasUseful = true
		}
		if !neverUseful[c] {
			allNeverUseful = false
		}
	}

	// Exclude if every category is useless
	if allNeverUseful && !hasUseful {
		return true, false
	}

	// Mark as limited if from a known webview-heavy namespace
	parts := strings.SplitN(id, ".", 2)
	if len(parts) == 2 && limitedNamespaces[parts[0]] {
		limited = true
	}
	return false, limited
}

// SearchExtensions searches for extensions in Open VSX marketplace
func (s *Service) SearchExtensions(query string) ([]Extension, error) {
	installed, _ := s.GetInstalledExtensions()
	installedMap := make(map[string]bool)
	for _, ext := range installed {
		installedMap[ext.ID] = true
	}

	// Open VSX API endpoint — empty query returns popular/featured extensions
	searchURL := fmt.Sprintf("https://open-vsx.org/api/-/search?query=%s&size=50&sortBy=downloadCount&sortOrder=desc", query)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to search extensions: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Extensions []struct {
			Namespace   string   `json:"namespace"`
			Name        string   `json:"name"`
			Version     string   `json:"version"`
			Description string   `json:"description"`
			DisplayName string   `json:"displayName"`
			Categories  []string `json:"categories"`
			Files       struct {
				Download string `json:"download"`
			} `json:"files"`
		} `json:"extensions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	extensions := []Extension{}
	for _, ext := range result.Extensions {
		// Skip extensions without a downloadable VSIX file
		if ext.Files.Download == "" {
			continue
		}
		id := fmt.Sprintf("%s.%s", ext.Namespace, ext.Name)
		excluded, limited := classifyExtension(id, ext.Categories)
		if excluded {
			continue
		}
		displayName := ext.DisplayName
		if displayName == "" {
			displayName = ext.Name
		}
		extensions = append(extensions, Extension{
			ID:            id,
			Name:          displayName,
			TechnicalName: ext.Name,
			Publisher:     ext.Namespace,
			Version:       ext.Version,
			Description:   ext.Description,
			Installed:     installedMap[id],
			Limited:       limited,
		})
	}
	return extensions, nil
}

// InstallExtension installs an extension from Open VSX marketplace
func (s *Service) InstallExtension(publisher, name string) error {
	client := &http.Client{Timeout: 60 * time.Second}

	// Step 1: Query extension metadata to confirm it exists and get latest version
	metaURL := fmt.Sprintf("https://open-vsx.org/api/%s/%s/latest", publisher, name)
	metaResp, err := client.Get(metaURL)
	if err != nil {
		return fmt.Errorf("无法连接 Open VSX: %w", err)
	}
	defer metaResp.Body.Close()

	if metaResp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("扩展 %s.%s 不在 Open VSX 市场中（Open VSX 不含微软官方扩展，可搜索其开源替代品）", publisher, name)
	}
	if metaResp.StatusCode != http.StatusOK {
		return fmt.Errorf("查询扩展信息失败，状态码 %d", metaResp.StatusCode)
	}

	var meta struct {
		Files struct {
			Download string `json:"download"`
		} `json:"files"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return fmt.Errorf("解析扩展信息失败: %w", err)
	}

	// Use download URL from metadata if available, otherwise build it
	downloadURL := meta.Files.Download
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("https://open-vsx.org/api/%s/%s/latest/download", publisher, name)
	}

	// Step 2: Download the VSIX
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，服务器返回 %d", resp.StatusCode)
	}

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "extension-*.vsix")
	if err != nil {
		return fmt.Errorf("无法创建临时文件: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("保存扩展文件失败: %w", err)
	}
	tmpFile.Close()

	// Extract VSIX (it's a ZIP file)
	return s.extractVSIX(tmpPath, publisher, name)
}

func (s *Service) extractVSIX(vsixPath, publisher, name string) error {
	reader, err := zip.OpenReader(vsixPath)
	if err != nil {
		return fmt.Errorf("failed to open VSIX: %w", err)
	}
	defer reader.Close()

	// Create extension directory
	extDir := filepath.Join(s.extensionsDir, fmt.Sprintf("%s.%s", publisher, name))
	os.RemoveAll(extDir)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		return fmt.Errorf("failed to create extension directory: %w", err)
	}

	for _, file := range reader.File {
		// Skip the "extension/" prefix in the VSIX
		path := file.Name
		if strings.HasPrefix(path, "extension/") {
			path = strings.TrimPrefix(path, "extension/")
		} else {
			continue // Skip files outside extension/
		}

		targetPath := filepath.Join(extDir, "extension", path)

		if file.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		os.MkdirAll(filepath.Dir(targetPath), 0755)

		destFile, err := os.Create(targetPath)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", targetPath, err)
		}

		srcFile, err := file.Open()
		if err != nil {
			destFile.Close()
			return fmt.Errorf("failed to open file in VSIX: %w", err)
		}

		_, err = io.Copy(destFile, srcFile)
		srcFile.Close()
		destFile.Close()
		if err != nil {
			return fmt.Errorf("failed to extract file: %w", err)
		}
	}

	return nil
}

// UninstallExtension removes an installed extension
func (s *Service) UninstallExtension(publisher, name string) error {
	extDir := filepath.Join(s.extensionsDir, fmt.Sprintf("%s.%s", publisher, name))
	return os.RemoveAll(extDir)
}

// GetExtensionsDir returns the extensions directory path
func (s *Service) GetExtensionsDir() string {
	return s.extensionsDir
}

// GetExtensionContributes parses the installed extension's package.json and returns
// all statically-applicable contributions (themes, grammars, language configs, snippets).
func (s *Service) GetExtensionContributes(publisher, name string) (*ExtensionContributes, error) {
	extDir := filepath.Join(s.extensionsDir, fmt.Sprintf("%s.%s", publisher, name), "extension")
	pkgData, err := os.ReadFile(filepath.Join(extDir, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("cannot read package.json: %w", err)
	}

	var pkg struct {
		Contributes struct {
			Themes []struct {
				Label   string `json:"label"`
				UITheme string `json:"uiTheme"`
				Path    string `json:"path"`
			} `json:"themes"`
			Languages []struct {
				ID            string   `json:"id"`
				Extensions    []string `json:"extensions"`
				Filenames     []string `json:"filenames"`
				Configuration string   `json:"configuration"`
			} `json:"languages"`
			Grammars []struct {
				Language  string `json:"language"`
				ScopeName string `json:"scopeName"`
				Path      string `json:"path"`
			} `json:"grammars"`
			Snippets []struct {
				Language string `json:"language"`
				Path     string `json:"path"`
			} `json:"snippets"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(pkgData, &pkg); err != nil {
		return nil, fmt.Errorf("cannot parse package.json: %w", err)
	}

	contrib := &ExtensionContributes{}

	// Themes
	for _, t := range pkg.Contributes.Themes {
		if t.Path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(extDir, filepath.FromSlash(t.Path)))
		if err != nil {
			continue
		}
		contrib.Themes = append(contrib.Themes, ThemeContrib{
			Label:   t.Label,
			UITheme: t.UITheme,
			Data:    string(data),
		})
	}

	// Languages + their configuration files
	for _, l := range pkg.Contributes.Languages {
		entry := LanguageContrib{
			ID:         l.ID,
			Extensions: l.Extensions,
			Filenames:  l.Filenames,
		}
		if l.Configuration != "" {
			data, err := os.ReadFile(filepath.Join(extDir, filepath.FromSlash(l.Configuration)))
			if err == nil {
				entry.Configuration = string(data)
			}
		}
		contrib.Languages = append(contrib.Languages, entry)
	}

	// Grammars (JSON/YAML only; skip binary plist)
	for _, g := range pkg.Contributes.Grammars {
		if g.Path == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(g.Path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue // skip binary plist grammars
		}
		data, err := os.ReadFile(filepath.Join(extDir, filepath.FromSlash(g.Path)))
		if err != nil {
			continue
		}
		contrib.Grammars = append(contrib.Grammars, GrammarContrib{
			Language:  g.Language,
			ScopeName: g.ScopeName,
			Data:      string(data),
		})
	}

	// Snippets
	for _, sn := range pkg.Contributes.Snippets {
		if sn.Path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(extDir, filepath.FromSlash(sn.Path)))
		if err != nil {
			continue
		}
		contrib.Snippets = append(contrib.Snippets, SnippetContrib{
			Language: sn.Language,
			Data:     string(data),
		})
	}

	// Detect known LSP command
	extID := fmt.Sprintf("%s.%s", publisher, name)
	if cmd, ok := knownLSPCmds[extID]; ok {
		contrib.LSPCmd = cmd
	}

	return contrib, nil
}

// InstallLocalVSIX installs an extension from a local .vsix file path
func (s *Service) InstallLocalVSIX(vsixPath string) error {
	// Read package.json inside VSIX to get publisher and name
	reader, err := zip.OpenReader(vsixPath)
	if err != nil {
		return fmt.Errorf("failed to open VSIX: %w", err)
	}

	var publisher, name string
	for _, file := range reader.File {
		if file.Name == "extension/package.json" {
			rc, err := file.Open()
			if err != nil {
				reader.Close()
				return fmt.Errorf("failed to read package.json: %w", err)
			}
			var pkg struct {
				Name      string `json:"name"`
				Publisher string `json:"publisher"`
			}
			if err := json.NewDecoder(rc).Decode(&pkg); err != nil {
				rc.Close()
				reader.Close()
				return fmt.Errorf("failed to parse package.json: %w", err)
			}
			rc.Close()
			publisher = pkg.Publisher
			name = pkg.Name
			break
		}
	}
	reader.Close()

	if publisher == "" || name == "" {
		return fmt.Errorf("invalid VSIX: missing publisher or name in package.json")
	}

	return s.extractVSIX(vsixPath, publisher, name)
}

// GetExtensionIcon returns the base64-encoded icon for an installed extension, or empty string
func (s *Service) GetExtensionIcon(publisher, name string) string {
	extDir := filepath.Join(s.extensionsDir, fmt.Sprintf("%s.%s", publisher, name), "extension")
	packagePath := filepath.Join(extDir, "package.json")
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return ""
	}
	var pkg struct {
		Icon string `json:"icon"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Icon == "" {
		return ""
	}
	iconPath := filepath.Join(extDir, pkg.Icon)
	iconData, err := os.ReadFile(iconPath)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(pkg.Icon))
	mimeType := "image/png"
	if ext == ".jpg" || ext == ".jpeg" {
		mimeType = "image/jpeg"
	} else if ext == ".svg" {
		mimeType = "image/svg+xml"
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(iconData))
}

// ServeExtensionAsset serves a static asset file from an installed extension.
// assetPath format: "publisher.name/relative/path/to/file"
func (s *Service) ServeExtensionAsset(assetPath string) ([]byte, string, error) {
	// Split into extension ID and relative path
	parts := strings.SplitN(assetPath, "/", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid asset path")
	}
	extID := parts[0]
	relPath := parts[1]

	// Security: prevent path traversal
	cleanPath := filepath.Clean(relPath)
	if strings.HasPrefix(cleanPath, "..") {
		return nil, "", fmt.Errorf("invalid path")
	}

	fullPath := filepath.Join(s.extensionsDir, extID, "extension", cleanPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("asset not found: %w", err)
	}

	// Determine MIME type from extension
	ext := strings.ToLower(filepath.Ext(fullPath))
	mimeMap := map[string]string{
		".js":    "application/javascript",
		".css":   "text/css",
		".json":  "application/json",
		".html":  "text/html",
		".png":   "image/png",
		".jpg":   "image/jpeg",
		".jpeg":  "image/jpeg",
		".svg":   "image/svg+xml",
		".wasm":  "application/wasm",
		".ttf":   "font/ttf",
		".woff":  "font/woff",
		".woff2": "font/woff2",
	}
	mime := mimeMap[ext]
	if mime == "" {
		mime = "application/octet-stream"
	}
	return data, mime, nil
}
