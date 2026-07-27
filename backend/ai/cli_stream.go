package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type cliStreamState struct {
	service     *Service
	provider    string
	final       string
	lastThought string
	toolNames   map[string]string
}

func (s *Service) runClaudeStream(cmd *exec.Cmd) (string, error) {
	state := &cliStreamState{service: s, provider: "Claude Code", toolNames: make(map[string]string)}
	s.emitModelStream("start", state.provider, "")
	result, err := runCLIJSONStream(cmd, state.consumeClaude)
	s.emitModelStream("end", state.provider, "")
	return result, err
}

func (s *Service) runCodexStream(cmd *exec.Cmd) (string, error) {
	state := &cliStreamState{service: s, provider: "Codex", toolNames: make(map[string]string)}
	s.emitModelStream("start", state.provider, "")
	result, err := runCLIJSONStream(cmd, state.consumeCodex)
	s.emitModelStream("end", state.provider, "")
	return result, err
}

func runCLIJSONStream(cmd *exec.Cmd, consume func(map[string]interface{})) (string, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("创建 CLI 输出管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("创建 CLI 错误管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 CLI 失败: %w", err)
	}

	var stderrOutput bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrOutput, stderr)
		close(stderrDone)
	}()

	var rawOutput strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		rawOutput.WriteString(line)
		rawOutput.WriteByte('\n')
		var event map[string]interface{}
		if json.Unmarshal([]byte(line), &event) == nil {
			consume(event)
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	<-stderrDone
	if scanErr != nil {
		return "", fmt.Errorf("读取 CLI 流失败: %w", scanErr)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderrOutput.String())
		if detail == "" {
			detail = rawOutput.String()
		}
		return "", fmt.Errorf("%w: %s", waitErr, truncateString(detail, 1200))
	}

	// Both consumers store the final response in their closure state. It is
	// returned through the synthetic final event appended by consumeFinal.
	var finalEvent map[string]interface{}
	for _, line := range strings.Split(rawOutput.String(), "\n") {
		if json.Unmarshal([]byte(line), &finalEvent) != nil {
			continue
		}
		if result := cliFinalText(finalEvent); result != "" {
			return result, nil
		}
	}
	return "", nil
}

func (s *cliStreamState) consumeClaude(event map[string]interface{}) {
	eventType := stringValue(event["type"])
	if eventType == "stream_event" {
		streamEvent := mapValue(event["event"])
		delta := mapValue(streamEvent["delta"])
		if stringValue(delta["type"]) == "text_delta" {
			s.service.emitModelStream("token", s.provider, stringValue(delta["text"]))
		}
		return
	}
	if eventType == "result" {
		s.final = stringValue(event["result"])
		return
	}
	message := mapValue(event["message"])
	role := stringValue(message["role"])
	content := sliceValue(message["content"])
	if role == "assistant" {
		var preToolText []string
		for _, rawBlock := range content {
			block := mapValue(rawBlock)
			switch stringValue(block["type"]) {
			case "thinking":
				s.emitThought(stringValue(block["thinking"]))
			case "text":
				if text := strings.TrimSpace(stringValue(block["text"])); text != "" {
					preToolText = append(preToolText, text)
				}
			case "tool_use":
				if len(preToolText) > 0 {
					s.emitThought(strings.Join(preToolText, "\n"))
					preToolText = nil
				}
				id := stringValue(block["id"])
				name := normalizeCLIToolName(stringValue(block["name"]))
				args := mapValue(block["input"])
				s.toolNames[id] = name
				s.emitToolDecision(name, args)
				s.service.emitProgress(0, "cli", name, args, "", false, "")
			}
		}
		return
	}
	if role != "user" {
		return
	}
	for _, rawBlock := range content {
		block := mapValue(rawBlock)
		if stringValue(block["type"]) != "tool_result" {
			continue
		}
		name := s.toolNames[stringValue(block["tool_use_id"])]
		if name == "" {
			name = "tool"
		}
		output := truncateString(claudeToolResultText(block["content"]), 8000)
		s.service.emitProgress(0, "cli", name, nil, output, false, "")
	}
}

