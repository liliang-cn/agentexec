package cliagent

import (
	"context"
	"slices"
)

type codexProvider struct{ cfg providerConfig }

// NewCodex returns a Codex Provider.
func NewCodex(opts ...Option) Provider { return &codexProvider{cfg: resolveOptions("codex", opts)} }

func (p *codexProvider) Name() string { return p.cfg.name }

func (p *codexProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *codexProvider) NewSession() Session { return &codexSession{cfg: p.cfg, lb: &LineBuffer{}} }

type codexSession struct {
	cfg      providerConfig
	lb       *LineBuffer
	usage    Usage
	threadID string
	summary  string
}

func (s *codexSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}
	argv := []string{s.cfg.binary, "exec"}
	if req.ResumeSessionID != "" {
		argv = append(argv, "resume", req.ResumeSessionID)
	}
	argv = append(argv, "--json")
	if !req.Sandbox {
		argv = append(argv, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox")
	} else if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--", prompt)
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *codexSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapCodexEvent), nil
}

func (s *codexSession) SessionID() string { return s.threadID }

func (s *codexSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapCodexEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, tail, nil
}

func (s *codexSession) mapCodexEvent(obj map[string]any) []Event {
	if s.threadID == "" {
		if tid, _ := obj["thread_id"].(string); tid != "" {
			s.threadID = tid
		}
	}
	t, _ := obj["type"].(string)
	switch t {
	case "turn.completed":
		captureCodexUsage(obj, &s.usage)
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "thread.started", "turn.started":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "item.completed":
		item, _ := obj["item"].(map[string]any)
		if item == nil {
			return []Event{{Type: EventAgentMessage, Payload: obj}}
		}
		switch it, _ := item["type"].(string); it {
		case "agent_message":
			text, _ := item["text"].(string)
			s.summary = text
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
		case "function_call", "tool_call", "command_execution":
			return []Event{{Type: EventToolCall, Payload: item}}
		case "function_call_output", "tool_result", "command_output":
			return []Event{{Type: EventToolResult, Payload: item}}
		default:
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": it, "raw": item}}}
		}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func captureCodexUsage(obj map[string]any, into *Usage) {
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
	if v, ok := usage["cached_input_tokens"].(float64); ok {
		into.CacheTokens = int64(v)
	}
	if v, ok := usage["reasoning_output_tokens"].(float64); ok {
		into.OutputTokens += int64(v)
	}
}
