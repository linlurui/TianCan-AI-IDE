package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/config/schema"
)

// --- IsSecret: exact + wildcard ---

func TestIsSecret_Exact(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "password", Role: schema.RoleSecret},
	}}
	if !s.IsSecret("password") {
		t.Fatal("password should be secret")
	}
	if s.IsSecret("username") {
		t.Fatal("username must not be secret")
	}
}

func TestIsSecret_WildcardSubtree(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	// The container itself and every descendant are secret.
	if !s.IsSecret("env") {
		t.Fatal("env container should be secret under env.*")
	}
	if !s.IsSecret("env.OPENAI_API_KEY") {
		t.Fatal("env.OPENAI_API_KEY should be secret under env.*")
	}
	if !s.IsSecret("env.a.b.c") {
		t.Fatal("deep descendant of env should be secret")
	}
	if s.IsSecret("envo") {
		t.Fatal("envo must not match env.*")
	}
	if s.IsSecret("envx.foo") {
		t.Fatal("envx.foo must not match env.*")
	}
}

func TestIsSecret_OpenWorldKeepsUndeclared(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	// A sibling not covered by any secret declaration survives.
	if s.IsSecret("model") {
		t.Fatal("model is not declared secret; open-world must keep it")
	}
}

// --- Redact: structural removal + no mutation ---

func TestRedact_RemovesSecretSubtree(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	in := map[string]interface{}{
		"model": "gpt-4",
		"env": map[string]interface{}{
			"OPENAI_API_KEY": "sk-123",
			"PATH":           "/usr/bin",
		},
	}
	out := s.Redact(in)

	if _, ok := out["env"]; ok {
		t.Fatal("env subtree must be structurally removed")
	}
	if out["model"] != "gpt-4" {
		t.Fatalf("model must be retained, got %v", out["model"])
	}
}

func TestRedact_NestedExact(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "mcpServers.slack.token", Role: schema.RoleSecret},
	}}
	in := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"slack": map[string]interface{}{
				"command": "slack",
				"token":   "xoxb-secret",
			},
			"gh": map[string]interface{}{"token": "keep-me"},
		},
	}
	out := s.Redact(in)
	servers := out["mcpServers"].(map[string]interface{})
	slack := servers["slack"].(map[string]interface{})
	if _, ok := slack["token"]; ok {
		t.Fatal("slack.token must be removed")
	}
	if slack["command"] != "slack" {
		t.Fatal("slack.command must be retained")
	}
	if servers["gh"].(map[string]interface{})["token"] != "keep-me" {
		t.Fatal("gh.token must be retained (not declared secret)")
	}
}

// 反证: Redact MUST NOT mutate its input. If it mutated in-place, downstream
// code holding the original map would silently lose secret-free data too.
func TestRedact_DoesNotMutateInput(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	in := map[string]interface{}{
		"model": "gpt-4",
		"env":   map[string]interface{}{"OPENAI_API_KEY": "sk-123"},
	}
	// Snapshot the input for later comparison.
	before, _ := json.Marshal(in)

	_ = s.Redact(in)

	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatalf("Redact mutated input:\n before=%s\n after=%s", before, after)
	}
	// And crucially the env key must STILL be present in the original.
	env := in["env"].(map[string]interface{})
	if env["OPENAI_API_KEY"] != "sk-123" {
		t.Fatal("input env secret was mutated away by Redact")
	}
}

// 反证: a secret field whose value happens to equal a default is STILL removed.
// "默认值也要脱敏" means default-valued secrets are not special-cased.
func TestRedact_DefaultValuedSecretStillRemoved(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "password", Role: schema.RoleSecret, Default: "hunter2"},
	}}
	in := map[string]interface{}{"password": "hunter2"} // value == default
	out := s.Redact(in)
	if _, ok := out["password"]; ok {
		t.Fatal("secret field carrying its default value must still be redacted")
	}
}

// --- RedactJSON: boundary primitive ---

