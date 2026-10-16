package browser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResolveBin_HonorsEnv proves an operator-pinned binary wins over discovery.
// This is the mechanism that keeps go-rod from auto-downloading a Chromium build
// when the machine's own browser lives somewhere unusual.
func TestResolveBin_HonorsEnv(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned-chrome")
	if err := os.WriteFile(pinned, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	t.Setenv(BinEnv, pinned)
	if got := ResolveBin(); got != pinned {
		t.Fatalf("ResolveBin() = %q, want the pinned %q", got, pinned)
	}
	if !Available() {
		t.Fatal("Available() must report true when BinEnv points at a file")
	}
}

// TestResolveBin_IgnoresMissingEnvTarget guards against a stale env var silently
// being handed to the launcher: an unset path must fall through to discovery
// rather than being returned as-is.
func TestResolveBin_IgnoresMissingEnvTarget(t *testing.T) {
	t.Setenv(BinEnv, "/definitely/not/a/browser")
	got := ResolveBin()
	if got == "/definitely/not/a/browser" {
		t.Fatal("ResolveBin must not return a non-existent pinned path")
	}
}

// TestResolveBin_IgnoresDirectory catches a directory being treated as a binary
// (e.g. someone pointing BinEnv at the .app bundle instead of its executable).
func TestResolveBin_IgnoresDirectory(t *testing.T) {
	t.Setenv(BinEnv, t.TempDir())
	got := ResolveBin()
	if got == "" {
		// Falling through to discovery may legitimately find nothing.
		return
	}
	if st, err := os.Stat(got); err == nil && st.IsDir() {
		t.Fatalf("ResolveBin returned a directory: %q", got)
	}
}

// TestLaunch_MissingBinaryFailsFast is the regression test for the hang this
// package was created to prevent. go-rod's default behaviour when it cannot find
// a browser is to download one, which turned "no browser installed" into an
// indefinite network stall. We always resolve the binary ourselves first, so an
// unusable path must come back as an error promptly.
func TestLaunch_MissingBinaryFailsFast(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Launch(Options{Headless: true, Bin: filepath.Join(t.TempDir(), "no-such-chrome")})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("launching a non-existent binary must fail")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Launch did not return within 10s — it is trying to download a browser")
	}
}
