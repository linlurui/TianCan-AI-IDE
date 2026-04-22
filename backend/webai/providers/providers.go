// Package providers implements login and chat for all 11 zero-token AI providers.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/parser"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/sse"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

// Login opens a browser for the user to authenticate and captures auth tokens.
// This requires playwright-go to be installed.
func Login(providerID string, headless bool, timeout int) (*types.AuthResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	// Note: Full Playwright integration requires playwright-go dependency.
	// This provides the login flow structure; actual browser automation
	// uses the playwright-community/playwright-go package.
	//
	// Usage pattern:
	//   pw, _ := playwright.Run()
	//   browser, _ := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: &headless})
	//   context, _ := browser.NewContext(...)
	//   page, _ := context.NewPage()
	//   page.Goto(cfg.LoginURL)
	//   ... poll cookies until auth detected ...

	result := &types.AuthResult{
		ProviderID: providerID,
		Extra:      map[string]string{},
	}

	// The login flow structure (to be used with playwright-go):
	// 1. Launch browser, navigate to cfg.LoginURL
	// 2. Poll cookies every 2s until cfg.AuthCookies found
	// 3. If cfg.CapturesBearer, listen to request headers
	// 4. Extract provider-specific tokens
	// 5. Return AuthResult

	return result, fmt.Errorf("login requires playwright-go runtime; use LoginWithBrowser() for full implementation")
}

// LoginWithBrowser performs login using an existing playwright page.
// This is the main entry point when you have a browser context already.
func LoginWithBrowser(providerID string, cookieStr string, userAgent string, extra map[string]string) *types.AuthResult {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return &types.AuthResult{ProviderID: providerID, Error: "unknown provider"}
	}

	result := &types.AuthResult{
		Success:    true,
		ProviderID: providerID,
		Cookie:     cookieStr,
		UserAgent:  userAgent,
		Extra:      extra,
	}

	// Extract provider-specific tokens from cookies
	cookies := sse.ParseCookieString(cookieStr, "")
	for _, c := range cookies {
		switch providerID {
		case "chatgpt":
			if c["name"] == "__Secure-next-auth.session-token" {
				result.AccessToken = c["value"]
			}
		case "claude":
			if strings.Contains(strings.ToLower(c["name"]), "sessionkey") {
				result.AccessToken = c["value"]
			}
			if c["name"] == "claude_ai_org" {
				result.Extra["org_id"] = c["value"]
			}
		case "deepseek":
			if c["name"] == "ds_session_id" || c["name"] == "d_id" {
				// Bearer should be captured from request headers separately
			}
		case "doubao":
			if c["name"] == "sessionid" {
				result.Extra["sessionid"] = c["value"]
			}
			if c["name"] == "ttwid" {
				result.Extra["ttwid"] = c["value"]
			}
		case "glm", "glm_intl":
			if c["name"] == "chatglm_refresh_token" || c["name"] == "refresh_token" {
				result.AccessToken = c["value"]
			}
			// Ensure device_id exists (Python: uuid.uuid4().hex)
			if result.Extra["device_id"] == "" {
				result.Extra["device_id"] = strings.ReplaceAll(uuid.New().String(), "-", "")
			}
		case "kimi":
			if c["name"] == "access_token" {
				result.AccessToken = c["value"]
			}
		case "qwen_cn":
			if c["name"] == "tongyi_sso_ticket" || c["name"] == "login_aliyunid_ticket" {
				// Valid auth
			}
			if c["name"] == "XSRF-TOKEN" {
				result.Extra["xsrf_token"] = c["value"]
			}
			if c["name"] == "b-user-id" {
				result.Extra["ut"] = c["value"]
			}
		}
	}

	return result
}

// Chat sends a message to a provider and returns the response.
func Chat(ctx context.Context, providerID string, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}
	if !auth.IsAuthenticated() {
		return &types.ChatResult{Error: "Not authenticated. Run login first."}, nil
	}

	switch providerID {
	case "deepseek":
		return chatDeepSeek(ctx, auth, req)
	case "claude":
		return chatClaude(ctx, auth, req)
	case "doubao":
		return chatDoubao(ctx, auth, req)
	case "kimi":
		return chatKimi(ctx, auth, req)
	default:
		return &types.ChatResult{Error: fmt.Sprintf("Chat for %s requires browser (page.evaluate). Use ChatWithBrowser() instead.", providerID)}, nil
	}
}

