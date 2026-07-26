package project

import (
	"os"
	"path/filepath"
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
