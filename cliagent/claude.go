package cliagent

import "context"

// claudeProvider builds and parses the Claude Code CLI in stream-json mode.
type claudeProvider struct{ cfg providerConfig }

// NewClaude returns a Claude Code Provider.
func NewClaude(opts ...Option) Provider {
	return &claudeProvider{cfg: resolveOptions("claude", opts)}
}

func (p *claudeProvider) Name() string { return "claude" }

func (p *claudeProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, Plugins: true, MCP: true}
}

func (p *claudeProvider) NewSession() Session {
	return &claudeSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type claudeSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	sessionID string
	usage     Usage
	summary   string
}

func (s *claudeSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	argv := []string{s.cfg.binary, "-p", "--output-format", "stream-json", "--verbose"}

	if req.Model != "" && s.cfg.modelEnv == "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.SystemPrompt != "" {
		argv = append(argv, "--append-system-prompt", req.SystemPrompt)
	}
	if req.Continue {
		argv = append(argv, "--continue")
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	}
	if req.MCPConfigPath != "" {
		argv = append(argv, "--mcp-config", req.MCPConfigPath)
	}
	for _, pl := range req.Plugins {
		argv = append(argv, "--plugin-dir", pl.Path)
	}
	argv = append(argv, req.ExtraArgs...)

	return CommandSpec{
		Argv:    argv,
		Env:     mergeEnv(s.cfg.baseEnv, req.Env, s.cfg.modelEnv, req.Model),
		WorkDir: req.WorkspacePath,
		Stdin:   []byte(req.Prompt),
	}, nil
}

func (s *claudeSession) ParseChunk(chunk []byte) ([]Event, error) {
	lines := s.lb.Feed(chunk)
	return mapJSONLines(lines, s.mapClaudeEvent), nil
}

func (s *claudeSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(fullOutput)
	lines = append(lines, s.lb.Flush()...)
	events := mapJSONLines(lines, s.mapClaudeEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, events, nil
}

func (s *claudeSession) SessionID() string { return s.sessionID }

// mapClaudeEvent normalizes one stream-json frame into canonical events.
func (s *claudeSession) mapClaudeEvent(obj map[string]any) []Event {
	switch mapString(obj, "type") {
	case "system":
		if id := mapString(obj, "session_id"); id != "" {
			s.sessionID = id
		}
		return nil

	case "assistant":
		if id := mapString(obj, "session_id"); id != "" {
			s.sessionID = id
		}
		msg := mapObject(obj, "message")
		if u := mapObject(msg, "usage"); u != nil {
			s.addUsageFromObject(u)
		}
		return s.extractAssistantContent(msg)

	case "user":
		return extractToolResults(mapObject(obj, "message"))

	case "result":
		s.summary = mapString(obj, "result")
		if u := mapObject(obj, "usage"); u != nil {
			s.setUsageFromObject(u)
		}
		if c := mapFloat64(obj, "total_cost_usd"); c != 0 {
			s.usage.EstimatedCostUSD = c
		}
		return nil

	case "rate_limit_event":
		return []Event{{Type: EventRateLimit, Payload: obj}}
	}
	return nil
}

func (s *claudeSession) extractAssistantContent(msg map[string]any) []Event {
	var events []Event
	for _, item := range mapArray(msg, "content") {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch mapString(block, "type") {
		case "text":
			events = append(events, Event{
				Type:    EventAgentMessage,
				Payload: map[string]any{"text": mapString(block, "text")},
			})
		case "tool_use":
			events = append(events, Event{
				Type: EventToolCall,
				Payload: map[string]any{
					"id":    mapString(block, "id"),
					"name":  mapString(block, "name"),
					"input": block["input"],
				},
			})
		}
	}
	return events
}

func extractToolResults(msg map[string]any) []Event {
	var events []Event
	for _, item := range mapArray(msg, "content") {
		block, ok := item.(map[string]any)
		if !ok || mapString(block, "type") != "tool_result" {
			continue
		}
		events = append(events, Event{
			Type: EventToolResult,
			Payload: map[string]any{
				"tool_use_id": mapString(block, "tool_use_id"),
				"content":     block["content"],
			},
		})
	}
	return events
}

// addUsageFromObject accumulates per-turn token usage.
func (s *claudeSession) addUsageFromObject(u map[string]any) {
	s.usage.InputTokens += mapInt64(u, "input_tokens")
	s.usage.OutputTokens += mapInt64(u, "output_tokens")
	s.usage.CacheTokens += mapInt64(u, "cache_creation_input_tokens") + mapInt64(u, "cache_read_input_tokens")
}

// setUsageFromObject replaces totals with an authoritative usage object.
func (s *claudeSession) setUsageFromObject(u map[string]any) {
	s.usage.InputTokens = mapInt64(u, "input_tokens")
	s.usage.OutputTokens = mapInt64(u, "output_tokens")
	s.usage.CacheTokens = mapInt64(u, "cache_creation_input_tokens") + mapInt64(u, "cache_read_input_tokens")
}
