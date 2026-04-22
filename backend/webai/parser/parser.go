package parser

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

func makeID() string {
	return uuid.New().String()[:8]
}

// extractJSONObjects finds all top-level JSON objects in text using brace-depth counting.
func extractJSONObjects(text string) []string {
	var results []string
	inStr := false
	depth := 0
	start := -1
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inStr {
			if ch == '\\' && i+1 < len(text) {
				i++
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				candidate := text[start : i+1]
				var test interface{}
				if json.Unmarshal([]byte(candidate), &test) == nil {
					results = append(results, candidate)
				}
				start = -1
			}
		}
	}
	return results
}

// extractContentBetween finds content between opening and closing delimiters.
func extractContentBetween(text, openTag, closeTag string) []struct {
	FullMatch string
	Inner     string
} {
	var results []struct {
		FullMatch string
		Inner     string
	}
	searchFrom := 0
	for {
		start := strings.Index(text[searchFrom:], openTag)
		if start == -1 {
			break
		}
		start += searchFrom
		innerStart := start + len(openTag)
		end := strings.Index(text[innerStart:], closeTag)
		if end == -1 {
			break
		}
		end += innerStart
		results = append(results, struct {
			FullMatch string
			Inner     string
		}{
			FullMatch: text[start : end+len(closeTag)],
			Inner:     text[innerStart:end],
		})
		searchFrom = end + len(closeTag)
	}
	return results
}

// extractCodeBlocks finds content inside triple-backtick code blocks.
func extractCodeBlocks(text string) []struct {
	FullMatch string
	Lang      string
	Inner     string
} {
	var results []struct {
		FullMatch string
		Lang      string
		Inner     string
	}
	searchFrom := 0
	for {
		start := strings.Index(text[searchFrom:], "```")
		if start == -1 {
			break
		}
		start += searchFrom
		afterOpen := start + 3
		lineEnd := strings.Index(text[afterOpen:], "\n")
		if lineEnd == -1 {
			break
		}
		lang := strings.TrimSpace(text[afterOpen : afterOpen+lineEnd])
		innerStart := afterOpen + lineEnd + 1
		closeIdx := strings.Index(text[innerStart:], "```")
		if closeIdx == -1 {
			break
		}
		closeIdx += innerStart
		results = append(results, struct {
			FullMatch string
			Lang      string
			Inner     string
		}{
			FullMatch: text[start : closeIdx+3],
			Lang:      lang,
			Inner:     text[innerStart:closeIdx],
		})
		searchFrom = closeIdx + 3
	}
	return results
}

// ParseToolCalls extracts all tool calls from response text using multiple strategies.
func ParseToolCalls(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	calls = append(calls, parseXMLTags(text)...)
	calls = append(calls, parseAnthropicBlocks(text)...)
	calls = append(calls, parseCodeBlocksStrategy(text)...)
	calls = append(calls, parseInlineJSON(text)...)
	calls = append(calls, parseBareJSON(text)...)
	calls = append(calls, parseInferredToolCalls(text)...)
	calls = append(calls, parseEmojiToolCalls(text)...)
	calls = append(calls, parseBareToolLabel(text)...)
	calls = append(calls, parseShellCodeBlocks(text)...)

	seen := map[string]bool{}
	var unique []types.ToolCallBlock
	for _, c := range calls {
		key := c.Name + ":" + string(c.Arguments)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, c)
		}
	}
	return unique
}

func extractNameAndArgs(data map[string]interface{}) (string, json.RawMessage) {
	name, _ := data["name"].(string)
	if name == "" {
		if fn, ok := data["function"].(string); ok {
			name = fn
		}
	}
	var argsVal interface{}
	for _, key := range []string{"arguments", "args", "input", "parameters"} {
		if v, ok := data[key]; ok {
			argsVal = v
			break
		}
	}
	if argsVal == nil {
		argsVal = map[string]interface{}{}
	}
	if s, ok := argsVal.(string); ok {
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			argsVal = parsed
		} else {
			argsVal = map[string]interface{}{"raw": s}
		}
	}
	argsBytes, _ := json.Marshal(argsVal)
	return name, json.RawMessage(argsBytes)
}

