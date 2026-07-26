package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectProjectTypesDetectsFlutter(t *testing.T) {
	root := t.TempDir()
	pubspec := `name: sample_app
dependencies:
  flutter:
    sdk: flutter
`
	if err := os.WriteFile(filepath.Join(root, "pubspec.yaml"), []byte(pubspec), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := (&Service{}).DetectProjectTypes(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, result := range results {
		if result.Type == "flutter" {
			if !result.Detected {
				t.Fatal("expected Flutter project to be detected")
			}
			if result.SetupCmd != "flutter pub get" {
				t.Fatalf("unexpected Flutter setup command: %q", result.SetupCmd)
			}
			return
		}
	}
	t.Fatal("Flutter detection result was not returned")
}

func TestRunSetupUsesProjectFVMFlutter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-only")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, ".fvm", "flutter_sdk", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	flutter := filepath.Join(binDir, "flutter")
	if err := os.WriteFile(flutter, []byte("#!/bin/sh\necho fvm-flutter:$PWD:$*\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := (&Service{}).RunSetup(root, "flutter")
	if err != nil {
		t.Fatalf("RunSetup returned error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "fvm-flutter:") || !strings.Contains(out, ":pub get") {
		t.Fatalf("expected project FVM flutter to run in project dir, got %q", out)
	}
}