// ChatWithBrowser performs chat using page.evaluate for browser-based providers.
// The jsCode and jsArgs should be obtained from GetChatJS().
func ChatWithBrowser(responseData string, providerID string) *types.ChatResult {
	result := &types.ChatResult{}

	var content string
	switch providerID {
	case "grok":
		content = sse.ParseGrokResponse(responseData)
	case "qwen_cn":
		content = sse.ParseSSEChoices(responseData)
	case "glm", "glm_intl":
		content = sse.ParseSSEGLM(responseData)
	default:
		content = sse.ParseSSEChoices(responseData)
	}

	if content != "" {
		result.Success = true
		result.Content = content
		result.ToolCalls = parser.ParseToolCalls(content)
	} else {
		result.Error = fmt.Sprintf("Empty response from %s", providerID)
	}
	return result
}

// GetChatJS returns the JavaScript code and arguments for browser-based chat.
func GetChatJS(providerID string, auth *types.AuthResult, req types.ChatRequest) (jsCode string, args map[string]interface{}, err error) {
	switch providerID {
	case "gemini":
		return geminiJS, map[string]interface{}{
			"message": req.Message, "timeoutMs": 90000,
		}, nil
	case "chatgpt":
		return chatGPTJS, map[string]interface{}{
			"message": req.Message, "model": req.Model, "convId": req.ConversationID,
		}, nil
	case "grok":
		return grokJS, map[string]interface{}{
			"conversationId": req.ConversationID, "message": req.Message, "timeoutMs": 120000,
		}, nil
	case "qwen":
		return qwenJS, map[string]interface{}{
			"baseUrl": "https://chat.qwen.ai", "chatId": req.ConversationID,
			"model": req.Model, "message": req.Message, "fid": uuid.New().String(),
		}, nil
	case "qwen_cn":
		xsrfToken, _ := auth.Extra["xsrf_token"]
		ut, _ := auth.Extra["ut"]
		deviceID := ut
		if deviceID == "" {
			deviceID = strings.ReplaceAll(uuid.New().String(), "-", "")
		}
		return qwenCNJS, map[string]interface{}{
			"baseUrl": "https://chat2.qianwen.com", "sessionId": req.ConversationID,
			"model": req.Model, "message": req.Message, "ut": ut,
			"xsrfToken": xsrfToken, "deviceId": deviceID,
			"nonce":     uuid.New().String()[:10],
			"timestamp": time.Now().UnixMilli(),
		}, nil
	case "glm", "glm_intl":
		base := "https://chatglm.cn"
		if providerID == "glm_intl" {
			base = "https://chat.z.ai"
		}
		timestamp, nonce, sign := sse.GenerateGLMSign()
		deviceID, _ := auth.Extra["device_id"]
		if deviceID == "" {
			deviceID = strings.ReplaceAll(uuid.New().String(), "-", "")
		}
		// Python: _ASSISTANT_IDS.get(model or "glm-4-plus", _DEFAULT_ASSISTANT_ID)
		glmAssistantIDs := map[string]string{
			"glm-4-plus":  "65940acff94777010aa6b796",
			"glm-4":       "65940acff94777010aa6b796",
			"glm-4-think": "676411c38945bbc58a905d31",
			"glm-4-zero":  "676411c38945bbc58a905d31",
		}
		modelName := req.Model
		if modelName == "" {
			modelName = "glm-4-plus"
		}
		assistantID := glmAssistantIDs[modelName]
		if assistantID == "" {
			assistantID = "65940acff94777010aa6b796"
		}
		body, _ := json.Marshal(map[string]interface{}{
			"assistant_id":    assistantID,
			"conversation_id": req.ConversationID,
			"project_id":      "", "chat_type": "user_chat",
			"meta_data": map[string]interface{}{
				"cogview": map[string]interface{}{"rm_label_watermark": false},
				"is_test": false, "input_question_type": "xxxx", "channel": "",
				"draft_id": "", "chat_mode": "zero", "is_networking": false,
				"quote_log_id": "", "platform": "pc",
			},
			"messages": []map[string]interface{}{
				{"role": "user", "content": []map[string]interface{}{
					{"type": "text", "text": req.Message},
				}},
			},
		})
		return glmJS, map[string]interface{}{
			"accessToken": auth.AccessToken, "bodyStr": string(body),
			"deviceId": deviceID, "requestId": strings.ReplaceAll(uuid.New().String(), "-", ""),
			"sign": map[string]string{"timestamp": timestamp, "nonce": nonce, "sign": sign},
			"xExpGroups": "na_android_config:exp:NA,na_4o_config:exp:4o_A,tts_config:exp:tts_config_a," +
				"na_glm4plus_config:exp:open,mainchat_server_app:exp:A,mobile_history_daycheck:exp:a," +
				"desktop_toolbar:exp:A,chat_drawing_server:exp:A,drawing_server_cogview:exp:cogview4," +
				"app_welcome_v2:exp:A,chat_drawing_streamv2:exp:A,mainchat_rm_fc:exp:add," +
				"mainchat_dr:exp:open,chat_auto_entrance:exp:A,drawing_server_hi_dream:control:A," +
				"homepage_square:exp:close,assistant_recommend_prompt:exp:3,app_home_regular_user:exp:A," +
				"memory_common:exp:enable,mainchat_moe:exp:300,assistant_greet_user:exp:greet_user," +
				"app_welcome_personalize:exp:A,assistant_model_exp_group:exp:glm4.5," +
				"ai_wallet:exp:ai_wallet_enable",
			"timeoutMs": 120000, "baseUrl": base,
		}, nil
	default:
		return "", nil, fmt.Errorf("no browser chat JS for provider: %s", providerID)
	}
}