// parseXMLTags extracts tool calls from XML-style tags using string search + brace-counting.
func parseXMLTags(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock

	// Tag-delimited JSON: <function_call>...</function_call>, <tool_use>...</tool_use>
	tagPairs := []struct{ open, close string }{
		{"<function_call>", "</function_call>"},
		{"<tool_use>", "</tool_use>"},
	}
	for _, tp := range tagPairs {
		for _, match := range extractContentBetween(text, tp.open, tp.close) {
			for _, obj := range extractJSONObjects(match.Inner) {
				var data map[string]interface{}
				if err := json.Unmarshal([]byte(obj), &data); err == nil {
					name, args := extractNameAndArgs(data)
					if name != "" {
						id, _ := data["id"].(string)
						if id == "" {
							id = makeID()
						}
						calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: id, RawText: match.FullMatch})
					}
				}
			}
		}
	}

	// Thinking-marker delimited JSON (Antml/DeepSeek format)
	thinkOpen := string([]byte{0xe2, 0x9c, 0x85})
	thinkClose := string([]byte{0xe2, 0x9c, 0x86})
	for _, match := range extractContentBetween(text, thinkOpen, thinkClose) {
		for _, obj := range extractJSONObjects(match.Inner) {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(obj), &data); err == nil {
				name, args := extractNameAndArgs(data)
				if name != "" {
					calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: match.FullMatch})
				}
			}
		}
	}

	// <invoke name="..."> XML format (DeepSeek tool calls)
	for _, match := range extractContentBetween(text, "<invoke ", "</invoke>") {
		nameIdx := strings.Index(match.FullMatch, "name=\"")
		if nameIdx == -1 {
			nameIdx = strings.Index(match.FullMatch, "name='")
		}
		if nameIdx == -1 {
			continue
		}
		quoteChar := match.FullMatch[nameIdx+5]
		nameStart := nameIdx + 6
		nameEnd := strings.IndexByte(match.FullMatch[nameStart:], quoteChar)
		if nameEnd == -1 {
			continue
		}
		name := match.FullMatch[nameStart : nameStart+nameEnd]
		args := map[string]interface{}{}
		for _, pm := range extractContentBetween(match.Inner, "<parameter ", "</parameter>") {
			pNameIdx := strings.Index(pm.FullMatch, "name=\"")
			if pNameIdx == -1 {
				pNameIdx = strings.Index(pm.FullMatch, "name='")
			}
			if pNameIdx == -1 {
				continue
			}
			pQuoteChar := pm.FullMatch[pNameIdx+5]
			pNameStart := pNameIdx + 6
			pNameEnd := strings.IndexByte(pm.FullMatch[pNameStart:], pQuoteChar)
			if pNameEnd == -1 {
				continue
			}
			k := pm.FullMatch[pNameStart : pNameStart+pNameEnd]
			v := strings.TrimSpace(pm.Inner)
			var parsed interface{}
			if err := json.Unmarshal([]byte(v), &parsed); err == nil {
				args[k] = parsed
			} else {
				args[k] = v
			}
		}
		argsBytes, _ := json.Marshal(args)
		calls = append(calls, types.ToolCallBlock{Name: name, Arguments: json.RawMessage(argsBytes), ID: makeID(), RawText: match.FullMatch})
	}

	return calls
}

// parseAnthropicBlocks extracts tool calls from Anthropic-style XML blocks.
func parseAnthropicBlocks(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	for _, match := range extractContentBetween(text, "<tool_use>", "</tool_use>") {
		nameMatches := extractContentBetween(match.Inner, "<name>", "</name>")
		if len(nameMatches) == 0 {
			continue
		}
		name := strings.TrimSpace(nameMatches[0].Inner)
		inputMatches := extractContentBetween(match.Inner, "<input>", "</input>")
		if len(inputMatches) == 0 {
			continue
		}
		input := strings.TrimSpace(inputMatches[0].Inner)
		var args interface{}
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			args = map[string]interface{}{"raw_input": input}
		}
		argsBytes, _ := json.Marshal(args)
		calls = append(calls, types.ToolCallBlock{Name: name, Arguments: json.RawMessage(argsBytes), ID: makeID(), RawText: match.FullMatch})
	}
	return calls
}

