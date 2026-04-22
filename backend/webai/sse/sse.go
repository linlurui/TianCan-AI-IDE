// Package sse provides SSE stream parsers for each provider's response format.
package sse

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ParseSSEChoices parses generic SSE format with choices[].delta.content.
func ParseSSEChoices(text string) string {
	var content strings.Builder
	for _, line := range strings.Split(text, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			continue
		}
		if choices, ok := d["choices"].([]interface{}); ok {
			for _, c := range choices {
				if cm, ok := c.(map[string]interface{}); ok {
					if delta, ok := cm["delta"].(map[string]interface{}); ok {
						if s, ok := delta["content"].(string); ok {
							content.WriteString(s)
						}
					}
				}
			}
		}
		if s, ok := d["content"].(string); ok {
			content.WriteString(s)
		}
	}
	return content.String()
}

// ParseSSEClaude parses Claude SSE format.
func ParseSSEClaude(text string) string {
	var content strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(line[6:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			continue
		}
		if s, ok := d["completion"].(string); ok {
			content.WriteString(s)
		}
		if delta, ok := d["delta"].(map[string]interface{}); ok {
			if s, ok := delta["text"].(string); ok {
				content.WriteString(s)
			}
		}
	}
	return content.String()
}

// sseRemoveOverlap detects if the start of newContent overlaps with the end
// of existingContent and strips the duplicated prefix from newContent.
// e.g. existing="我们有一个", new="有一个工具" → returns "工具"
func sseRemoveOverlap(existing, newContent string) string {
	if existing == "" || newContent == "" {
		return newContent
	}
	maxOverlap := 100
	if len(existing) < maxOverlap {
		maxOverlap = len(existing)
	}
	if len(newContent) < maxOverlap {
		maxOverlap = len(newContent)
	}
	for n := maxOverlap; n > 0; n-- {
		suffix := existing[len(existing)-n:]
		if strings.HasPrefix(newContent, suffix) {
			return newContent[n:]
		}
	}
	return newContent
}

// ParseSSEDeepSeek parses DeepSeek SSE stream (JSON Patch format).
func ParseSSEDeepSeek(text string) string {
	var thinking, content strings.Builder
	var lastThinkLen, lastContentLen int // track length to detect cumulative (non-delta) fragments
	lastFragType := ""                   // track the type of the last fragment for routing JSON Patch ops
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(line[6:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			continue
		}
		// Check for DeepSeek API error response (PoW failure, auth error, etc.)
		if code, ok := d["code"].(float64); ok && code != 0 {
			if msg, _ := d["msg"].(string); msg != "" {
				return fmt.Sprintf("[DeepSeek Error %d] %s", int(code), msg)
			}
		}
		var resp map[string]interface{}
		if v, ok := d["v"].(map[string]interface{}); ok {
			if r, ok := v["response"].(map[string]interface{}); ok {
				resp = r
			}
		}
		if resp == nil {
			resp, _ = d["response"].(map[string]interface{})
		}
		if resp != nil {
			if fragments, ok := resp["fragments"].([]interface{}); ok {
				for _, f := range fragments {
					frag, _ := f.(map[string]interface{})
					fragType, _ := frag["type"].(string)
					fragContent, _ := frag["content"].(string)
					lastFragType = fragType // track for JSON Patch routing
					switch fragType {
					case "THINK":
						// DeepSeek may send cumulative (full-so-far) or delta fragments.
						// If the new content starts with what we already have, it's cumulative — take only the tail.
						existing := thinking.String()
						if len(fragContent) > len(existing) && strings.HasPrefix(fragContent, existing) {
							thinking.Reset()
							thinking.WriteString(fragContent)
							lastThinkLen = len(fragContent)
						} else if len(fragContent) <= lastThinkLen && strings.HasPrefix(existing, fragContent) {
							// Duplicate or older snapshot — skip entirely
						} else {
							// Delta fragment — may overlap with end of existing content.
							// e.g. existing="我们有一个", delta="有一个工具" → overlap="有一个"
							fragContent = sseRemoveOverlap(existing, fragContent)
							thinking.WriteString(fragContent)
							lastThinkLen = thinking.Len()
						}
					case "RESPONSE":
						existingC := content.String()
						if len(fragContent) > len(existingC) && strings.HasPrefix(fragContent, existingC) {
							content.Reset()
							content.WriteString(fragContent)
							lastContentLen = len(fragContent)
						} else if len(fragContent) <= lastContentLen && strings.HasPrefix(existingC, fragContent) {
							// Duplicate or older snapshot — skip
						} else {
							fragContent = sseRemoveOverlap(existingC, fragContent)
							content.WriteString(fragContent)
							lastContentLen = content.Len()
						}
					}
				}
			}
		}
		// Route JSON Patch operations to the correct builder based on lastFragType.
		// During THINK phase, -1 points to the THINK fragment, so content must go to thinking builder.
		targetBuilder := &content
		if lastFragType == "THINK" {
			targetBuilder = &thinking
		}
		if v, ok := d["v"].(string); ok {
			if _, hasP := d["p"]; !hasP {
				if _, hasO := d["o"]; !hasO {
					targetBuilder.WriteString(v)
				}
			}
		}
		if o, _ := d["o"].(string); o == "APPEND" {
			if v, ok := d["v"].(string); ok {
				targetBuilder.WriteString(v)
			}
		}
		if p, _ := d["p"].(string); strings.HasPrefix(p, "response/fragments/-1/content") {
			if v, ok := d["v"].(string); ok {
				targetBuilder.WriteString(v)
			}
		}
	}
	t, c := thinking.String(), content.String()
	if t != "" && c != "" {
		return "<think>\n" + t + "\n</think>\n\n" + c
	}
	if c != "" {
		return c
	}
	return t
}