// --- Direct HTTP chat implementations ---

func chatDeepSeek(ctx context.Context, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	base := "https://chat.deepseek.com"
	result := &types.ChatResult{ConversationID: req.ConversationID}

	client := &http.Client{Timeout: 120 * time.Second}

	// 0. Init — fetch client settings (non-critical)
	initReq, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v0/client/settings?did=&scope=banner", nil)
	setDeepSeekHeaders(initReq, auth)
	if resp, err := client.Do(initReq); err == nil {
		resp.Body.Close()
	}

	// 1. Create chat session if needed
	sessionID := req.ConversationID
	if sessionID == "" {
		sessionBody, _ := json.Marshal(map[string]interface{}{})
		sessionReq, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v0/chat_session/create", bytes.NewReader(sessionBody))
		setDeepSeekHeaders(sessionReq, auth)
		resp, err := client.Do(sessionReq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			log.Printf("chatDeepSeek: session create status=%d body=%s", resp.StatusCode, string(body))
			return nil, fmt.Errorf("session create failed: status %d", resp.StatusCode)
		}

		var sessionData map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&sessionData)
		if data, ok := sessionData["data"].(map[string]interface{}); ok {
			if biz, ok := data["biz_data"].(map[string]interface{}); ok {
				if id, ok := biz["id"].(string); ok {
					sessionID = id
				}
				if id, ok := biz["chat_session_id"].(string); ok {
					sessionID = id
				}
			}
		}
	}

	// 2. Create & solve PoW challenge
	powHeader := ""
	targetPath := "/api/v0/chat/completion"
	powBody, _ := json.Marshal(map[string]interface{}{"target_path": targetPath})
	powReq, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v0/chat/create_pow_challenge", bytes.NewReader(powBody))
	setDeepSeekHeaders(powReq, auth)
	powResp, err := client.Do(powReq)
	if err != nil {
		log.Printf("chatDeepSeek: PoW challenge request failed: %v", err)
	} else {
		defer powResp.Body.Close()
		if powResp.StatusCode == 200 {
			var powData map[string]interface{}
			json.NewDecoder(powResp.Body).Decode(&powData)
			// Python: data.get("data",{}).get("biz_data",{}).get("challenge") or data.get("challenge")
			var ch map[string]interface{}
			if d, ok := powData["data"].(map[string]interface{}); ok {
				if biz, ok := d["biz_data"].(map[string]interface{}); ok {
					ch, _ = biz["challenge"].(map[string]interface{})
				}
			}
			if ch == nil {
				ch, _ = powData["challenge"].(map[string]interface{})
			}
			if ch != nil {
				powHeader, err = solvePoW(ch, targetPath)
				if err != nil {
					log.Printf("chatDeepSeek: PoW solve failed: %v", err)
				} else {
					log.Printf("chatDeepSeek: PoW solved, answer in header")
				}
			} else {
				log.Printf("chatDeepSeek: PoW challenge not found in response: %v", powData)
			}
		} else {
			body, _ := io.ReadAll(powResp.Body)
			log.Printf("chatDeepSeek: PoW challenge status=%d body=%s", powResp.StatusCode, string(body[:min(len(body), 200)]))
		}
	}

	// 3. Chat completion with PoW header
	model := req.Model
	if model == "" {
		model = "deepseek-reasoner"
	}
	thinking := model != "deepseek-chat"
	chatBody, _ := json.Marshal(map[string]interface{}{
		"chat_session_id": sessionID, "parent_message_id": nil,
		"prompt": req.Message, "ref_file_ids": []interface{}{},
		"thinking_enabled": thinking, "search_enabled": true,
	})
	chatReq, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v0/chat/completion", bytes.NewReader(chatBody))
	setDeepSeekHeaders(chatReq, auth)
	if powHeader != "" {
		chatReq.Header.Set("x-ds-pow-response", powHeader)
	}

	resp, err := client.Do(chatReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	log.Printf("chatDeepSeek: completion status=%d body_len=%d", resp.StatusCode, len(bodyBytes))

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("chat completion failed: status %d, body: %s", resp.StatusCode, string(bodyBytes[:min(len(bodyBytes), 300)]))
	}

	content := sse.ParseSSEDeepSeek(string(bodyBytes))

	if content != "" {
		result.Success = true
		result.Content = content
		result.ConversationID = sessionID
		result.ToolCalls = parser.ParseToolCalls(content)
	} else {
		result.Error = "Empty response from DeepSeek"
	}
	return result, nil
}