func (s *cliStreamState) consumeCodex(event map[string]interface{}) {
	item := mapValue(event["item"])
	itemType := stringValue(item["type"])
	if itemType == "" {
		return
	}
	completed := strings.HasSuffix(stringValue(event["type"]), ".completed")
	switch itemType {
	case "reasoning":
		if completed {
			s.emitThought(firstString(item, "text", "summary"))
		}
	case "command_execution":
		name := "bash"
		command := firstString(item, "command", "cmd")
		if !completed {
			args := map[string]interface{}{"command": command}
			s.emitToolDecision(name, args)
			s.service.emitProgress(0, "cli", name, args, "", false, "")
		} else {
			s.service.emitProgress(0, "cli", name, nil, truncateString(firstString(item, "aggregated_output", "output"), 8000), false, "")
		}
	case "file_change":
		if completed {
			s.service.emitProgress(0, "cli", "edit_file", map[string]interface{}{"files": codexChangedFiles(item)}, firstString(item, "status"), false, "")
		}
	case "mcp_tool_call", "tool_call":
		name := normalizeCLIToolName(firstString(item, "tool", "name"))
		if !completed {
			args := mapValue(item["arguments"])
			s.emitToolDecision(name, args)
			s.service.emitProgress(0, "cli", name, args, "", false, "")
		} else {
			s.service.emitProgress(0, "cli", name, nil, firstString(item, "result", "output", "error"), false, "")
		}
	case "agent_message":
		if completed {
			s.final = firstString(item, "text", "message")
			s.service.emitModelStream("token", s.provider, s.final)
		}
	}
}

func (s *cliStreamState) emitThought(text string) {
	text = strings.TrimSpace(text)
	if text == "" || text == s.lastThought || s.service.App == nil {
		return
	}
	s.lastThought = text
	s.service.App.Event.Emit("ai:reasoning", map[string]string{
		"provider": s.provider,
		"content":  truncateString(text, 4000),
	})
}

func (s *cliStreamState) emitToolDecision(name string, args map[string]interface{}) {
	if summary := cliToolDecisionSummary(name, args); summary != "" {
		s.emitThought(summary)
	}
}

func cliToolDecisionSummary(name string, args map[string]interface{}) string {
	path := firstString(args, "file_path", "path")
	pattern := firstString(args, "pattern", "query", "glob")
	switch normalizeCLIToolName(name) {
	case "read_file":
		if path != "" {
			return fmt.Sprintf("需要确认项目中的实际实现，模型决定读取 %s。", path)
		}
		return "需要确认项目中的实际实现，模型决定读取相关文件。"
	case "glob":
		if pattern != "" {
			return fmt.Sprintf("需要定位相关文件，模型正在按 %s 搜索项目。", pattern)
		}
		return "需要定位相关文件，模型正在搜索项目目录。"
	case "grep":
		if pattern != "" {
			return fmt.Sprintf("需要找到相关实现位置，模型正在搜索 %s。", pattern)
		}
		return "需要找到相关实现位置，模型正在搜索代码。"
	case "bash":
		command := firstString(args, "command", "cmd")
		if command != "" {
			return fmt.Sprintf("需要验证项目状态或结果，模型决定执行命令：%s", truncateString(command, 500))
		}
		return "需要验证项目状态或结果，模型决定执行命令。"
	case "edit_file", "write_file":
		if path != "" {
			return fmt.Sprintf("分析完成后，模型决定修改 %s。", path)
		}
		return "分析完成后，模型决定修改相关项目文件。"
	default:
		return fmt.Sprintf("模型根据当前分析决定调用 %s 获取下一步所需信息。", name)
	}
}

func cliFinalText(event map[string]interface{}) string {
	if stringValue(event["type"]) == "result" {
		return strings.TrimSpace(stringValue(event["result"]))
	}
	if strings.HasSuffix(stringValue(event["type"]), ".completed") {
		item := mapValue(event["item"])
		if stringValue(item["type"]) == "agent_message" {
			return strings.TrimSpace(firstString(item, "text", "message"))
		}
	}
	return ""
}

func normalizeCLIToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "read_file"
	case "write":
		return "write_file"
	case "edit", "multiedit", "apply_patch":
		return "edit_file"
	case "glob":
		return "glob"
	case "grep":
		return "grep"
	case "bash", "shell", "command_execution":
		return "bash"
	default:
		if name == "" {
			return "tool"
		}
		return name
	}
}

func claudeToolResultText(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	var parts []string
	for _, raw := range sliceValue(value) {
		block := mapValue(raw)
		if text := stringValue(block["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func codexChangedFiles(item map[string]interface{}) []string {
	var files []string
	for _, raw := range sliceValue(item["changes"]) {
		change := mapValue(raw)
		if path := firstString(change, "path", "file_path"); path != "" {
			files = append(files, path)
		}
	}
	return files
}

func firstString(value map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if text := stringValue(value[key]); text != "" {
			return text
		}
	}
	return ""
}

func mapValue(value interface{}) map[string]interface{} {
	result, _ := value.(map[string]interface{})
	if result == nil {
		return map[string]interface{}{}
	}
	return result
}

func sliceValue(value interface{}) []interface{} {
	result, _ := value.([]interface{})
	return result
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}
