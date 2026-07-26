package providers

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type ideRPCProviderSpec struct {
	ID             string
	Name           string
	EnvPrefix      string
	DefaultModel   string
	DefaultBaseURL string
	Port           int
	Priority       int
	Icon           string
	Color          string
	AuthPaths      []string
	ExactKeys      []string
	LikeKeys       []string
}

// CursorDiscoverer discovers Cursor's local-account state and optional API bridge.
type CursorDiscoverer struct{}

func (d *CursorDiscoverer) Name() string {
	return "Cursor"
}

func (d *CursorDiscoverer) Priority() int {
	return 26
}

func (d *CursorDiscoverer) Discover() (*Provider, error) {
	return discoverIDEProvider(cursorProviderSpec())
}

func (d *CursorDiscoverer) Test(p *Provider) error {
	return testIDEProvider(p)
}

// WindsurfDiscoverer discovers Windsurf's local-account state and optional API bridge.
type WindsurfDiscoverer struct{}

func (d *WindsurfDiscoverer) Name() string {
	return "Windsurf"
}

func (d *WindsurfDiscoverer) Priority() int {
	return 27
}

func (d *WindsurfDiscoverer) Discover() (*Provider, error) {
	return discoverIDEProvider(windsurfProviderSpec())
}

func (d *WindsurfDiscoverer) Test(p *Provider) error {
	return testIDEProvider(p)
}

func cursorProviderSpec() ideRPCProviderSpec {
	home, _ := os.UserHomeDir()
	return ideRPCProviderSpec{
		ID:             "cursor-ide-rpc",
		Name:           "Cursor",
		EnvPrefix:      "CURSOR",
		DefaultModel:   "cursor-auto",
		DefaultBaseURL: "http://127.0.0.1:18780",
		Port:           18780,
		Priority:       26,
		Icon:           "⌁",
		Color:          "#111827",
		AuthPaths: []string{
			filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"),
		},
		ExactKeys: []string{"cursorAuth/accessToken", "cursorAuth/refreshToken", "cursorAuth/cachedEmail"},
	}
}

func windsurfProviderSpec() ideRPCProviderSpec {
	home, _ := os.UserHomeDir()
	return ideRPCProviderSpec{
		ID:             "windsurf-ide-rpc",
		Name:           "Windsurf",
		EnvPrefix:      "WINDSURF",
		DefaultModel:   "windsurf-auto",
		DefaultBaseURL: "http://127.0.0.1:18766",
		Port:           18766,
		Priority:       27,
		Icon:           "~",
		Color:          "#0891b2",
		AuthPaths: []string{
			filepath.Join(home, "Library", "Application Support", "Windsurf", "User", "globalStorage", "state.vscdb"),
		},
		ExactKeys: []string{
			"codeium.windsurf-windsurf_auth",
			`secret://{"extensionId":"codeium.windsurf","key":"windsurf_auth.sessions"}`,
			"windsurfAuthStatus",
		},
		LikeKeys: []string{"%windsurf%auth%", "%codeium%auth%"},
	}
}

func discoverIDEProvider(spec ideRPCProviderSpec) (*Provider, error) {
	authFile := firstExistingPath(spec.AuthPaths)
	if authFile == "" {
		return nil, fmt.Errorf("%s storage not found", spec.Name)
	}

	authorized := false
	if len(spec.ExactKeys) > 0 && sqliteHasProviderItemKey(authFile, spec.ExactKeys...) {
		authorized = true
	}
	if !authorized && len(spec.LikeKeys) > 0 && sqliteHasProviderItemKeyLike(authFile, spec.LikeKeys...) {
		authorized = true
	}
	if !authorized {
		return nil, fmt.Errorf("%s auth state not found", spec.Name)
	}

	baseURL := providerEnv(spec.EnvPrefix, "API_BASE_URL")
	status := ProviderStatusAvailable
	errText := ""
	if baseURL == "" {
		baseURL = spec.DefaultBaseURL
		if !providerTCPPortOpen("127.0.0.1", spec.Port, 250*time.Millisecond) {
			status = ProviderStatusUnavailable
			errText = fmt.Sprintf("%s 已登录，但本地 IDE RPC/API 桥接端口 %d 未监听", spec.Name, spec.Port)
		} else {
			status = ProviderStatusUnavailable
			errText = fmt.Sprintf("%s 本地端口 %d 已发现；若这是 ConnectRPC 私有桥，需要兼容桥转为 OpenAI API 后调用", spec.Name, spec.Port)
		}
	}

	return &Provider{
		ID:       spec.ID,
		Name:     spec.Name,
		Type:     ProviderTypeLocal,
		Status:   status,
		BaseURL:  baseURL,
		Model:    firstNonEmptyProviderString(providerEnv(spec.EnvPrefix, "MODEL"), spec.DefaultModel),
		Priority: spec.Priority,
		Error:    errText,
		AuthFile: authFile,
		Icon:     spec.Icon,
		Color:    spec.Color,
	}, nil
}

func testIDEProvider(p *Provider) error {
	if p == nil {
		return fmt.Errorf("provider is nil")
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("base URL is empty")
	}
	if p.Status == ProviderStatusUnavailable {
		if p.Error != "" {
			return fmt.Errorf("%s", p.Error)
		}
		return fmt.Errorf("%s unavailable", p.Name)
	}
	return nil
}

func providerEnv(prefix, suffix string) string {
	for _, key := range []string{
		"TIANCAN_" + prefix + "_" + suffix,
		prefix + "_" + suffix,
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyProviderString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstExistingPath(paths []string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func providerTCPPortOpen(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func sqliteHasProviderItemKey(dbPath string, keys ...string) bool {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return false
	}
	defer db.Close()

	tableName := sqliteProviderItemTableName(db)
	if tableName == "" {
		return false
	}
	query := fmt.Sprintf("SELECT 1 FROM %s WHERE key = ? LIMIT 1", tableName)
	for _, key := range keys {
		var exists int
		if err := db.QueryRow(query, key).Scan(&exists); err == nil && exists == 1 {
			return true
		}
	}
	return false
}

func sqliteHasProviderItemKeyLike(dbPath string, patterns ...string) bool {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return false
	}
	defer db.Close()

	tableName := sqliteProviderItemTableName(db)
	if tableName == "" {
		return false
	}
	query := fmt.Sprintf("SELECT 1 FROM %s WHERE lower(key) LIKE lower(?) LIMIT 1", tableName)
	for _, pattern := range patterns {
		var exists int
		if err := db.QueryRow(query, pattern).Scan(&exists); err == nil && exists == 1 {
			return true
		}
	}
	return false
}

func sqliteProviderItemTableName(db *sql.DB) string {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return ""
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if strings.EqualFold(name, "ItemTable") {
			return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		}
	}
	return ""
}