// parseCodeBlocksStrategy extracts tool calls from code blocks using brace-counting.
func parseCodeBlocksStrategy(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	toolLangs := map[string]bool{
		"json": true, "tool_call": true, "function_call": true, "tool": true,
	}
	for _, block := range extractCodeBlocks(text) {
		content := strings.TrimSpace(block.Inner)
		if !toolLangs[block.Lang] && !looksLikeToolCall(content) {
			continue
		}
		for _, obj := range extractJSONObjects(content) {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(obj), &data); err == nil {
				name, args := extractNameAndArgs(data)
				if name != "" {
					calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: block.FullMatch})
				}
			}
		}
	}
	return calls
}

// parseInlineJSON extracts tool calls from inline JSON with type field.
func parseInlineJSON(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	toolTypes := map[string]bool{"tool_call": true, "function_call": true, "tool_use": true}
	for _, obj := range extractJSONObjects(text) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(obj), &data); err != nil {
			continue
		}
		typeVal, _ := data["type"].(string)
		if !toolTypes[typeVal] {
			continue
		}
		name, args := extractNameAndArgs(data)
		if name != "" {
			calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: obj})
		}
	}
	return calls
}

// parseBareJSON extracts tool calls from bare JSON objects with name + args.
func parseBareJSON(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	argKeys := map[string]bool{"arguments": true, "args": true}
	for _, obj := range extractJSONObjects(text) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(obj), &data); err != nil {
			continue
		}
		name, _ := data["name"].(string)
		if name == "" {
			continue
		}
		hasArgs := false
		for k := range argKeys {
			if _, ok := data[k]; ok {
				hasArgs = true
				break
			}
		}
		if !hasArgs {
			continue
		}
		_, args := extractNameAndArgs(data)
		calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: obj})
	}
	return calls
}

// knownToolParams maps known parameter keys to the tool name they imply.
var knownToolParams = []struct {
	keys     []string
	toolName string
}{
	{[]string{"command"}, "bash"},
	{[]string{"path", "content"}, "file_write"},
	{[]string{"path", "old", "new"}, "file_edit"},
	{[]string{"pattern"}, "glob"},
	{[]string{"query"}, "grep"},
}

// inferToolName attempts to infer a tool name from parameter keys.
func inferToolName(data map[string]interface{}) string {
	for _, kp := range knownToolParams {
		allPresent := true
		for _, k := range kp.keys {
			if _, ok := data[k]; !ok {
				allPresent = false
				break
			}
		}
		if allPresent {
			return kp.toolName
		}
	}
	return ""
}

// parseInferredToolCalls handles JSON blocks that contain tool parameters
// but lack an explicit "name" field (common with DeepSeek Web).
func parseInferredToolCalls(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock

	for _, block := range extractCodeBlocks(text) {
		content := strings.TrimSpace(block.Inner)
		for _, obj := range extractJSONObjects(content) {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(obj), &data); err != nil {
				continue
			}
			if _, hasName := data["name"]; hasName {
				continue
			}
			inferredName := inferToolName(data)
			if inferredName == "" {
				continue
			}
			argsBytes, _ := json.Marshal(data)
			calls = append(calls, types.ToolCallBlock{
				Name:      inferredName,
				Arguments: json.RawMessage(argsBytes),
				ID:        makeID(),
				RawText:   block.FullMatch,
			})
		}
	}

	for _, obj := range extractJSONObjects(text) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(obj), &data); err != nil {
			continue
		}
		if _, hasName := data["name"]; hasName {
			continue
		}
		if _, hasCmd := data["command"]; !hasCmd {
			continue
		}
		inferredName := inferToolName(data)
		if inferredName == "" {
			continue
		}
		argsBytes, _ := json.Marshal(data)
		calls = append(calls, types.ToolCallBlock{
			Name:      inferredName,
			Arguments: json.RawMessage(argsBytes),
			ID:        makeID(),
			RawText:   obj,
		})
	}

	return calls
}