// ParseSSEDoubao parses Doubao SSE stream.
func ParseSSEDoubao(text string) string {
	var content strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			continue
		}
		if ev, _ := d["event"].(string); ev == "add_chat" {
			if data, ok := d["data"].(map[string]interface{}); ok {
				if s, ok := data["content"].(string); ok {
					content.WriteString(s)
				}
			}
		}
		if s, ok := d["content"].(string); ok {
			content.WriteString(s)
		}
		if choices, ok := d["choices"].([]interface{}); ok && len(choices) > 0 {
			if c0, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := c0["delta"].(map[string]interface{}); ok {
					if s, ok := delta["content"].(string); ok {
						content.WriteString(s)
					}
				}
			}
		}
	}
	return content.String()
}

// ParseSSEGLM parses ChatGLM SSE stream.
func ParseSSEGLM(text string) string {
	var content strings.Builder
	for _, line := range strings.Split(text, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			continue
		}
		if msg, ok := d["message"].(map[string]interface{}); ok {
			if parts, ok := msg["content"]; ok {
				switch p := parts.(type) {
				case []interface{}:
					for _, part := range p {
						if pm, ok := part.(map[string]interface{}); ok {
							if s, ok := pm["text"].(string); ok {
								content.WriteString(s)
							}
						}
					}
				case string:
					content.WriteString(p)
				}
			}
		}
		if choices, ok := d["choices"].([]interface{}); ok && len(choices) > 0 {
			if c0, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := c0["delta"].(map[string]interface{}); ok {
					if s, ok := delta["content"].(string); ok {
						content.WriteString(s)
					}
				}
			}
		}
	}
	return content.String()
}

// ParseGrokResponse parses Grok streaming response.
func ParseGrokResponse(text string) string {
	var content strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var d map[string]interface{}
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue
		}
		if s, ok := d["token"].(string); ok {
			content.WriteString(s)
		}
		if result, ok := d["result"].(map[string]interface{}); ok {
			if s, ok := result["response"].(string); ok {
				return s
			}
		}
		if s, ok := d["message"].(string); ok {
			content.WriteString(s)
		}
	}
	return content.String()
}

// ParseConnectRPC parses Kimi Connect RPC framed response.
func ParseConnectRPC(data []byte) string {
	var texts []string
	offset := 0
	for offset+5 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset+1 : offset+5]))
		if offset+5+length > len(data) {
			break
		}
		chunk := data[offset+5 : offset+5+length]
		var obj map[string]interface{}
		if err := json.Unmarshal(chunk, &obj); err == nil {
			if _, ok := obj["error"]; ok {
				break
			}
			if block, ok := obj["block"].(map[string]interface{}); ok {
				if txt, ok := block["text"].(map[string]interface{}); ok {
					if s, ok := txt["content"].(string); ok && s != "" {
						// Python: only append if op in ("set", "append", "")
						op, _ := obj["op"].(string)
						if op == "set" || op == "append" || op == "" {
							texts = append(texts, s)
						}
					}
				}
			}
			if _, ok := obj["done"]; ok {
				break
			}
		}
		offset += 5 + length
	}
	return strings.Join(texts, "")
}

// glmSignSecret is the fixed app secret embedded in the GLM web frontend.
// Matches Python's _SIGN_SECRET = "8a1317a7468aa3ad86e997d08f3f31cb"
const glmSignSecret = "8a1317a7468aa3ad86e997d08f3f31cb"

// GenerateGLMSign generates GLM X-Sign headers.
// sign = lowercase_hex(md5(timestamp + "-" + nonce + "-" + glmSignSecret))
func GenerateGLMSign() (timestamp, nonce, sign string) {
	ts := fmt.Sprintf("%d", currentTimeMillis())
	t := len(ts)
	digits := make([]int, t)
	sum := 0
	for i, c := range ts {
		d := int(c - '0')
		digits[i] = d
		sum += d
	}
	i := sum - digits[t-2]
	mod := i % 10
	timestamp = ts[:t-2] + fmt.Sprintf("%d", mod) + ts[t-1:]
	nonce = strings.ReplaceAll(uuid.New().String(), "-", "")
	h := md5.Sum([]byte(timestamp + "-" + nonce + "-" + glmSignSecret))
	sign = fmt.Sprintf("%x", h)
	return
}

func currentTimeMillis() int64 {
	return time.Now().UnixMilli()
}

// ParseCookieString splits "name1=val1; name2=val2" into slice of maps.
func ParseCookieString(cookieStr, domain string) []map[string]string {
	var cookies []map[string]string
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if idx := strings.Index(part, "="); idx > 0 {
			cookies = append(cookies, map[string]string{
				"name":   part[:idx],
				"value":  part[idx+1:],
				"domain": domain,
				"path":   "/",
			})
		}
	}
	return cookies
}

// BuildDoubaoQueryParams returns the default query params for Doubao API.
func BuildDoubaoQueryParams() string {
	return "aid=497858&device_platform=web&language=zh&pkg_type=release_version&real_aid=497858&region=CN&samantha_web=1&sys_region=CN&use_olympus_account=1&version_code=20800"
}

// BuildKimiConnectRPCFrame frames a JSON body with Connect RPC 5-byte header.
func BuildKimiConnectRPCFrame(body json.RawMessage) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x00)
	binary.Write(&buf, binary.BigEndian, uint32(len(body)))
	buf.Write(body)
	return buf.Bytes()
}
