// Package browser provides browser login and chat for web AI providers.
//
// It drives a real Chromium/Chrome process through go-rod (pure-Go CDP), which
// replaces the previous playwright-go implementation. Dropping playwright removes
// its bundled Node runtime (~50MB) and driver install step, keeping the binary
// pure Go with zero external runtimes to ship or keep in sync.
//
// Input produced here (clicks, typing) is real CDP input, i.e. trusted events.
package browser

import (
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	sharedbrowser "github.com/linlurui/tiancan-ai-ide/backend/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/types"
)

const (
	// loginTimeout bounds how long we wait for a human to finish authenticating.
	loginTimeout = 5 * time.Minute
	// pollInterval between auth-detection attempts.
	pollInterval = 2 * time.Second
	// pageTimeout bounds each individual CDP call.
	pageTimeout = 30 * time.Second
	// settleDelay lets the page apply restored cookies before we run page JS.
	settleDelay = 1500 * time.Millisecond
)

// IsBrowserReady reports whether a local browser binary can be resolved.
//
// This replaces IsPlaywrightReady. Checking for a binary is deliberately cheap:
// unlike playwright there is no driver/node runtime to boot and keep warm, so
// "ready" simply means a Chromium/Chrome executable exists on this machine.
func IsBrowserReady() bool {
	if !sharedbrowser.Available() {
		log.Printf("browser: IsBrowserReady=false: no local Chrome/Chromium found (override with %s)", sharedbrowser.BinEnv)
		return false
	}
	return true
}

// RunLogin launches a visible browser for the user to authenticate.
// It blocks until authentication is detected (up to 5 minutes) then returns the
// captured AuthResult. Run this in a goroutine to avoid blocking the caller.
func RunLogin(providerID string) (*types.AuthResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}
	if !sharedbrowser.Available() {
		return nil, fmt.Errorf("%w (override with %s)", sharedbrowser.ErrNoBrowser, sharedbrowser.BinEnv)
	}

	// Headed: the whole point is that a human completes the login.
	b, err := sharedbrowser.Launch(sharedbrowser.Options{Headless: false})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer closeBrowser(b)

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	page = page.Timeout(pageTimeout)

	stopBearerCapture, bearer := captureBearer(page, providerID)
	defer stopBearerCapture()

	// Navigate to the provider's login URL so the page is on the right domain.
	if err := navigateSettled(page, cfg.LoginURL); err != nil {
		log.Printf("browser: initial navigation to %s failed: %v", cfg.LoginURL, err)
	}

	return pollForAuth(page, cfg, providerID, bearer)
}

// pollForAuth waits for provider-specific auth evidence to appear, then builds
// the AuthResult. Mirrors the previous playwright implementation exactly.
func pollForAuth(page *rod.Page, cfg *types.ProviderConfig, providerID string, bearer *bearerCapture) (*types.AuthResult, error) {
	deadline := time.Now().Add(loginTimeout)
	for time.Now().Before(deadline) {
		cookies, err := currentCookies(page, cfg.LoginURL)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		cookieMap := map[string]string{}
		for _, c := range cookies {
			cookieMap[c.Name] = c.Value
		}

		authComplete := detectAuth(providerID, pageEvaluator(page), cookieMap, cfg, bearer.get())
		if !authComplete {
			time.Sleep(pollInterval)
			continue
		}

		var parts []string
		for _, c := range cookies {
			parts = append(parts, c.Name+"="+c.Value)
		}
		cookieStr := strings.Join(parts, "; ")

		uaStr := ""
		if v, err := evalAny(page, "() => navigator.userAgent"); err == nil {
			if s, ok := v.(string); ok {
				uaStr = s
			}
		}

		extra := map[string]string{}
		accessToken := ""
		captured := bearer.get()

		switch providerID {
		case "chatgpt":
			// Python: access_token = bearer (from /api/auth/session) or session cookie
			if captured != "" {
				accessToken = captured
			} else {
				accessToken = cookieMap["__Secure-next-auth.session-token"]
			}

		case "deepseek":
			if v := cookieMap["ds_access_token"]; v != "" {
				extra["access_token"] = v
			}

		case "claude":
			if v := cookieMap["claude_ai_org"]; v != "" {
				extra["org_id"] = v
			}
			deviceID := ""
			for name, val := range cookieMap {
				if strings.Contains(strings.ToLower(name), "anthropic-device-id") {
					deviceID = val
					break
				}
			}
			if deviceID == "" {
				if v, e := evalAny(page, "() => { try { const m=document.cookie.match(/anthropic-device-id=([^;]+)/); return m?m[1]:'' } catch{return ''} }"); e == nil {
					if s, ok := v.(string); ok {
						deviceID = s
					}
				}
			}
			if deviceID != "" {
				extra["device_id"] = deviceID
			}
			// sessionKey → AccessToken
			for name, val := range cookieMap {
				if strings.Contains(strings.ToLower(name), "sessionkey") {
					accessToken = val
				}
			}

		case "doubao":
			extra["sessionid"] = cookieMap["sessionid"]
			extra["ttwid"] = cookieMap["ttwid"]

		case "glm", "glm_intl":
			// Python: extra = {"device_id": uuid.uuid4().hex} — always fresh random hex
			extra["device_id"] = generateHex16()
			// refresh_token → AccessToken (Python: result.access_token = refresh_token)
			if v := cookieMap["chatglm_refresh_token"]; v != "" {
				accessToken = v
			}

		case "kimi":
			// access_token from cookie or localStorage
			accessToken = cookieMap["access_token"]
			if accessToken == "" {
				if v, e := evalAny(page, "() => localStorage.getItem('access_token') || ''"); e == nil {
					if s, ok := v.(string); ok {
						accessToken = s
					}
				}
			}
			extra["access_token"] = accessToken

		case "qwen", "qwen_cn":
			extra["xsrf_token"] = cookieMap["XSRF-TOKEN"]
			extra["ut"] = cookieMap["b-user-id"]
			// Python: access_token = captured_token or session cookie value
			if captured != "" {
				accessToken = captured
			} else {
				for name, val := range cookieMap {
					if strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") {
						accessToken = val
						break
					}
				}
			}
		}

		result := &types.AuthResult{
			Success:     true,
			ProviderID:  providerID,
			Cookie:      cookieStr,
			Bearer:      captured,
			AccessToken: accessToken,
			UserAgent:   uaStr,
			Extra:       extra,
		}
		log.Printf("browser: login complete for %s (cookie_len=%d bearer_len=%d access_token_len=%d)",
			providerID, len(cookieStr), len(captured), len(accessToken))
		return result, nil
	}

	return nil, fmt.Errorf("login timeout: user did not complete login within %v", loginTimeout)
}

