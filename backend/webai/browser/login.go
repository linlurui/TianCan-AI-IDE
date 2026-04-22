// Package browser provides Playwright-based login and chat for web AI providers
// using the playwright-community/playwright-go package.
package browser

import (
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	playwright "github.com/playwright-community/playwright-go"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

var (
	pwMu        sync.Mutex
	pwInst      *playwright.Playwright
	pwInstalled bool
)

// ensurePlaywright makes sure the Playwright driver is installed and the
// runtime is started. It auto-installs on first call if needed.
func ensurePlaywright() (*playwright.Playwright, error) {
	pwMu.Lock()
	defer pwMu.Unlock()

	if pwInst != nil {
		return pwInst, nil
	}

	// Try starting the driver first
	pw, err := playwright.Run()
	if err != nil {
		log.Printf("browser: playwright.Run() failed: %v, attempting auto-install...", err)
		// Auto-install driver + browsers and retry
		if installErr := playwright.Install(); installErr != nil {
			return nil, fmt.Errorf("playwright auto-install failed: %w (original: %v)", installErr, err)
		}
		pw, err = playwright.Run()
		if err != nil {
			return nil, fmt.Errorf("playwright.Run() failed after install: %w", err)
		}
		log.Printf("browser: playwright driver installed and started successfully")
	}

	pwInst = pw
	pwInstalled = true
	return pwInst, nil
}

// InstallPlaywright installs the Playwright driver and Chromium browser.
func InstallPlaywright() error {
	return playwright.Install()
}

// IsPlaywrightReady checks whether the Playwright driver can be started.
func IsPlaywrightReady() bool {
	_, err := ensurePlaywright()
	if err != nil {
		log.Printf("browser: IsPlaywrightReady=false: %v", err)
		return false
	}
	return true
}

// RunLogin launches a visible Chromium browser for the user to authenticate.
// It blocks until authentication is detected (up to 5 minutes) then returns the
// captured AuthResult. Run this in a goroutine to avoid blocking the caller.
func RunLogin(providerID string) (*types.AuthResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	pw, err := ensurePlaywright()
	if err != nil {
		return nil, fmt.Errorf("playwright init: %w", err)
	}

	headless := false
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: &headless})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer browser.Close()

	context, err := browser.NewContext()
	if err != nil {
		return nil, fmt.Errorf("new context: %w", err)
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}

	// Always capture Bearer tokens from request headers (needed for DeepSeek, Qwen)
	var capturedBearer string
	context.On("request", func(req playwright.Request) {
		hdrs := req.Headers()
		if auth, ok := hdrs["authorization"]; ok {
			if strings.HasPrefix(auth, "Bearer ") && capturedBearer == "" {
				// Filter by provider-relevant URL prefix
				url := req.URL()
				relevant := false
				switch providerID {
				case "deepseek":
					relevant = strings.Contains(url, "/api/v0/")
				case "qwen", "qwen_cn":
					relevant = strings.Contains(url, "qwen.ai") || strings.Contains(url, "qianwen.com")
				default:
					relevant = true
				}
				if relevant {
					capturedBearer = strings.TrimPrefix(auth, "Bearer ")
					log.Printf("browser: captured bearer for %s (len=%d)", providerID, len(capturedBearer))
				}
			}
		}
	})

	// Navigate to login URL
	_, _ = page.Goto(cfg.LoginURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	})

	// Poll cookies every 2s until auth detected or 5-minute timeout
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		cookies, err := context.Cookies()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		cookieMap := map[string]string{}
		for _, c := range cookies {
			cookieMap[c.Name] = c.Value
		}

		// Per-provider auth detection — mirrors Python implementations exactly
		authComplete := false
		switch providerID {
		case "deepseek":
			// Python: captured_bearer AND (has ds_session_id OR len(cookies)>3)
			hasSession := cookieMap["ds_session_id"] != "" || cookieMap["d_id"] != ""
			authComplete = capturedBearer != "" && (hasSession || len(cookies) > 3)
		case "grok":
			// Python: sso OR _ga OR (auth_token/twid/ct0)
			authComplete = cookieMap["sso"] != "" || cookieMap["_ga"] != "" ||
				cookieMap["auth_token"] != "" || cookieMap["twid"] != "" || cookieMap["ct0"] != ""
		case "glm", "glm_intl":
			// Python: chatglm_refresh_token cookie
			authComplete = cookieMap["chatglm_refresh_token"] != ""
		case "kimi":
			// Python: access_token in cookie OR localStorage
			hasToken := cookieMap["access_token"] != ""
			if !hasToken {
				if v, e := page.Evaluate("() => !!localStorage.getItem('access_token')"); e == nil {
					hasToken, _ = v.(bool)
				}
			}
			authComplete = hasToken
		case "claude":
			// Python: session cookie OR len(cookies)>5
			hasSession := false
			for name := range cookieMap {
				if strings.Contains(strings.ToLower(name), "session") {
					hasSession = true
					break
				}
			}
			authComplete = hasSession || len(cookies) > 5
		case "qwen":
			// Python: (has_auth OR captured_token) AND len(cookies)>2
			hasAuth := false
			for name := range cookieMap {
				if strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") {
					hasAuth = true
					break
				}
			}
			authComplete = (hasAuth || capturedBearer != "") && len(cookies) > 2
		case "doubao":
			// Python: has sessionid/token/auth_token/csrf_token OR (len>3 AND "sessionid"/"token" in cookie)
			hasAuth := cookieMap["sessionid"] != "" || cookieMap["token"] != "" ||
				cookieMap["auth_token"] != "" || cookieMap["csrf_token"] != ""
			if !hasAuth && len(cookies) > 3 {
				cs := strings.Join(func() []string {
					s := make([]string, 0, len(cookieMap))
					for k := range cookieMap {
						s = append(s, k)
					}
					return s
				}(), " ")
				hasAuth = strings.Contains(cs, "sessionid") || strings.Contains(cs, "token")
			}
			authComplete = hasAuth
		case "chatgpt":
			// Python: __Secure-next-auth.session-token OR fetch /api/auth/session accessToken
			if cookieMap["__Secure-next-auth.session-token"] != "" {
				authComplete = true
			} else {
				if v, e := page.Evaluate("async()=>{try{const r=await fetch('https://chatgpt.com/api/auth/session',{credentials:'include'});return r.ok?(await r.json()).accessToken||'':''}catch{return''}}"); e == nil {
					if s, ok := v.(string); ok && s != "" {
						capturedBearer = s
						authComplete = true
					}
				}
			}
		default:
			// Generic: check AuthCookies list
			for _, name := range cfg.AuthCookies {
				if cookieMap[name] != "" {
					authComplete = true
					break
				}
			}
		}

		if authComplete {
			var parts []string
			for _, c := range cookies {
				parts = append(parts, c.Name+"="+c.Value)
			}
			cookieStr := strings.Join(parts, "; ")

			userAgent, _ := page.Evaluate("() => navigator.userAgent")
			uaStr := ""
			if s, ok := userAgent.(string); ok {
				uaStr = s
			}

			// Build extra fields per-provider (mirrors Python login extra capture)
			extra := map[string]string{}
			var accessToken string

			switch providerID {
			case "chatgpt":
				// Python: access_token = bearer (from /api/auth/session) or session cookie
				if capturedBearer != "" {
					accessToken = capturedBearer
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
				// device_id from cookie or JS
				deviceID := ""
				for name, val := range cookieMap {
					if strings.Contains(strings.ToLower(name), "anthropic-device-id") {
						deviceID = val
						break
					}
				}
				if deviceID == "" {
					if v, e := page.Evaluate("() => { try { const m=document.cookie.match(/anthropic-device-id=([^;]+)/); return m?m[1]:'' } catch{return ''} }"); e == nil {
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
					if v, e := page.Evaluate("() => localStorage.getItem('access_token') || ''"); e == nil {
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
				if capturedBearer != "" {
					accessToken = capturedBearer
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
				Bearer:      capturedBearer,
				AccessToken: accessToken,
				UserAgent:   uaStr,
				Extra:       extra,
			}
			log.Printf("browser: login complete for %s (cookie_len=%d bearer_len=%d access_token_len=%d)",
				providerID, len(cookieStr), len(capturedBearer), len(accessToken))
			return result, nil
		}
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("login timeout: user did not complete login within 5 minutes")
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