func chatClaude(ctx context.Context, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	base := "https://claude.ai/api"
	result := &types.ChatResult{ConversationID: req.ConversationID}

	client := &http.Client{Timeout: 120 * time.Second}

	// Discover org
	orgID, _ := auth.Extra["org_id"]
	if orgID == "" {
		orgReq, _ := http.NewRequestWithContext(ctx, "GET", base+"/organizations", nil)
		setClaudeHeaders(orgReq, auth)
		resp, err := client.Do(orgReq)
		if err == nil {
			var orgs []map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&orgs)
			resp.Body.Close()
			if len(orgs) > 0 {
				orgID, _ = orgs[0]["uuid"].(string)
			}
		}
	}

	// Create conversation
	convID := req.ConversationID
	if convID == "" {
		url := base + "/chat_conversations"
		if orgID != "" {
			url = fmt.Sprintf("%s/organizations/%s/chat_conversations", base, orgID)
		}
		convBody, _ := json.Marshal(map[string]interface{}{
			"name": fmt.Sprintf("Conversation %s", time.Now().Format("2006-01-02T15:04:05")),
			"uuid": uuid.New().String(),
		})
		convReq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(convBody))
		setClaudeHeaders(convReq, auth)
		resp, err := client.Do(convReq)
		if err == nil {
			var data map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&data)
			resp.Body.Close()
			convID, _ = data["uuid"].(string)
		}
	}

	// Send completion
	url := fmt.Sprintf("%s/chat_conversations/%s/completion", base, convID)
	if orgID != "" {
		url = fmt.Sprintf("%s/organizations/%s/chat_conversations/%s/completion", base, orgID, convID)
	}
	model := req.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	compBody, _ := json.Marshal(map[string]interface{}{
		"prompt":              req.Message,
		"parent_message_uuid": "00000000-0000-4000-8000-000000000000",
		"model":               model, "timezone": "Asia/Shanghai",
		"rendering_mode": "messages", "attachments": []interface{}{},
		"files": []interface{}{}, "locale": "en-US",
		"personalized_styles": []interface{}{}, "sync_sources": []interface{}{},
		"tools": []interface{}{},
	})
	compReq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(compBody))
	setClaudeHeaders(compReq, auth)

	resp, err := client.Do(compReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	content := sse.ParseSSEClaude(string(bodyBytes))

	if content != "" {
		result.Success = true
		result.Content = content
		result.ConversationID = convID
		result.ToolCalls = parser.ParseToolCalls(content)
	} else {
		result.Error = "Empty response from Claude"
	}
	return result, nil
}

