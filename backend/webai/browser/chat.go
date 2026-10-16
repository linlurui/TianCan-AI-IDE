package browser

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	sharedbrowser "github.com/linlurui/tiancan-ai-ide/backend/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/providers"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/types"
)

// RunBrowserChat restores session cookies into a headless browser page and
// executes the provider's chat JS (from providers.GetChatJS) via page eval.
//
// This is the rod replacement for the previous playwright implementation. All
// input reaching the page through this path is real CDP input (trusted events);
// we never synthesize DOM events.
func RunBrowserChat(providerID string, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	jsCode, jsArgs, err := providers.GetChatJS(providerID, auth, req)
	if err != nil {
		return nil, fmt.Errorf("GetChatJS: %w", err)
	}

	if !sharedbrowser.Available() {
		return nil, fmt.Errorf("%w (override with %s)", sharedbrowser.ErrNoBrowser, sharedbrowser.BinEnv)
	}

	b, err := sharedbrowser.Launch(sharedbrowser.Options{Headless: true})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer closeBrowser(b)

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	page = page.Timeout(pageTimeout)

	// Restore auth cookies into the page.
	if auth.Cookie != "" {
		if err := page.SetCookies(parseCookies(auth.Cookie, cfg.LoginURL)); err != nil {
			return nil, fmt.Errorf("set cookies: %w", err)
		}
	}

	// For Bearer-based providers, inject Authorization header.
	if auth.Bearer != "" {
		if _, err := page.SetExtraHeaders([]string{"Authorization", "Bearer " + auth.Bearer}); err != nil {
			return nil, fmt.Errorf("set extra headers: %w", err)
		}
	}

	// Navigate to the provider's login URL so the page is in the right domain.
	if err := page.Navigate(cfg.LoginURL); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}
	// Let the page settle so cookies are fully applied.
	if err := page.WaitDOMStable(500*time.Millisecond, 0); err != nil {
		return nil, fmt.Errorf("wait for page: %w", err)
	}
	time.Sleep(settleDelay)

	result, err := evalAny(page, jsCode, jsArgs)
	if err != nil {
		return &types.ChatResult{Error: fmt.Sprintf("page eval: %v", err)}, nil
	}

	return interpretChatResult(result, providerID), nil
}

// parseCookies turns a raw "a=1; b=2" cookie header into the CDP cookie params
// applicable to the login host. Playwright accepted a Cookie object with an
// explicit domain; CDP's Network.setCookies has the same requirement, so we
// derive it from the provider's login URL exactly as before.
func parseCookies(cookieStr, loginURL string) []*proto.NetworkCookieParam {
	u, err := url.Parse(loginURL)
	if err != nil {
		return nil
	}
	domain := u.Hostname()
	if domain == "" {
		return nil
	}
	var out []*proto.NetworkCookieParam
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx < 0 {
			continue
		}
		out = append(out, &proto.NetworkCookieParam{
			Name:   part[:idx],
			Value:  part[idx+1:],
			Domain: domain,
			Path:   "/",
		})
	}
	return out
}

// interpretChatResult parses the JS reply into a ChatResult. Providers return
// different shapes, handled here in the same order as the previous implementation.
func interpretChatResult(result interface{}, providerID string) *types.ChatResult {
	if result == nil {
		return &types.ChatResult{Error: "empty result from page eval"}
	}

	// Result is typically a map (JS object)
	if m, ok := result.(map[string]interface{}); ok {
		if okVal, _ := m["ok"].(bool); !okVal {
			errMsg, _ := m["error"].(string)
			return &types.ChatResult{Error: fmt.Sprintf("browser chat: %s", errMsg)}
		}
		// Providers that return {ok:true, text: "..."} (ChatGPT, Qwen)
		if text, ok := m["text"].(string); ok && text != "" {
			return &types.ChatResult{Success: true, Content: text}
		}
		// Providers that return {ok:true, data: "<sse stream>"} (Grok, GLM, Qwen-CN)
		if data, ok := m["data"].(string); ok && data != "" {
			return providers.ChatWithBrowser(data, providerID)
		}
	}

	// Fallback: try treating the result as a string
	if s, ok := result.(string); ok && s != "" {
		return providers.ChatWithBrowser(s, providerID)
	}

	return &types.ChatResult{Error: "unexpected browser chat response format"}
}
