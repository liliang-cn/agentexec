package cliagent

import (
	"context"
	"slices"
)

type geminiProvider struct{ cfg providerConfig }

// NewGemini returns a Gemini Provider.
func NewGemini(opts ...Option) Provider { return &geminiProvider{cfg: resolveOptions("gemini", opts)} }

func (p *geminiProvider) Name() string { return p.cfg.name }

func (p *geminiProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *geminiProvider) NewSession() Session { return &geminiSession{cfg: p.cfg, lb: &LineBuffer{}} }

type geminiSession struct {
	cfg     providerConfig
	lb      *LineBuffer
	usage   Usage
	summary string
}

func (s *geminiSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}
	argv := []string{s.cfg.binary, "--prompt", prompt, "--output-format", "stream-json"}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--yolo")
	}
	if !req.Sandbox {
		argv = append(argv, "--skip-trust")
	}
	argv = append(argv, req.ExtraArgs...)
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *geminiSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapGeminiEvent), nil
}

func (s *geminiSession) SessionID() string { return "" }

func (s *geminiSession) Finalize(_ context.Context, full []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(full)
	lines = append(lines, s.lb.Flush()...)
	tail := mapJSONLines(lines, s.mapGeminiEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, tail, nil
}

func (s *geminiSession) mapGeminiEvent(obj map[string]any) []Event {
	t, _ := obj["type"].(string)
	switch t {
	case "init":
		if m, _ := obj["model"].(string); m != "" {
			s.usage.Model = m
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "message":
		role, _ := obj["role"].(string)
		text, _ := obj["content"].(string)
		switch role {
		case "user":
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "user", "text": text}}}
		case "assistant":
			s.summary = text
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text, "delta": obj["delta"] == true}}}
		default:
			return []Event{{Type: EventAgentMessage, Payload: obj}}
		}
	case "tool_call", "tool_use":
		return []Event{{Type: EventToolCall, Payload: obj}}
	case "tool_result", "tool_response":
		return []Event{{Type: EventToolResult, Payload: obj}}
	case "result":
		captureGeminiUsage(obj, &s.usage)
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func captureGeminiUsage(obj map[string]any, into *Usage) {
	stats, _ := obj["stats"].(map[string]any)
	if stats == nil {
		return
	}
	if v, ok := stats["input_tokens"].(float64); ok {
		into.InputTokens = int64(v)
	}
	if v, ok := stats["output_tokens"].(float64); ok {
		into.OutputTokens = int64(v)
	}
	if v, ok := stats["cached"].(float64); ok {
		into.CacheTokens = int64(v)
	}
}