// pageEval runs page JS and returns the result as a plain Go value. It is the
// narrow surface detectAuth needs, which lets the provider matrix below be tested
// without launching a browser.
type pageEval func(js string) (interface{}, error)

// evalAny runs page JS with optional arguments and returns a plain Go value.
// The provider scripts are delivered as functions and receive their arguments at
// position 0, matching the previous playwright call signature.
func evalAny(page *rod.Page, js string, args ...interface{}) (interface{}, error) {
	obj, err := page.Eval(sharedbrowser.WrapFunc(js), args...)
	if err != nil {
		return nil, err
	}
	return sharedbrowser.DecodeValue(obj.Value), nil
}

// pageEvaluator binds evalAny to a concrete page.
func pageEvaluator(page *rod.Page) pageEval {
	return func(js string) (interface{}, error) { return evalAny(page, js) }
}

// detectAuth implements the per-provider auth-detection rules ported from the
// previous playwright implementation (which mirrored the Python versions).
//
// eval may be nil for providers whose rule is purely cookie-based; providers that
// need to inspect localStorage/fetch simply receive an error and report false,
// which keeps the polling loop waiting rather than misreporting success.
func detectAuth(providerID string, eval pageEval, cookieMap map[string]string, cfg *types.ProviderConfig, capturedBearer string) bool {
	switch providerID {
	case "deepseek":
		// Python: captured_bearer AND (has ds_session_id OR len(cookies)>3)
		hasSession := cookieMap["ds_session_id"] != "" || cookieMap["d_id"] != ""
		return capturedBearer != "" && (hasSession || len(cookieMap) > 3)

	case "grok":
		// Python: sso OR _ga OR (auth_token/twid/ct0)
		return cookieMap["sso"] != "" || cookieMap["_ga"] != "" ||
			cookieMap["auth_token"] != "" || cookieMap["twid"] != "" || cookieMap["ct0"] != ""

	case "glm", "glm_intl":
		// Python: chatglm_refresh_token cookie
		return cookieMap["chatglm_refresh_token"] != ""

	case "kimi":
		// Python: access_token in cookie OR localStorage
		hasToken := cookieMap["access_token"] != ""
		if !hasToken && eval != nil {
			if v, e := eval("() => !!localStorage.getItem('access_token')"); e == nil {
				hasToken, _ = v.(bool)
			}
		}
		return hasToken

	case "claude":
		// Python: session cookie OR len(cookies)>5
		hasSession := false
		for name := range cookieMap {
			if strings.Contains(strings.ToLower(name), "session") {
				hasSession = true
				break
			}
		}
		return hasSession || len(cookieMap) > 5

	case "qwen":
		// Python: (has_auth OR captured_token) AND len(cookies)>2
		hasAuth := false
		for name := range cookieMap {
			if strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") {
				hasAuth = true
				break
			}
		}
		return (hasAuth || capturedBearer != "") && len(cookieMap) > 2

	case "doubao":
		// Python: has sessionid/token/auth_token/csrf_token OR (len>3 AND "sessionid"/"token" in cookie)
		hasAuth := cookieMap["sessionid"] != "" || cookieMap["token"] != "" ||
			cookieMap["auth_token"] != "" || cookieMap["csrf_token"] != ""
		if !hasAuth && len(cookieMap) > 3 {
			cs := strings.Join(cookieNames(cookieMap), " ")
			hasAuth = strings.Contains(cs, "sessionid") || strings.Contains(cs, "token")
		}
		return hasAuth

	case "chatgpt":
		// Python: __Secure-next-auth.session-token OR fetch /api/auth/session accessToken
		if cookieMap["__Secure-next-auth.session-token"] != "" {
			return true
		}
		if eval == nil {
			return false
		}
		v, err := eval("async()=>{try{const r=await fetch('https://chatgpt.com/api/auth/session',{credentials:'include'});return r.ok?(await r.json()).accessToken||'':''}catch{return''}}")
		if err != nil {
			return false
		}
		s, ok := v.(string)
		return ok && s != ""

	default:
		// Generic: check AuthCookies list
		for _, name := range cfg.AuthCookies {
			if cookieMap[name] != "" {
				return true
			}
		}
		return false
	}
}

