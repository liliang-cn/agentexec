package cliagent

import "context"

// codexProvider builds and parses the Codex CLI in experimental --json mode.
type codexProvider struct{ cfg providerConfig }

// NewCodex returns a Codex Provider.
func NewCodex(opts ...Option) Provider {
	return &codexProvider{cfg: resolveOptions("codex", opts)}
}

func (p *codexProvider) Name() string { return "codex" }

func (p *codexProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, Plugins: false, MCP: true}
}

func (p *codexProvider) NewSession() Session {
	return &codexSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type codexSession struct {
	cfg      providerConfig
	lb       *LineBuffer
	threadID string
	usage    Usage
	summary  string
}

func (s *codexSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	argv := []string{s.cfg.binary, "exec"}
	if req.ResumeSessionID != "" {
		argv = append(argv, "resume", req.ResumeSessionID)
	}
	argv = append(argv, "--json")
	if req.Model != "" && s.cfg.modelEnv == "" {
		argv = append(argv, "--model", req.Model)
	}
	argv = append(argv, req.ExtraArgs...)

	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}
	argv = append(argv, prompt)

	return CommandSpec{
		Argv:    argv,
		Env:     mergeEnv(s.cfg.baseEnv, req.Env, s.cfg.modelEnv, req.Model),
		WorkDir: req.WorkspacePath,
	}, nil
}

func (s *codexSession) ParseChunk(chunk []byte) ([]Event, error) {
	lines := s.lb.Feed(chunk)
	return mapJSONLines(lines, s.mapCodexEvent), nil
}

func (s *codexSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(fullOutput)
	lines = append(lines, s.lb.Flush()...)
	events := mapJSONLines(lines, s.mapCodexEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, events, nil
}

func (s *codexSession) SessionID() string { return s.threadID }

// mapCodexEvent normalizes one Codex JSON frame into canonical events.
func (s *codexSession) mapCodexEvent(obj map[string]any) []Event {
	switch mapString(obj, "type") {
	case "thread.started":
		if id := mapString(obj, "thread_id"); id != "" {
			s.threadID = id
		}
	case "turn.started":
		// nothing to emit
	case "item.completed":
		return s.mapCodexItem(mapObject(obj, "item"))
	case "turn.completed":
		if u := mapObject(obj, "usage"); u != nil {
			s.captureCodexUsage(u)
		}
	}
	return nil
}

func (s *codexSession) mapCodexItem(item map[string]any) []Event {
	switch mapString(item, "type") {
	case "assistant_message", "agent_message":
		text := mapString(item, "text")
		s.summary = text
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"text": text}}}
	case "command_execution":
		return []Event{{Type: EventToolCall, Payload: map[string]any{
			"name":    "command",
			"command": mapString(item, "command"),
		}}}
	case "mcp_tool_call":
		return []Event{{Type: EventToolCall, Payload: map[string]any{
			"name":   mapString(item, "tool"),
			"server": mapString(item, "server"),
			"input":  item["arguments"],
		}}}
	}
	return nil
}

func (s *codexSession) captureCodexUsage(u map[string]any) {
	s.usage.InputTokens = mapInt64(u, "input_tokens")
	s.usage.OutputTokens = mapInt64(u, "output_tokens")
	s.usage.CacheTokens = mapInt64(u, "cached_input_tokens")
}
