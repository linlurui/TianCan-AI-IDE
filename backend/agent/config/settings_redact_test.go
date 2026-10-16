package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeProjectSettings writes a settings.json under projectRoot/.tiancan and
// returns the projectRoot.
func writeProjectSettings(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, DotDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

const settingsWithSecret = `{
  "model": "gpt-4",
  "env": {"OPENAI_API_KEY": "sk-secret-123", "PATH": "/usr/bin"},
  "permissions": {"allow": ["Bash(ls)"]}
}`

// TestSettingsManager_RedactedForUI_RemovesSecretEnv verifies that the env block
// (the designated secret store) is structurally removed before crossing the UI
// boundary, while normal fields survive.
func TestSettingsManager_RedactedForUI_RemovesSecretEnv(t *testing.T) {
	home := t.TempDir()
	root := writeProjectSettings(t, settingsWithSecret)

	sm := NewSettingsManager(home)
	if err := sm.LoadAll(root); err != nil {
		t.Fatal(err)
	}

	ui, err := sm.RedactedForUI()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := ui["env"]; ok {
		t.Fatalf("env must be removed for UI: %#v", ui["env"])
	}
	if ui["model"] != "gpt-4" {
		t.Fatalf("model must survive redaction, got %v", ui["model"])
	}
	perms, ok := ui["permissions"].(map[string]interface{})
	if !ok || perms["allow"] == nil {
		t.Fatalf("permissions must survive redaction: %#v", ui["permissions"])
	}

	// Stored settings must be untouched (no mutation of the secret store).
	stored := sm.GetSettings()
	if stored.Env["OPENAI_API_KEY"] != "sk-secret-123" {
		t.Fatalf("stored env must be intact after redaction, got %#v", stored.Env)
	}
}

// TestSettingsManager_RedactedJSON_BoundaryPrimitive checks the JSON boundary
// primitive directly: valid JSON, no env key, model retained, input untouched.
func TestSettingsManager_RedactedJSON_BoundaryPrimitive(t *testing.T) {
	home := t.TempDir()
	root := writeProjectSettings(t, settingsWithSecret)

	sm := NewSettingsManager(home)
	if err := sm.LoadAll(root); err != nil {
		t.Fatal(err)
	}

	redacted, err := sm.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(redacted, &m); err != nil {
		t.Fatalf("redacted output not valid json: %v", err)
	}
	if _, ok := m["env"]; ok {
		t.Fatal("env must be absent in redacted JSON")
	}
	if m["model"] != "gpt-4" {
		t.Fatal("model must survive redacted JSON")
	}
}

// 反证: a non-secret field must be retained. If redaction over-eagerly stripped
// everything, model would vanish and this fails.
func TestSettingsManager_Redacted_RetainsNormalField(t *testing.T) {
	home := t.TempDir()
	root := writeProjectSettings(t, settingsWithSecret)

	sm := NewSettingsManager(home)
	if err := sm.LoadAll(root); err != nil {
		t.Fatal(err)
	}
	ui, err := sm.RedactedForUI()
	if err != nil {
		t.Fatal(err)
	}
	if ui["model"] != "gpt-4" {
		t.Fatal("normal field 'model' must be retained by redaction")
	}
}

// TestSettingsManager_PublicSchema_ZeroesSecretDefault verifies the UI-safe
// schema carries no default secret ("默认值也要脱敏").
func TestSettingsManager_PublicSchema_ZeroesSecretDefault(t *testing.T) {
	sm := NewSettingsManager(t.TempDir())
	pub := sm.PublicSchema()

	for _, f := range pub.Fields {
		if f.Role == "secret" && f.Default != nil {
			t.Fatalf("public schema must zero secret default for %q, got %#v", f.Path, f.Default)
		}
	}
	// And a normal field keeps its default.
	var modelDef interface{}
	for _, f := range pub.Fields {
		if f.Path == "model" {
			modelDef = f.Default
		}
	}
	if modelDef != "" {
		t.Fatalf("normal field 'model' default must be retained, got %v", modelDef)
	}
}