func cookieNames(m map[string]string) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

// bearerCapture holds the first Authorization: Bearer token observed leaving the
// page. Several providers (DeepSeek, Qwen) only expose their token in flight.
type bearerCapture struct {
	mu    sync.Mutex
	token string
	stop  []func()
}

func (b *bearerCapture) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token
}

func (b *bearerCapture) set(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token == "" {
		b.token = token
		log.Printf("browser: captured bearer (len=%d)", len(token))
	}
}

// captureBearer observes outgoing request headers without intercepting them.
//
// We listen to Network.requestWillBeSent rather than hijacking requests: this is
// purely observational, so a bug here can never stall the user's login page.
func captureBearer(page *rod.Page, providerID string) (func(), *bearerCapture) {
	bc := &bearerCapture{}
	disable := page.EnableDomain(&proto.NetworkEnable{})
	go page.EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		auth := headerOf(e.Request.Headers, "authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return
		}
		// Only capture tokens the provider actually owns.
		if !relevantURL(providerID, e.Request.URL) {
			return
		}
		bc.set(strings.TrimPrefix(auth, "Bearer "))
	})()
	return func() {
		if disable != nil {
			disable()
		}
	}, bc
}

// relevantURL filters captured Bearer tokens by provider-specific URL prefixes.
func relevantURL(providerID, u string) bool {
	switch providerID {
	case "deepseek":
		return strings.Contains(u, "/api/v0/")
	case "qwen", "qwen_cn":
		return strings.Contains(u, "qwen.ai") || strings.Contains(u, "qianwen.com")
	default:
		return true
	}
}

// headerOf looks a header up case-insensitively — CDP reports lowercase names,
// but we don't want a casing change to silently drop a bearer token.
func headerOf(h proto.NetworkHeaders, name string) string {
	if h == nil {
		return ""
	}
	lower := strings.ToLower(name)
	for k, v := range h {
		if strings.ToLower(k) == lower {
			return v.Str()
		}
	}
	return ""
}

// currentCookies returns cookies applicable to the login URL and to wherever the
// user currently is (OAuth flows often hop across domains).
func currentCookies(page *rod.Page, loginURL string) ([]*proto.NetworkCookie, error) {
	urls := []string{loginURL}
	if info, err := page.Info(); err == nil && info.URL != "" && !strings.HasPrefix(info.URL, "about:") {
		urls = append(urls, info.URL)
	}
	return page.Cookies(urls)
}

// navigateSettled navigates and waits for the document to settle enough that
// cookies/challenges have been applied before we inspect page state.
func navigateSettled(page *rod.Page, url string) error {
	if err := page.Navigate(url); err != nil {
		return err
	}
	// Playwright's WaitUntil=domcontentloaded equivalent: wait for the frame's
	// lifecycle events, then give the page a moment to run its own JS.
	if err := page.WaitDOMStable(500*time.Millisecond, 0); err != nil {
		return err
	}
	time.Sleep(settleDelay)
	return nil
}

// closeBrowser shuts the browser down without ever panicking on teardown.
func closeBrowser(b *rod.Browser) {
	if b == nil {
		return
	}
	if err := b.Close(); err != nil {
		log.Printf("browser: close: %v", err)
	}
}

// generateHex16 returns a 32-char cryptographically random hex string.
// Matches Python's uuid.uuid4().hex.
func generateHex16() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: mix time-based entropy
		ns := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ns>>uint(i*8)) ^ byte(i*37+13)
		}
	}
	return fmt.Sprintf("%x", b)
}