func looksLikeToolCall(content string) bool {
	lower := strings.ToLower(content)
	indicators := []string{
		`"type": "tool_call"`, `"type": "function_call"`, `"type": "tool_use"`,
		`"name":`, `"function":`, `"arguments":`, `"args":`, `"tool_call_id":`,
	}
	for _, ind := range indicators {
		if strings.Contains(lower, strings.ToLower(ind)) {
			return true
		}
	}
	return false
}

// parseEmojiToolCalls handles wrench emoji + JSON tool call format.
func parseEmojiToolCalls(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	wrench := string([]byte{0xf0, 0x9f, 0x94, 0xa7})

	searchFrom := 0
	for {
		idx := strings.Index(text[searchFrom:], wrench)
		if idx == -1 {
			break
		}
		idx += searchFrom
		afterWrench := text[idx+len(wrench):]
		trimmed := strings.TrimLeft(afterWrench, " \t\n\r")
		objs := extractJSONObjects(trimmed)
		if len(objs) > 0 {
			for _, obj := range objs {
				var data map[string]interface{}
				if err := json.Unmarshal([]byte(obj), &data); err == nil {
					name, args := extractNameAndArgs(data)
					if name == "" {
						name = inferToolName(data)
					}
					if name != "" {
						jsonEnd := strings.Index(afterWrench, obj) + len(obj)
						rawText := text[idx : idx+len(wrench)+jsonEnd]
						calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: rawText})
					}
				}
			}
		}
		searchFrom = idx + len(wrench)
		if searchFrom >= len(text) {
			break
		}
	}

	for _, block := range extractCodeBlocks(text) {
		blockStart := strings.Index(text, block.FullMatch)
		if blockStart == -1 {
			continue
		}
		prefixStart := blockStart - len(wrench) - 10
		if prefixStart < 0 {
			prefixStart = 0
		}
		prefix := text[prefixStart:blockStart]
		if !strings.Contains(prefix, wrench) {
			continue
		}
		content := strings.TrimSpace(block.Inner)
		for _, obj := range extractJSONObjects(content) {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(obj), &data); err == nil {
				name, args := extractNameAndArgs(data)
				if name == "" {
					name = inferToolName(data)
				}
				if name != "" {
					calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: block.FullMatch})
				}
			}
		}
	}

	return calls
}

// parseShellCodeBlocks treats ```bash/```sh/```shell/```zsh code blocks as bash tool calls.
// DeepSeek sometimes outputs shell commands in fenced code blocks instead of <invoke> format.
func parseShellCodeBlocks(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock
	shellLangs := map[string]bool{
		"bash": true, "sh": true, "shell": true, "zsh": true, "fish": true,
	}
	for _, block := range extractCodeBlocks(text) {
		if !shellLangs[block.Lang] {
			continue
		}
		command := strings.TrimSpace(block.Inner)
		if command == "" {
			continue
		}
		// Build a bash tool call with the command as argument
		args, _ := json.Marshal(map[string]interface{}{"command": command})
		calls = append(calls, types.ToolCallBlock{
			Name:      "bash",
			Arguments: args,
			ID:        makeID(),
			RawText:   block.FullMatch,
		})
	}
	return calls
}

// parseBareToolLabel handles "tool\n{...}" format where the model outputs
// a bare label followed by a JSON object on the next line.
// No hardcoded label list — any single-word line followed by JSON is a candidate.
func parseBareToolLabel(text string) []types.ToolCallBlock {
	var calls []types.ToolCallBlock

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Dynamic detection: single word, no spaces, not a code fence, followed by JSON
		if trimmed == "" || strings.Contains(trimmed, " ") || strings.HasPrefix(trimmed, "```") {
			continue
		}
		if i+1 >= len(lines) {
			continue
		}
		remaining := strings.Join(lines[i+1:], "\n")
		objs := extractJSONObjects(remaining)
		if len(objs) > 0 {
			obj := objs[0]
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(obj), &data); err == nil {
				name, args := extractNameAndArgs(data)
				if name == "" {
					name = inferToolName(data)
				}
				if name != "" {
					rawText := line + "\n" + obj
					calls = append(calls, types.ToolCallBlock{Name: name, Arguments: args, ID: makeID(), RawText: rawText})
				}
			}
		}
	}
	return calls
}
