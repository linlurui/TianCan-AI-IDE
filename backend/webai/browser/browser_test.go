package browser

import (
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// TestParseCookies covers converting a raw "a=1; b=2" cookie header into CDP
// cookie params bound to the provider's login host. Getting the domain wrong
// silently drops every cookie, and the chat flow then fails with an opaque
// provider-side auth error.
func TestParseCookies(t *testing.T) {
	got := parseCookies("sid=abc; XSRF-TOKEN=xyz", "https://chat.example.com/login")
	if len(got) != 2 {
		t.Fatalf("expected 2 cookies, got %d: %+v", len(got), got)
	}
	if got[0].Name != "sid" || got[0].Value != "abc" {
		t.Errorf("cookie 0 = %+v, want sid=abc", got[0])
	}
	if got[1].Name != "XSRF-TOKEN" || got[1].Value != "xyz" {
		t.Errorf("cookie 1 = %+v, want XSRF-TOKEN=xyz", got[1])
	}
	for _, c := range got {
		if c.Domain != "chat.example.com" {
			t.Errorf("domain = %q, want chat.example.com", c.Domain)
		}
		if c.Path != "/" {
			t.Errorf("path = %q, want /", c.Path)
		}
	}
}

// TestParseCookies_MalformedInput proves the parser is fail-safe rather than
// panicking or inventing cookies from garbage.
func TestParseCookies_MalformedInput(t *testing.T) {
	if got := parseCookies("", "https://x.example.com"); len(got) != 0 {
		t.Fatalf("empty input should yield no cookies, got %+v", got)
	}
	// Fragments without "=" are skipped, not turned into nameless cookies.
	got := parseCookies("noise; ; sid=1", "https://x.example.com")
	if len(got) != 1 || got[0].Name != "sid" {
		t.Fatalf("expected only the well-formed cookie, got %+v", got)
	}
	// An unparseable URL yields nothing (fail closed rather than guessing host).
	if got := parseCookies("sid=1", ":::not a url:::"); len(got) != 0 {
		t.Fatalf("unparseable URL should yield no cookies, got %+v", got)
	}
}

// TestInterpretChatResult covers the three reply shapes providers actually use.
func TestInterpretChatResult(t *testing.T) {
	// {ok:true, text:"..."} — ChatGPT/Qwen.
	res := interpretChatResult(map[string]interface{}{"ok": true, "text": "hello"}, "chatgpt")
	if !res.Success || res.Content != "hello" {
		t.Fatalf("text shape: got %+v", res)
	}
	// {ok:false, error:"..."} — must surface the provider's error, not succeed.
	res = interpretChatResult(map[string]interface{}{"ok": false, "error": "rate limited"}, "chatgpt")
	if res.Success || res.Error == "" {
		t.Fatalf("failure shape must be reported: got %+v", res)
	}
	// nil is a defect, not success.
	res = interpretChatResult(nil, "chatgpt")
	if res.Success || res.Error == "" {
		t.Fatalf("nil result must be an error: got %+v", res)
	}
	// An unknown shape must not be silently treated as success.
	res = interpretChatResult(map[string]interface{}{"ok": true}, "chatgpt")
	if res.Success {
		t.Fatalf("ok-without-payload must not be success: got %+v", res)
	}
}

// TestInterpretChatResult_NoSuccessWithoutPayload is the counterexample pinning
// fail-closed behaviour: if someone reordered the branches to check "text" before
// "ok", a failing provider reply could be mistaken for a successful one.
func TestInterpretChatResult_NoSuccessWithoutPayload(t *testing.T) {
	res := interpretChatResult(map[string]interface{}{
		"ok":    false,
		"error": "boom",
		"text":  "leftover from an earlier turn",
	}, "chatgpt")
	if res.Success {
		t.Fatalf("ok=false with stale text must still fail: got %+v", res)
	}
	if res.Error == "" {
		t.Fatalf("expected the provider error to be surfaced, got %+v", res)
	}
}

// TestRelevantURL covers the provider scoping of captured bearer tokens: without
// it a token belonging to an unrelated site on the page could be captured.
func TestRelevantURL(t *testing.T) {
	cases := []struct {
		provider, url string
		want          bool
	}{
		{"deepseek", "https://chat.deepseek.com/api/v0/chat/completions", true},
		{"deepseek", "https://cdn.example.com/lib.js", false},
		{"qwen", "https://chat.qwen.ai/api", true},
		{"qwen_cn", "https://qianwen.com/api", true},
		{"chatgpt", "https://chatgpt.com/backend-api/conversation", true},
	}
	for _, c := range cases {
		if got := relevantURL(c.provider, c.url); got != c.want {
			t.Errorf("relevantURL(%q, %q) = %v, want %v", c.provider, c.url, got, c.want)
		}
	}
}

// TestHeaderOf covers case-insensitive header lookup: CDP reports lowercase
// names, but relying on that alone would silently drop bearer tokens if the
// protocol changed casing.
func TestHeaderOf(t *testing.T) {
	h := proto.NetworkHeaders{"Authorization": gson.New("Bearer abc")}
	if got := headerOf(h, "authorization"); got != "Bearer abc" {
		t.Fatalf("lower-case lookup = %q, want %q", got, "Bearer abc")
	}
	if got := headerOf(h, "Authorization"); got != "Bearer abc" {
		t.Fatalf("mixed-case lookup = %q, want %q", got, "Bearer abc")
	}
	if got := headerOf(nil, "authorization"); got != "" {
		t.Fatalf("nil headers should yield empty, got %q", got)
	}
	if got := headerOf(h, "missing"); got != "" {
		t.Fatalf("missing header should yield empty, got %q", got)
	}
}

// TestGenerateHex16 covers the device-id generator used by the GLM providers:
// it must be unique across calls (a constant would collide with another client).
func TestGenerateHex16(t *testing.T) {
	a, b := generateHex16(), generateHex16()
	if len(a) != 32 {
		t.Fatalf("length = %d, want 32", len(a))
	}
	if a == b {
		t.Fatalf("two calls returned the same value %q; must be unique", a)
	}
}

// TestCookieNames is a small guard that joining cookie names (used by the
// doubao detection rule) is deterministic in content, not order.
func TestCookieNames(t *testing.T) {
	names := cookieNames(map[string]string{"a": "1", "b": "2"})
	if len(names) != 2 {
		t.Fatalf("names = %v, want 2 entries", names)
	}
	joined := names[0] + names[1]
	if joined != "ab" && joined != "ba" {
		t.Fatalf("names = %v, expected exactly a and b", names)
	}
}
