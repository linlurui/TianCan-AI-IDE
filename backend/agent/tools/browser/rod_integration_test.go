package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// chromeBinEnv allows pinning a browser for these tests.
const chromeBinEnv = "TIANCAN_TEST_CHROME_BIN"

// liveBrowserEnv opts these tests in. Driving a real Chrome is inherently
// environment-dependent (binary may be absent, the sandbox may block the CDP
// socket, machine resources may be exhausted), so a mandatory live test would
// make the suite flaky for contributors. The isTrusted invariants are still
// enforced unconditionally by the pure unit tests in guard_test.go and
// manager_test.go; these tests add end-to-end proof when opted in.
const liveBrowserEnv = "TIANCAN_LIVE_BROWSER"

// liveBrowserEnabled reports whether the suite was explicitly opted in via
// TIANCAN_LIVE_BROWSER=1/true/yes.
func liveBrowserEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(liveBrowserEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// findChrome locates a local Chrome/Chromium binary, returning "" when none exists.
//
// We deliberately resolve the binary ourselves and hand it to rod: go-rod otherwise
// falls back to auto-downloading a Chromium build over the network, which hangs in
// sandboxed/offline environments. No binary => the live tests skip.
func findChrome() string {
	if p := os.Getenv(chromeBinEnv); p != "" && fileExists(p) {
		return p
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, c := range candidates {
		if fileExists(c) {
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

// launchOrSkip starts a rod-backed session against a locally installed browser.
//
// It skips rather than fails whenever the environment cannot host a real Chrome:
// no binary, no opt-in, or a start-up failure (sandboxed CDP socket, exhausted
// resources). A browserless environment is an environment property, not a defect
// in this package, and the security invariant these tests exist to demonstrate is
// already covered unconditionally by guard_test.go.
func launchOrSkip(t *testing.T) (Manager, Session) {
	t.Helper()
	if !liveBrowserEnabled() {
		t.Skipf("set %s=1 to run the live browser tests (they drive a real Chrome)", liveBrowserEnv)
	}
	bin := findChrome()
	if bin == "" {
		t.Skip("no local Chrome/Chromium found; skipping live browser test")
	}
	m := NewManager(WithBinPath(bin))
	id, s, err := m.NewSession()
	if err != nil {
		t.Skipf("live browser unavailable with %q (%v); skipping", bin, err)
	}
	t.Cleanup(func() { _ = m.CloseAll() })
	_ = id
	return m, s
}

// TestRodIntegration_TrustedClickIsTrusted proves the core isTrusted invariant:
// a click driven through rod's real CDP input arrives at the page as a trusted
// event (isTrusted === true), which synthetic dispatch (blocked by the guard)
// could never achieve.
func TestRodIntegration_TrustedClickIsTrusted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live browser test in -short mode")
	}
	_, s := launchOrSkip(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<button id="b">click</button>
<script>
  window.__trusted = null;
  document.getElementById('b').addEventListener('click', function(e){ window.__trusted = e.isTrusted; });
</script>`))
	}))
	defer srv.Close()

	ctx := context.Background()
	if err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := s.Click(ctx, "#b"); err != nil {
		t.Fatalf("click: %v", err)
	}
	out, err := s.Evaluate(ctx, "window.__trusted")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out != "true" {
		t.Fatalf("rod click must produce isTrusted=true, got %q", out)
	}
}

// TestRodIntegration_GuardBlocksSyntheticDispatch proves the isTrusted hard wall:
// even with a live browser, a script that would synthesize input is rejected
// before any CDP call, returning ErrSyntheticInput.
func TestRodIntegration_GuardBlocksSyntheticDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live browser test in -short mode")
	}
	_, s := launchOrSkip(t)

	ctx := context.Background()
	_, err := s.Evaluate(ctx, "document.body.dispatchEvent(new Event('click'))")
	if !errors.Is(err, ErrSyntheticInput) {
		t.Fatalf("synthetic dispatch must be blocked with ErrSyntheticInput, got %v", err)
	}
}

// TestRodIntegration_ReadOnlyEvaluateWorks proves legitimate read-only evaluate
// executes against a real page.
func TestRodIntegration_ReadOnlyEvaluateWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live browser test in -short mode")
	}
	_, s := launchOrSkip(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<title>Probe</title><h1 id="h">hi</h1>`))
	}))
	defer srv.Close()

	ctx := context.Background()
	if err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	title, err := s.Evaluate(ctx, "document.title")
	if err != nil {
		t.Fatalf("evaluate title: %v", err)
	}
	if title != "Probe" {
		t.Fatalf("expected title 'Probe', got %q", title)
	}
}