func chatDoubao(ctx context.Context, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	base := "https://www.doubao.com"
	result := &types.ChatResult{}

	client := &http.Client{Timeout: 120 * time.Second}

	url := fmt.Sprintf("%s/samantha/chat/completion?%s", base, sse.BuildDoubaoQueryParams())
	textMsg := fmt.Sprintf("<|im_start|>user\n%s\n<|im_end|>\n", req.Message)
	contentJSON, _ := json.Marshal(map[string]string{"text": textMsg})
	ts := time.Now().UnixMilli()
	tsStr := fmt.Sprintf("%d", ts)
	localConvID := "local_16" + tsStr[len(tsStr)-14:]
	msgID := uuid.New().String()

	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]interface{}{
			{"content": string(contentJSON), "content_type": 2001, "attachments": []interface{}{}, "references": []interface{}{}},
		},
		"completion_option": map[string]interface{}{
			"is_regen": false, "with_suggest": true, "need_create_conversation": true,
			"launch_stage": 1, "is_replace": false, "is_delete": false,
			"message_from": 0, "event_id": "0",
		},
		"conversation_id": "0", "local_conversation_id": localConvID,
		"local_message_id": msgID,
	})

	httpReq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	setDoubaoHeaders(httpReq, auth)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	content := sse.ParseSSEDoubao(string(bodyBytes))

	if content != "" {
		result.Success = true
		result.Content = content
		result.ToolCalls = parser.ParseToolCalls(content)
	} else {
		result.Error = "Empty response from Doubao"
	}
	return result, nil
}

func chatKimi(ctx context.Context, auth *types.AuthResult, req types.ChatRequest) (*types.ChatResult, error) {
	base := "https://www.kimi.com"
	result := &types.ChatResult{}

	client := &http.Client{Timeout: 120 * time.Second}

	model := req.Model
	scenario := "SCENARIO_K2"
	if strings.Contains(model, "search") {
		scenario = "SCENARIO_SEARCH"
	} else if strings.Contains(model, "research") {
		scenario = "SCENARIO_RESEARCH"
	} else if strings.Contains(model, "k1") {
		scenario = "SCENARIO_K1"
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"scenario": scenario,
		"message": map[string]interface{}{
			"role": "user", "scenario": scenario,
			"blocks": []map[string]interface{}{
				{"message_id": "", "text": map[string]interface{}{"content": req.Message}},
			},
		},
		"options": map[string]interface{}{"thinking": false},
	})

	framed := sse.BuildKimiConnectRPCFrame(reqBody)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", base+"/apiv2/kimi.gateway.chat.v1.ChatService/Chat", bytes.NewReader(framed))
	setKimiHeaders(httpReq, auth)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	content := sse.ParseConnectRPC(bodyBytes)

	if content != "" {
		result.Success = true
		result.Content = content
		result.ToolCalls = parser.ParseToolCalls(content)
	} else {
		result.Error = "Empty response from Kimi"
	}
	return result, nil
}

// --- Header helpers ---

func setDeepSeekHeaders(r *http.Request, auth *types.AuthResult) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "*/*")
	ua := auth.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	}
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Referer", "https://chat.deepseek.com/")
	r.Header.Set("Origin", "https://chat.deepseek.com")
	r.Header.Set("x-client-platform", "web")
	r.Header.Set("x-client-version", "1.7.0")
	r.Header.Set("x-app-version", "20241129.1")
	r.Header.Set("x-client-locale", "zh_CN")
	r.Header.Set("x-client-timezone-offset", "28800")
	if auth.Cookie != "" {
		r.Header.Set("Cookie", auth.Cookie)
	}
	if auth.Bearer != "" {
		r.Header.Set("Authorization", "Bearer "+auth.Bearer)
	}
}

func setClaudeHeaders(r *http.Request, auth *types.AuthResult) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Referer", "https://claude.ai/")
	r.Header.Set("Origin", "https://claude.ai")
	r.Header.Set("anthropic-client-platform", "web_claude_ai")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if auth.UserAgent != "" {
		r.Header.Set("User-Agent", auth.UserAgent)
	}
	if auth.Cookie != "" {
		r.Header.Set("Cookie", auth.Cookie)
	}
	if auth.AccessToken != "" {
		r.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	}
	if deviceID, ok := auth.Extra["device_id"]; ok && deviceID != "" {
		r.Header.Set("anthropic-device-id", deviceID)
	}
}

