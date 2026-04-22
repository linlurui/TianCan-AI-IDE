// Package types defines core types for the web-ai-sdk-go.
package types

import "encoding/json"

// AuthResult holds the result of a login attempt.
type AuthResult struct {
	Success     bool            `json:"success"`
	ProviderID  string          `json:"provider_id"`
	Cookie      string          `json:"cookie"`
	AccessToken string          `json:"access_token"`
	Bearer      string          `json:"bearer"`
	UserAgent   string          `json:"user_agent"`
	Error       string          `json:"error"`
	Extra       map[string]string `json:"extra"`
}

// IsAuthenticated checks if we have valid auth credentials.
func (a *AuthResult) IsAuthenticated() bool {
	return a.Cookie != "" || a.AccessToken != "" || a.Bearer != ""
}

// ToolCallBlock represents a parsed tool call from a provider response.
type ToolCallBlock struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	ID        string          `json:"id"`
	RawText   string          `json:"raw_text"`
}

// ChatResult holds the result of a chat completion request.
type ChatResult struct {
	Success        bool            `json:"success"`
	Content        string          `json:"content"`
	ToolCalls      []ToolCallBlock `json:"tool_calls"`
	Model          string          `json:"model"`
	ConversationID string          `json:"conversation_id"`
	Error          string          `json:"error"`
	RawResponse    string          `json:"raw_response"`
}

// ChatRequest is the input for a chat message.
type ChatRequest struct {
	Message        string `json:"message"`
	Model          string `json:"model,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// ProviderConfig holds static metadata for a provider.
type ProviderConfig struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name"`
	LoginURL        string   `json:"login_url"`
	AuthCookies     []string `json:"auth_cookies"`
	UsesBrowserChat bool    `json:"uses_browser_chat"`
	CapturesBearer  bool    `json:"captures_bearer"`
	NeedsXSRF       bool    `json:"needs_xsrf"`
}

// AllProviders returns all 11 provider configurations.
func AllProviders() []ProviderConfig {
	return []ProviderConfig{
		{ID: "chatgpt", DisplayName: "ChatGPT", LoginURL: "https://chatgpt.com/", AuthCookies: []string{"__Secure-next-auth.session-token"}, UsesBrowserChat: true},
		{ID: "claude", DisplayName: "Claude", LoginURL: "https://claude.ai/", AuthCookies: []string{"sessionKey"}, UsesBrowserChat: false},
		{ID: "deepseek", DisplayName: "DeepSeek", LoginURL: "https://chat.deepseek.com/", AuthCookies: []string{"ds_session_id", "d_id"}, CapturesBearer: true},
		{ID: "doubao", DisplayName: "豆包 Doubao", LoginURL: "https://www.doubao.com/chat/", AuthCookies: []string{"sessionid"}},
		{ID: "gemini", DisplayName: "Gemini", LoginURL: "https://gemini.google.com/app", AuthCookies: []string{"SID", "__Secure-1PSID"}, UsesBrowserChat: true},
		{ID: "glm", DisplayName: "智谱清言 ChatGLM", LoginURL: "https://chatglm.cn/", AuthCookies: []string{"chatglm_refresh_token"}, UsesBrowserChat: true},
		{ID: "glm_intl", DisplayName: "ChatGLM International", LoginURL: "https://chat.z.ai/", AuthCookies: []string{"chatglm_refresh_token", "refresh_token", "auth_token", "access_token"}, UsesBrowserChat: true},
		{ID: "grok", DisplayName: "Grok", LoginURL: "https://grok.com/", AuthCookies: []string{"sso", "_ga"}, UsesBrowserChat: true},
		{ID: "kimi", DisplayName: "Kimi", LoginURL: "https://www.kimi.com/", AuthCookies: []string{"access_token"}},
		{ID: "qwen", DisplayName: "Qwen", LoginURL: "https://chat.qwen.ai/", CapturesBearer: true, UsesBrowserChat: true},
		{ID: "qwen_cn", DisplayName: "Qwen 国内版", LoginURL: "https://www.qianwen.com/", AuthCookies: []string{"tongyi_sso_ticket", "login_aliyunid_ticket"}, UsesBrowserChat: true, NeedsXSRF: true},
	}
}

// GetProviderConfig returns the config for a given provider ID.
func GetProviderConfig(id string) *ProviderConfig {
	for _, p := range AllProviders() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}
