// Package browser is the shared, neutral go-rod (pure-Go CDP) plumbing used by
// every browser consumer in this repo.
//
// Why this package exists: locating and launching a browser is deceptively easy
// to get wrong. go-rod will happily try to *download* a Chromium build when it
// cannot find a local binary, which turns a missing-browser situation into an
// indefinite network hang (worse: a hang that looks like a deadlock). The
// resolution here is deterministic: an operator-pinned binary wins, then a known
// list of install locations, then PATH; if nothing is found we report it instead
// of letting rod download.
//
// Package layering: this package depends only on rod + the standard library, so
// it can be imported from anywhere (agent tools, webai) without cycles.
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// BinEnv overrides the browser executable. Setting it pins a known local binary,
// which also keeps go-rod from trying to auto-download a Chromium build.
const BinEnv = "TIANCAN_BROWSER_BIN"

// ErrNoBrowser is returned when no local browser binary can be located.
var ErrNoBrowser = fmt.Errorf("no browser binary available (install Chrome/Chromium or set %s)", BinEnv)

// Options control how the browser process is started.
type Options struct {
	// Headless runs without a visible window. The login flow intentionally runs
	// headed so a human can complete authentication.
	Headless bool
	// Bin is an explicit executable path. When empty it falls back to ResolveBin.
	Bin string
}

// ResolveBin locates a local Chrome/Chromium binary, returning "" if none exists.
//
// Order: operator-pinned BinEnv, known macOS/Linux install locations, then PATH.
func ResolveBin() string {
	if p := strings.TrimSpace(os.Getenv(BinEnv)); p != "" && regularFile(p) {
		return p
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, c := range candidates {
		if regularFile(c) {
			return c
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// Launch starts a browser process and connects to it. The caller owns the
// returned Browser and must Close it.
func Launch(opts Options) (*rod.Browser, error) {
	bin := opts.Bin
	if bin == "" {
		bin = ResolveBin()
	}
	if bin == "" {
		return nil, ErrNoBrowser
	}
	l := launcher.New().Bin(bin)
	if opts.Headless {
		// 必须是 --headless=new：旧 --headless 模式已被 Chrome 132+ 移除，
		// 传旧 flag 浏览器会启动后立刻退出，表现为 CDP 连接 EOF。
		l.HeadlessNew(true)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	return b, nil
}

// Available reports whether a local browser binary can be resolved.
func Available() bool { return ResolveBin() != "" }

func regularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
