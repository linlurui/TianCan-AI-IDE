package browser

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	playwright "github.com/playwright-community/playwright-go"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/providers"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

// RunBrowserChat restores session cookies into a headless Chromium page and
// executes the provider's chat JS (from providers.GetChatJS) via page.Evaluate.
func RunBrowserChat(providerID string, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	jsCode, jsArgs, err := providers.GetChatJS(providerID, auth, req)
	if err != nil {
		return nil, fmt.Errorf("GetChatJS: %w", err)
	}

	pw, err := ensurePlaywright()
	if err != nil {
		return nil, fmt.Errorf("playwright init: %w", err)
	}

	headless := true
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

	// Restore auth cookies into the browser context
	if auth.Cookie != "" {
		u, _ := url.Parse(cfg.LoginURL)
		domain := u.Hostname()
		var pwCookies []playwright.OptionalCookie
		for _, part := range strings.Split(auth.Cookie, ";") {
			part = strings.TrimSpace(part)
			idx := strings.Index(part, "=")
			if idx < 0 {
				continue
			}
			name := part[:idx]
			value := part[idx+1:]
			pwCookies = append(pwCookies, playwright.OptionalCookie{
				Name:     name,
				Value:    value,
				Domain:   &domain,
				Path:     playwright.String("/"),
				SameSite: playwright.SameSiteAttributeLax,
			})
		}
		if len(pwCookies) > 0 {
			if err := context.AddCookies(pwCookies); err != nil {
				return nil, fmt.Errorf("add cookies: %w", err)
			}
		}
	}

	// For Bearer-based providers, inject Authorization header
	if auth.Bearer != "" {
		headers := map[string]string{"Authorization": "Bearer " + auth.Bearer}
		if err := context.SetExtraHTTPHeaders(headers); err != nil {
			return nil, fmt.Errorf("set extra headers: %w", err)
		}
	}

	page, err := context.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}

	// Navigate to the provider's login URL so the page is in the right domain
	_, _ = page.Goto(cfg.LoginURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	})
	// Let the page settle so cookies are fully applied
	time.Sleep(1500 * time.Millisecond)

	// Execute the provider's chat JS via page.Evaluate
	result, err := page.Evaluate(jsCode, jsArgs)
	if err != nil {
		return &types.ChatResult{Error: fmt.Sprintf("page.Evaluate: %v", err)}, nil
	}

	// Parse the result — providers return different shapes
	if result == nil {
		return &types.ChatResult{Error: "empty result from page.Evaluate"}, nil
	}

	// Result is typically a map (JS object)
	if m, ok := result.(map[string]interface{}); ok {
		if okVal, _ := m["ok"].(bool); !okVal {
			errMsg, _ := m["error"].(string)
			return &types.ChatResult{Error: fmt.Sprintf("browser chat: %s", errMsg)}, nil
		}
		// Providers that return {ok:true, text: "..."} (ChatGPT, Qwen)
		if text, ok := m["text"].(string); ok && text != "" {
			return &types.ChatResult{Success: true, Content: text}, nil
		}
		// Providers that return {ok:true, data: "<sse stream>"} (Grok, GLM, Qwen-CN)
		if data, ok := m["data"].(string); ok && data != "" {
			return providers.ChatWithBrowser(data, providerID), nil
		}
	}

	// Fallback: try treating the result as a string
	if s, ok := result.(string); ok && s != "" {
		return providers.ChatWithBrowser(s, providerID), nil
	}

	return &types.ChatResult{Error: "unexpected browser chat response format"}, nil
}