func TestRedactJSON_RoundTrip(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	in := []byte(`{"model":"gpt-4","env":{"OPENAI_API_KEY":"sk-123"},"features":{"A":true}}`)
	out, err := s.RedactJSON(in)
	if err != nil {
		t.Fatalf("RedactJSON error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("redacted output not valid json: %v", err)
	}
	if _, ok := m["env"]; ok {
		t.Fatal("env must be gone after RedactJSON")
	}
	if m["model"] != "gpt-4" {
		t.Fatal("model must survive RedactJSON")
	}
	// Input must be untouched.
	if string(in) != `{"model":"gpt-4","env":{"OPENAI_API_KEY":"sk-123"},"features":{"A":true}}` {
		t.Fatalf("RedactJSON mutated input: %s", in)
	}
}

func TestRedactJSON_EmptyInput(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{{Path: "x", Role: schema.RoleSecret}}}
	out, err := s.RedactJSON(nil)
	if err != nil || out != nil {
		t.Fatalf("empty input should pass through: out=%v err=%v", out, err)
	}
}

// --- Public: zero secret defaults ---

func TestPublic_ZeroesSecretDefaults(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "password", Role: schema.RoleSecret, Default: "hunter2"},
		{Path: "model", Role: schema.RoleNormal, Default: "gpt-4"},
	}}
	pub := s.Public()
	for _, f := range pub.Fields {
		if f.Role == schema.RoleSecret && f.Default != nil {
			t.Fatalf("secret field %q must have nil default in Public, got %v", f.Path, f.Default)
		}
	}
	// Normal field keeps its default.
	var modelDef interface{}
	for _, f := range pub.Fields {
		if f.Path == "model" {
			modelDef = f.Default
		}
	}
	if modelDef != "gpt-4" {
		t.Fatalf("normal field default must be retained, got %v", modelDef)
	}
}

// 反证: if Public forgot to zero a secret default, the UI would receive a secret.
func TestPublic_SecretDefaultNotLeaked(t *testing.T) {
	s := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret, Default: map[string]interface{}{"OPENAI_API_KEY": "sk-leak"}},
	}}
	pub := s.Public()
	for _, f := range pub.Fields {
		if f.Role == schema.RoleSecret && f.Default != nil {
			t.Fatalf("secret default must be nil in Public schema, got %#v", f.Default)
		}
	}
}

// --- Union: conservative secret wins ---

func TestUnion_SecretWins(t *testing.T) {
	a := schema.Schema{Fields: []schema.Field{
		{Path: "token", Role: schema.RoleNormal},
	}}
	b := schema.Schema{Fields: []schema.Field{
		{Path: "token", Role: schema.RoleSecret},
	}}
	u := schema.Union(a, b)
	if !u.IsSecret("token") {
		t.Fatal("union must mark token secret (b declared it secret)")
	}
}

// 反证: if Union took the INTERSECTION (secret only if ALL branches agree),
// token would NOT be secret here and the test would fail. This pins the
// conservative "any branch" semantics from the plan.
func TestUnion_ConservativeAnyBranch(t *testing.T) {
	global := schema.Schema{Fields: []schema.Field{
		{Path: "env.*", Role: schema.RoleSecret},
	}}
	project := schema.Schema{Fields: []schema.Field{
		// project schema is silent about env -> would be lost under intersection
		{Path: "model", Role: schema.RoleNormal},
	}}
	u := schema.Union(global, project)
	if !u.IsSecret("env.OPENAI_API_KEY") {
		t.Fatal("conservative union must keep env secret even though project schema is silent")
	}
}

func TestUnion_MergesDefaults(t *testing.T) {
	a := schema.Schema{Fields: []schema.Field{
		{Path: "model", Role: schema.RoleNormal, Default: "gpt-4"},
	}}
	b := schema.Schema{Fields: []schema.Field{
		{Path: "token", Role: schema.RoleSecret, Default: "x"},
	}}
	u := schema.Union(a, b)
	var modelDef, tokenDef interface{}
	for _, f := range u.Fields {
		switch f.Path {
		case "model":
			modelDef = f.Default
		case "token":
			tokenDef = f.Default
		}
	}
	if modelDef != "gpt-4" {
		t.Fatalf("model default lost in union: %v", modelDef)
	}
	if tokenDef != "x" {
		t.Fatalf("token default lost in union: %v", tokenDef)
	}
}
