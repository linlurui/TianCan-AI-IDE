package browser

import (
	"testing"

	"github.com/ysmood/gson"
)

// TestWrapFunc covers the adaptation to rod's Eval contract: rod always calls the
// script as a function (`<js>.apply(this, arguments)`), so a bare expression must
// be wrapped while an existing function is passed through — wrapping the latter
// would return the function object instead of calling it. The provider chat
// scripts are delivered as functions, so both shapes must be handled correctly.
func TestWrapFunc(t *testing.T) {
	cases := []struct{ in, want string }{
		{"document.title", "() => (document.title)"},
		{"window.__trusted", "() => (window.__trusted)"},
		{"1 + 2", "() => (1 + 2)"},
		// Surrounding whitespace is trimmed, not carried into the wrapper.
		{"  document.body.innerText  ", "() => (document.body.innerText)"},
		// Already functions: must NOT be re-wrapped.
		{"() => document.title", "() => document.title"},
		{"() => 1", "() => 1"},
		{"(el) => el.id", "(el) => el.id"},
		{"async () => 1", "async () => 1"},
		{"function() { return 1 }", "function() { return 1 }"},
		{"x => x + 1", "x => x + 1"},
		// Async IIFE used by several provider scripts.
		{"async()=>{try{return 1}catch{return ''}}", "async()=>{try{return 1}catch{return ''}}"},
		// Empty script must still be callable.
		{"", "() => undefined"},
	}
	for _, c := range cases {
		if got := WrapFunc(c.in); got != c.want {
			t.Errorf("WrapFunc(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRenderValue covers protocol-result rendering, including the case that
// produced the runtime bug this function exists to fix: gson keeps an unparsed
// value as []byte, and json.Marshal encodes []byte as base64, so an evaluate
// result of `true` came back as "dHJ1ZQ==".
func TestRenderValue(t *testing.T) {
	cases := []struct {
		name string
		in   gson.JSON
		want string
	}{
		// Raw (unparsed) bytes — exactly what CDP returns.
		{"bool true", gson.NewFrom("true"), "true"},
		{"bool false", gson.NewFrom("false"), "false"},
		{"number", gson.NewFrom("42"), "42"},
		{"string is unquoted", gson.NewFrom(`"Probe"`), "Probe"},
		{"object compact json", gson.NewFrom(`{"a":1}`), `{"a":1}`},
		{"array compact json", gson.NewFrom(`[1,2]`), `[1,2]`},
		{"null", gson.NewFrom("null"), "null"},
		// Already-parsed values.
		{"parsed string", gson.New("Probe"), "Probe"},
		{"parsed int", gson.New(7), "7"},
		{"parsed bool", gson.New(true), "true"},
	}
	for _, c := range cases {
		if got := RenderValue(c.in); got != c.want {
			t.Errorf("%s: RenderValue() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRenderValue_NilIsEmpty guards the "no value returned" case so callers get a
// stable empty string rather than the literal "null".
func TestRenderValue_NilIsEmpty(t *testing.T) {
	var empty gson.JSON
	if got := RenderValue(empty); got != "" {
		t.Fatalf("RenderValue(nil) = %q, want empty string", got)
	}
}

// TestRenderValue_NoBase64Leak is the counterexample that pins the bug fixed in
// RenderValue: naively marshalling gson's raw []byte base64-encodes the value.
// If someone reverts to the naive path, this fails loudly instead of silently
// emitting "dHJ1ZQ==" to callers.
func TestRenderValue_NoBase64Leak(t *testing.T) {
	const base64OfTrue = "dHJ1ZQ=="
	got := RenderValue(gson.NewFrom("true"))
	if got == base64OfTrue {
		t.Fatalf("RenderValue base64-encoded the value (%q); raw []byte must be decoded first", got)
	}
	if got != "true" {
		t.Fatalf("RenderValue() = %q, want \"true\"", got)
	}
}

// TestDecodeValue covers turning a CDP result back into Go shapes — the provider
// chat flow depends on JS objects arriving as map[string]interface{}.
func TestDecodeValue(t *testing.T) {
	m, ok := DecodeValue(gson.NewFrom(`{"ok":true,"text":"hi"}`)).(map[string]interface{})
	if !ok {
		t.Fatalf("object result did not decode to map[string]interface{}")
	}
	if m["text"] != "hi" || m["ok"] != true {
		t.Fatalf("decoded object = %#v, want {ok:true text:hi}", m)
	}
	if s, ok := DecodeValue(gson.NewFrom(`"str"`)).(string); !ok || s != "str" {
		t.Fatalf("string result did not decode: got %#v", s)
	}
	if n, ok := DecodeValue(gson.NewFrom("5")).(float64); !ok || n != 5 {
		t.Fatalf("number result did not decode: got %#v", n)
	}
	var empty gson.JSON
	if DecodeValue(empty) != nil {
		t.Fatal("DecodeValue(nil) should be nil")
	}
}

// TestResolveBinNeverPanics is a cheap guard that the discovery walk is side-effect
// free and safe to call when no browser exists (used by every consumer before it
// attempts a launch).
func TestResolveBinNeverPanics(t *testing.T) {
	_ = ResolveBin()
	_ = Available()
}