func setDoubaoHeaders(r *http.Request, auth *types.AuthResult) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Referer", "https://www.doubao.com/chat/")
	r.Header.Set("Origin", "https://www.doubao.com")
	r.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Agw-js-conv", "str")
	if auth.UserAgent != "" {
		r.Header.Set("User-Agent", auth.UserAgent)
	}
	sessionid, _ := auth.Extra["sessionid"]
	ttwid, _ := auth.Extra["ttwid"]
	if sessionid != "" {
		cookie := "sessionid=" + sessionid
		if ttwid != "" {
			cookie += "; ttwid=" + ttwid
		}
		r.Header.Set("Cookie", cookie)
	} else if auth.Cookie != "" {
		r.Header.Set("Cookie", auth.Cookie)
	}
}

func setKimiHeaders(r *http.Request, auth *types.AuthResult) {
	r.Header.Set("Content-Type", "application/connect+json")
	r.Header.Set("Connect-Protocol-Version", "1")
	r.Header.Set("Accept", "*/*")
	r.Header.Set("Origin", "https://www.kimi.com")
	r.Header.Set("Referer", "https://www.kimi.com/")
	r.Header.Set("X-Language", "zh-CN")
	r.Header.Set("X-Msh-Platform", "web")
	if auth.UserAgent != "" {
		r.Header.Set("User-Agent", auth.UserAgent)
	}
	if auth.AccessToken != "" {
		r.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	} else if auth.Cookie != "" {
		r.Header.Set("Cookie", auth.Cookie)
	}
}

// --- JavaScript code for browser-based providers ---

const geminiJS = `async ({message, timeoutMs}) => {
    try {
        const el = document.querySelector('textarea, [contenteditable="true"], input[type="text"]');
        if (!el) return {ok: false, error: 'No input found'};
        el.focus();
        if (el.tagName === 'TEXTAREA' || el.tagName === 'INPUT') {
            el.value = message; el.dispatchEvent(new Event('input', {bubbles: true}));
        } else {
            el.textContent = message; el.dispatchEvent(new Event('input', {bubbles: true}));
        }
        const btn = document.querySelector('button[aria-label*="Send"], button[aria-label*="submit"], .send-button');
        if (btn && !btn.disabled) { btn.click(); }
        else { el.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true})); }
        const deadline = Date.now() + timeoutMs;
        while (Date.now() < deadline) {
            await new Promise(r => setTimeout(r, 2000));
            const els = document.querySelectorAll('.model-response, [data-response], .response-container, .message-content');
            const last = els.length > 0 ? els[els.length - 1] : null;
            if (last && last.textContent.trim()) return {ok: true, text: last.textContent.trim()};
        }
        return {ok: false, error: 'Timeout waiting for Gemini response'};
    } catch (e) { return {ok: false, error: String(e)}; }
}`

const chatGPTJS = `async ({message, model, convId}) => {
    try {
        const sr = await fetch('https://chatgpt.com/api/auth/session', {credentials: 'include'});
        if (!sr.ok) return {ok: false, error: 'Session fetch failed'};
        const s = await sr.json();
        const at = s.accessToken;
        if (!at) return {ok: false, error: 'No access token'};
        const did = s.oaiDeviceId || crypto.randomUUID();
        const body = {
            action: "next",
            messages: [{id: crypto.randomUUID(), author: {role: "user"}, content: {content_type: "text", parts: [message]}}],
            parent_message_id: crypto.randomUUID(), model: model || "auto",
            conversation_id: convId || undefined, force_use_sse: true,
        };
        const r = await fetch("https://chatgpt.com/backend-api/conversation", {
            method: "POST",
            headers: {"Content-Type": "application/json", "Authorization": "Bearer " + at, "oai-device-id": did},
            body: JSON.stringify(body), credentials: "include",
        });
        if (!r.ok) return {ok: false, error: 'API ' + r.status};
        const rd = r.body.getReader(); const dc = new TextDecoder();
        let ft = ""; let lc = "";
        while (true) {
            const {done, value} = await rd.read(); if (done) break;
            ft += dc.decode(value, {stream: true});
            for (const l of ft.split("\n")) {
                if (l.startsWith("data: ") && l !== "data: [DONE]") {
                    try { const d = JSON.parse(l.slice(6)); const p = d.message?.content?.parts; if (p) lc = p.join(""); } catch {}
                }
            }
        }
        return {ok: true, text: lc || null};
    } catch (e) { return {ok: false, error: String(e)}; }
}`

