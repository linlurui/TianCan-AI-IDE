package browser

import (
	"errors"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/webai/types"
)

// errEval simulates a page whose JS context is unavailable — detectAuth must
// treat that as "not authenticated yet" rather than panicking or succeeding.
func errEval(string) (interface{}, error) { return nil, errors.New("no js context") }

// TestDetectAuthMatrix covers the per-provider auth rules. These decide when the
// login flow stops polling: a false negative costs the user five minutes and a
// timeout, a false positive ships an AuthResult with nothing usable in it.
func TestDetectAuthMatrix(t *testing.T) {
	cfg := &types.ProviderConfig{AuthCookies: []string{"sid"}}

	cases := []struct {
		name    string
		eval    pageEval
		cookies map[string]string
		bearer  string
		want    bool
	}{
		// deepseek: requires a Bearer token AND extra evidence.
		{name: "deepseek complete", cookies: map[string]string{"ds_session_id": "1"}, bearer: "tok", want: true},
		{name: "deepseek no bearer", cookies: map[string]string{"ds_session_id": "1"}, want: false},
		{name: "deepseek d_id fallback", cookies: map[string]string{"d_id": "1"}, bearer: "tok", want: true},
		{name: "deepseek many cookies", cookies: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}, bearer: "tok", want: true},
		{name: "deepseek thin cookies", cookies: map[string]string{"a": "1"}, bearer: "tok", want: false},

		// grok: any one of several markers.
		{name: "grok sso", cookies: map[string]string{"sso": "1"}, want: true},
		{name: "grok ct0", cookies: map[string]string{"ct0": "1"}, want: true},
		{name: "grok none", cookies: map[string]string{"other": "1"}, want: false},

		// glm needs its refresh token cookie.
		{name: "glm refresh token", cookies: map[string]string{"chatglm_refresh_token": "r"}, want: true},
		{name: "glm missing", cookies: map[string]string{"x": "1"}, want: false},

		// claude: session cookie, or more than five cookies.
		{name: "claude session cookie", cookies: map[string]string{"sessionFoo": "1"}, want: true},
		{name: "claude six cookies", cookies: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6"}, want: true},
		{name: "claude five cookies", cookies: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5"}, want: false},

		// kimi: cookie, else localStorage via JS.
		{name: "kimi cookie", cookies: map[string]string{"access_token": "t"}, want: true},
		{name: "kimi empty", cookies: map[string]string{}, eval: errEval, want: false},

		// qwen: auth marker (or bearer) plus more than two cookies.
		{name: "qwen token + 3 cookies", cookies: map[string]string{"token": "t", "a": "1", "b": "2"}, want: true},
		{name: "qwen bearer + 3 cookies", cookies: map[string]string{"a": "1", "b": "2", "c": "3"}, bearer: "tok", want: true},
		{name: "qwen token but only 2 cookies", cookies: map[string]string{"token": "t", "a": "1"}, want: false},

		// doubao: known auth cookie, or >3 cookies containing a marker name.
		{name: "doubao sessionid", cookies: map[string]string{"sessionid": "s"}, want: true},
		{name: "doubao marker name among 4", cookies: map[string]string{"a": "1", "b": "2", "c": "3", "sessionid": "s"}, want: true},
		{name: "doubao none", cookies: map[string]string{"a": "1"}, want: false},

		// chatgpt: session-token cookie, else fetch evaluate.
		{name: "chatgpt session token", cookies: map[string]string{"__Secure-next-auth.session-token": "s"}, want: true},
		{name: "chatgpt eval empty", cookies: map[string]string{}, eval: errEval, want: false},

		// default branch uses the provider's AuthCookies list.
		{name: "default listed cookie", cookies: map[string]string{"sid": "1"}, want: true},
		{name: "default unlisted cookie", cookies: map[string]string{"nope": "1"}, want: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectAuth(providerOf(c.name), c.eval, c.cookies, cfg, c.bearer)
			if got != c.want {
				t.Fatalf("detectAuth = %v, want %v", got, c.want)
			}
		})
	}
}

// providerOf maps a case name back to its provider id so the table stays readable.
func providerOf(name string) string {
	switch name {
	case "deepseek complete", "deepseek no bearer", "deepseek d_id fallback", "deepseek many cookies", "deepseek thin cookies":
		return "deepseek"
	case "grok sso", "grok ct0", "grok none":
		return "grok"
	case "glm refresh token", "glm missing":
		return "glm"
	case "claude session cookie", "claude six cookies", "claude five cookies":
		return "claude"
	case "kimi cookie", "kimi empty":
		return "kimi"
	case "qwen token + 3 cookies", "qwen bearer + 3 cookies", "qwen token but only 2 cookies":
		return "qwen"
	case "doubao sessionid", "doubao marker name among 4", "doubao none":
		return "doubao"
	case "chatgpt session token", "chatgpt eval empty":
		return "chatgpt"
	default:
		return "unknown-provider"
	}
}

// TestDetectAuth_KimiUsesLocalStorage proves the JS fallback actually gets
// consulted when the cookie is absent — otherwise a cookie-less Kimi login would
// wait out the full five-minute window and time out.
func TestDetectAuth_KimiUsesLocalStorage(t *testing.T) {
	called := false
	eval := func(js string) (interface{}, error) {
		called = true
		return true, nil
	}
	if !detectAuth("kimi", eval, map[string]string{}, nil, "") {
		t.Fatal("kimi should authenticate via localStorage when the cookie is absent")
	}
	if !called {
		t.Fatal("expected the localStorage fallback to be consulted")
	}
}

// TestDetectAuth_NoEvalIsFailClosed is the counterexample for the nil-evaluator
// guard: before the page is ready there is no JS context, and treating that as
// success would return an AuthResult carrying no usable credentials.
func TestDetectAuth_NoEvalIsFailClosed(t *testing.T) {
	if detectAuth("kimi", nil, map[string]string{}, nil, "") {
		t.Fatal("nil evaluator must not report success")
	}
	if detectAuth("chatgpt", nil, map[string]string{}, nil, "") {
		t.Fatal("nil evaluator must not report success")
	}
}
