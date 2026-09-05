package agentexec

import (
	"context"
	"maps"
	"slices"
)

type claudeProvider struct{ cfg providerConfig }

// NewClaude returns a Claude Code Provider.
func NewClaude(opts ...Option) Provider { return &claudeProvider{cfg: resolveOptions("claude", opts)} }

func (p *claudeProvider) Name() string { return p.cfg.name }

func (p *claudeProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, Plugins: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *claudeProvider) NewSession() Session {
	return &claudeSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type claudeSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	fallback  Usage
	sawResult bool
	failed    bool
	sessionID string
	summary   string
}

func (s *claudeSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--print", "--output-format", "stream-json", "--verbose"}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--permission-mode", "bypassPermissions")
	}
	if model := s.resolveModel(req); model != "" {
		argv = append(argv, "--model", model)
	}

	// Merge MCP servers: ExtraMCPServers first, then plugin .mcp.json (last wins).
	servers := map[string]any{}
	maps.Copy(servers, req.ExtraMCPServers)
	if len(req.Plugins) > 0 {
		pluginServers, err := loadPluginMCPServers(req.Plugins, req.Env)
		if err != nil {
			return CommandSpec{}, err
		}
		maps.Copy(servers, pluginServers)
	}
	if (len(servers) > 0 || req.NoMCP) && req.WorkspacePath != "" {
		filename := s.cfg.mcpFilename
		if filename == "" {
			filename = ".mcp-config.json"
		}
		// Best-effort: if the merged MCP config can't be written, run without it (matches the real CLI wrapper).
		if mcpPath, err := writeMCPConfig(req.WorkspacePath, filename, servers); err == nil {
			// The path goes before the strict flag on purpose. --mcp-config is
			// variadic: hand it a value with the prompt next in argv and it
			// takes the prompt as a second config path, then fails with a
			// file-not-found naming the entire prompt. A flag after it stops
			// that, and NoMCP would otherwise be the case that hit it, since it
			// has nothing else to append.
			argv = append(argv, "--mcp-config", mcpPath)
			if s.cfg.strictMCP || req.NoMCP {
				argv = append(argv, "--strict-mcp-config")
			}
		}
	}
	for _, p := range req.Plugins {
		if p.Path != "" {
			argv = append(argv, "--plugin-dir", p.Path)
		}
	}

	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	if req.SystemPrompt != "" {
		argv = append(argv, "--append-system-prompt", req.SystemPrompt)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, req.Prompt)

	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *claudeSession) resolveModel(req Request) string {
	if req.Model != "" {
		return req.Model
	}
	if s.cfg.modelEnv != "" {
		return req.Env[s.cfg.modelEnv]
	}
	return ""
}

func (s *claudeSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapClaudeEventWithUsage), nil
}

func (s *claudeSession) SessionID() string { return s.sessionID }

func (s *claudeSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapClaudeEventWithUsage)
	final := s.usage
	if !s.sawResult {
		final.InputTokens = s.fallback.InputTokens
		final.OutputTokens = s.fallback.OutputTokens
		final.CacheTokens = s.fallback.CacheTokens
	}
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: final, Failed: s.failed}, tail, nil
}

func (s *claudeSession) mapClaudeEventWithUsage(obj map[string]any) []Event {
	if s.sessionID == "" {
		if sid, _ := obj["session_id"].(string); sid != "" {
			s.sessionID = sid
		}
	}
	if t, _ := obj["type"].(string); t == "result" {
		s.sawResult = true
		s.summary = mapString(obj, "result")
		// subtype stays "success" on an authentication failure, so is_error is
		// the only field that carries the verdict.
		s.failed, _ = obj["is_error"].(bool)
		setUsageFromObject(obj, &s.usage)
	} else if msg, ok := obj["message"].(map[string]any); ok {
		if model, _ := msg["model"].(string); model != "" && s.usage.Model == "" {
			s.usage.Model = model
		}
		if !s.sawResult {
			addUsageFromObject(msg, &s.fallback)
		}
	}
	return mapClaudeEvent(obj)
}

func mapClaudeEvent(obj map[string]any) []Event {
	t, _ := obj["type"].(string)
	switch t {
	case "system":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "assistant":
		text, tools := extractAssistantContent(obj)
		out := make([]Event, 0, len(tools)+1)
		for _, tc := range tools {
			out = append(out, Event{Type: EventToolCall, Payload: tc})
		}
		if text != "" {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}})
		}
		if len(out) == 0 {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "raw": obj}})
		}
		return out
	case "user":
		return []Event{{Type: EventToolResult, Payload: obj}}
	case "result":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "rate_limit_event":
		info, _ := obj["rate_limit_info"].(map[string]any)
		if info == nil {
			return nil
		}
		payload := map[string]any{"raw": info}
		if v, ok := info["status"].(string); ok {
			payload["status"] = v
		}
		if v, ok := info["rateLimitType"].(string); ok {
			payload["rate_limit_type"] = v
		}
		if v, ok := info["resetsAt"].(float64); ok {
			payload["resets_at"] = int64(v)
		}
		if v, ok := info["overageStatus"].(string); ok {
			payload["overage_status"] = v
		}
		if v, ok := info["isUsingOverage"].(bool); ok {
			payload["is_using_overage"] = v
		}
		return []Event{{Type: EventRateLimit, Payload: payload}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func extractAssistantContent(obj map[string]any) (string, []map[string]any) {
	msg, _ := obj["message"].(map[string]any)
	if msg == nil {
		return "", nil
	}
	content, _ := msg["content"].([]any)
	var text string
	var tools []map[string]any
	for _, c := range content {
		item, _ := c.(map[string]any)
		if item == nil {
			continue
		}
		switch item["type"] {
		case "text":
			if t, ok := item["text"].(string); ok {
				if text != "" {
					text += "\n"
				}
				text += t
			}
		case "tool_use":
			tools = append(tools, item)
		}
	}
	return text, tools
}

func setUsageFromObject(obj map[string]any, into *Usage) {
	if cost, ok := obj["total_cost_usd"].(float64); ok && cost > 0 {
		into.EstimatedCostUSD = cost
	}
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		into.InputTokens = int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		into.OutputTokens = int64(v)
	}
	cache := int64(0)
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		cache += int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		cache += int64(v)
	}
	into.CacheTokens = cache
}

func addUsageFromObject(obj map[string]any, into *Usage) {
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		into.InputTokens += int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		into.OutputTokens += int64(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		into.CacheTokens += int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		into.CacheTokens += int64(v)
	}
}