const grokJS = `async ({conversationId, message, timeoutMs}) => {
    try {
        let convId = conversationId || null;
        if (!convId) {
            const m = window.location.pathname.match(/\/c\/([a-f0-9-]{36})/);
            convId = m?.[1] ?? null;
        }
        if (!convId) {
            const listRes = await fetch('https://grok.com/rest/app-chat/conversations?limit=1', {credentials: 'include'});
            if (listRes.ok) { const list = await listRes.json(); convId = list?.conversations?.[0]?.conversationId ?? null; }
        }
        if (!convId) {
            const createRes = await fetch('https://grok.com/rest/app-chat/conversations', {
                method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'include', body: JSON.stringify({}),
            });
            if (createRes.ok) { const data = await createRes.json(); convId = data?.conversationId ?? data?.id ?? null; }
        }
        if (!convId) return {ok: false, error: 'Failed to get conversation ID'};
        const body = {
            message: message, parentResponseId: crypto.randomUUID(), disableSearch: false,
            enableImageGeneration: true, imageAttachments: [], returnImageBytes: false,
            returnRawGrokInXaiRequest: false, fileAttachments: [], enableImageStreaming: true,
            imageGenerationCount: 2, forceConcise: false, toolOverrides: {},
            enableSideBySide: true, sendFinalMetadata: true, isReasoning: false,
            metadata: {request_metadata: {mode: 'auto'}}, disableTextFollowUps: false,
            disableArtifact: false, isFromGrokFiles: false, disableMemory: false,
            forceSideBySide: false, modelMode: 'MODEL_MODE_AUTO', isAsyncChat: false,
            skipCancelCurrentInflightRequests: false, isRegenRequest: false,
            disableSelfHarmShortCircuit: false,
            deviceEnvInfo: {darkModeEnabled: false, devicePixelRatio: 1, screenWidth: 2560, screenHeight: 1440, viewportWidth: 1440, viewportHeight: 719},
        };
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), timeoutMs);
        const res = await fetch('https://grok.com/rest/app-chat/conversations/' + convId + '/responses', {
            method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'include',
            body: JSON.stringify(body), signal: controller.signal,
        });
        clearTimeout(timer);
        if (!res.ok) return {ok: false, error: 'API ' + res.status};
        const rd = res.body.getReader(); const dc = new TextDecoder();
        let full = '';
        while (true) { const {done, value} = await rd.read(); if (done) break; full += dc.decode(value, {stream: true}); }
        return {ok: true, data: full, conversationId: convId};
    } catch (e) { return {ok: false, error: String(e)}; }
}`

const qwenJS = `async ({baseUrl, chatId, model, message, fid}) => {
    try {
        let id = chatId;
        if (!id) {
            const r = await fetch(baseUrl + '/api/v2/chats/new', {
                method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({}),
            });
            if (r.ok) { const d = await r.json(); id = d.data?.id ?? d.chat_id ?? d.id ?? ''; }
        }
        if (!id) return {ok: false, error: 'Failed to create chat session'};
        const url = baseUrl + '/api/v2/chat/completions?chat_id=' + id;
        const body = {
            stream: true, version: "2.1", incremental_output: true, chat_id: id,
            chat_mode: "normal", model: model, parent_id: null,
            messages: [{
                fid: fid, parentId: null, childrenIds: [], role: "user", content: message,
                user_action: "chat", files: [], timestamp: Math.floor(Date.now() / 1000),
                models: [model], chat_type: "t2t",
                feature_config: {thinking_enabled: true, output_schema: "phase"},
            }],
        };
        const r = await fetch(url, {
            method: 'POST', headers: {'Content-Type': 'application/json', 'Accept': 'text/event-stream'},
            body: JSON.stringify(body),
        });
        if (!r.ok) return {ok: false, error: 'API ' + r.status};
        const rd = r.body.getReader(); const dc = new TextDecoder();
        let full = ''; let content = '';
        while (true) {
            const {done, value} = await rd.read(); if (done) break;
            full += dc.decode(value, {stream: true});
            for (const line of full.split('\n')) {
                if (line.startsWith('data: ') && line !== 'data: [DONE]') {
                    try {
                        const d = JSON.parse(line.slice(6));
                        if (d.choices?.[0]?.delta?.content) content += d.choices[0].delta.content;
                        if (d.output?.text) content = d.output.text;
                    } catch {}
                }
            }
            full = '';
        }
        return {ok: true, text: content, conversationId: id};
    } catch (e) { return {ok: false, error: String(e)}; }
}`

const qwenCNJS = `async ({baseUrl, sessionId, model, message, ut, xsrfToken, deviceId, nonce, timestamp}) => {
    try {
        const url = baseUrl + '/api/v2/chat?biz_id=ai_qwen&chat_client=h5&device=pc&fr=pc&pr=qwen&nonce=' + nonce + '&timestamp=' + timestamp + '&ut=' + ut;
        const body = {
            model: model,
            messages: [{content: message, mime_type: "text/plain", meta_data: {ori_query: message}}],
            session_id: sessionId, parent_req_id: "0", deep_search: "0",
            req_id: "req-" + Math.random().toString(36).slice(2),
            scene: "chat", sub_scene: "chat", temporary: false, from: "default",
            scene_param: "first_turn", chat_client: "h5",
            client_tm: timestamp.toString(), protocol_version: "v2", biz_id: "ai_qwen",
        };
        const res = await fetch(url, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "Accept": "text/event-stream, text/plain, */*",
                "x-xsrf-token": xsrfToken, "x-deviceid": deviceId, "x-platform": "pc_tongyi",
            },
            body: JSON.stringify(body),
        });
        if (!res.ok) return {ok: false, error: 'API ' + res.status};
        const reader = res.body?.getReader();
        if (!reader) return {ok: false, error: 'No response body'};
        const decoder = new TextDecoder();
        let fullText = '';
        while (true) { const {done, value} = await reader.read(); if (done) break; fullText += decoder.decode(value, {stream: true}); }
        return {ok: true, data: fullText};
    } catch (e) { return {ok: false, error: String(e)}; }
}`

const glmJS = `async ({accessToken, bodyStr, deviceId, requestId, sign, xExpGroups, timeoutMs, baseUrl}) => {
    try {
        // Step 1: Refresh access token (matches Python's _refresh_token)
        let token = accessToken;
        try {
            const rr = await fetch(baseUrl + '/chatglm/user-api/user/refresh', {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                credentials: 'include', body: '{}',
            });
            if (rr.ok) {
                const rd2 = await rr.json();
                const newToken = rd2?.result?.access_token ?? rd2?.result?.accessToken ?? rd2?.accessToken ?? '';
                if (newToken) token = newToken;
            }
        } catch {}

        // Step 2: Send chat
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), timeoutMs);
        const headers = {
            'Content-Type': 'application/json', 'Accept': 'text/event-stream',
            'App-Name': 'chatglm', 'Origin': baseUrl,
            'X-App-Platform': 'pc', 'X-App-Version': '0.0.1', 'X-App-fr': 'default',
            'X-Device-Brand': '', 'X-Device-Id': deviceId, 'X-Device-Model': '',
            'X-Exp-Groups': xExpGroups,
            'X-Lang': baseUrl.includes('z.ai') ? 'en' : 'zh',
            'X-Nonce': sign.nonce, 'X-Request-Id': requestId,
            'X-Sign': sign.sign, 'X-Timestamp': sign.timestamp,
        };
        if (token) headers['Authorization'] = 'Bearer ' + token;
        const res = await fetch(baseUrl + '/chatglm/backend-api/assistant/stream', {
            method: 'POST', headers, credentials: 'include', body: bodyStr, signal: controller.signal,
        });
        clearTimeout(timer);
        if (!res.ok) return {ok: false, error: 'API ' + res.status};
        const rd = res.body.getReader(); const dc = new TextDecoder();
        let full = '';
        while (true) { const {done, value} = await rd.read(); if (done) break; full += dc.decode(value, {stream: true}); }
        return {ok: true, data: full};
    } catch (e) { return {ok: false, error: String(e)}; }
}`
