package agentexec

import (
	"context"
	"slices"
)

// pi's `-p --mode json` prints its agent-core event stream:
//
//	{"type":"session","id":...,"cwd":...}
//	{"type":"message_start"|"message_end","message":{"role","content":[...],
//	    "usage":{"input","output","cacheRead","cacheWrite","cost":{"total"}},
//	    "stopReason","errorMessage"?}}
//	{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta"}}
//	{"type":"tool_execution_start","toolCallId","toolName","args"}
//	{"type":"tool_execution_end","toolCallId","toolName","result","isError"}
//	{"type":"turn_end"} {"type":"agent_end"} {"type":"agent_settled"}
//
// Usage is per assistant message and summed. A provider failure — a 429, an
// auth error — is an assistant message_end with stopReason "error" and the
// text in errorMessage, and the process still exits zero; that is the verdict
// Failed reads.
//
// In print mode pi reads stdin to EOF before it starts when stdin is not a
// terminal, so a caller piping it must close stdin or hand it /dev/null. Under
// the pty runner stdin is a terminal and this does not arise.

type piProvider struct{ cfg providerConfig }

// NewPi returns a pi Provider.
func NewPi(opts ...Option) Provider { return &piProvider{cfg: resolveOptions("pi", opts)} }

func (p *piProvider) Name() string { return p.cfg.name }

func (p *piProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *piProvider) NewSession() Session { return &piSession{cfg: p.cfg, lb: &LineBuffer{}} }

type piSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	sessionID string
	summary   string
	failed    bool
}

func (s *piSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--print", "--mode", "json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--session", req.ResumeSessionID)
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

func (s *piSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapPiEvent), nil
}

func (s *piSession) SessionID() string { return s.sessionID }

func (s *piSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapPiEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage, Failed: s.failed}, tail, nil
}

func (s *piSession) mapPiEvent(obj map[string]any) []Event {
	t := mapString(obj, "type")
	switch t {
	case "session":
		if id := mapString(obj, "id"); id != "" {
			s.sessionID = id
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "message_update":
		ev := mapMap(obj, "assistantMessageEvent")
		switch mapString(ev, "type") {
		case "text_delta":
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": mapString(ev, "delta"), "delta": true}}}
		case "thinking_delta":
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "reasoning", "text": mapString(ev, "delta"), "delta": true}}}
		}
		return nil
	case "message_end":
		msg := mapMap(obj, "message")
		role := mapString(msg, "role")
		text := joinTextParts(msg["content"])
		if role != "assistant" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": role, "text": text}}}
		}
		if m := mapString(msg, "model"); m != "" {
			s.usage.Model = m
		}
		usage := mapMap(msg, "usage")
		s.usage.InputTokens += mapInt(usage, "input")
		s.usage.OutputTokens += mapInt(usage, "output")
		s.usage.CacheTokens += mapInt(usage, "cacheRead") + mapInt(usage, "cacheWrite")
		if total, ok := mapMap(usage, "cost")["total"].(float64); ok {
			s.usage.EstimatedCostUSD += total
		}
		if mapString(msg, "stopReason") == "error" {
			s.failed = true
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "error", "text": mapString(msg, "errorMessage"), "raw": obj}}}
		}
		if text == "" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "raw": obj}}}
		}
		s.summary = text
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
	case "tool_execution_start":
		return []Event{{Type: EventToolCall, Payload: map[string]any{
			"id": mapString(obj, "toolCallId"), "name": mapString(obj, "toolName"), "input": obj["args"], "raw": obj,
		}}}
	case "tool_execution_end":
		isErr, _ := obj["isError"].(bool)
		return []Event{{Type: EventToolResult, Payload: map[string]any{
			"id": mapString(obj, "toolCallId"), "name": mapString(obj, "toolName"), "output": obj["result"], "is_error": isErr, "raw": obj,
		}}}
	case "message_start":
		// The user's own prompt echoed back; the assistant's start carries
		// nothing the end will not.
		if msg := mapMap(obj, "message"); mapString(msg, "role") == "user" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "user", "text": joinTextParts(msg["content"])}}}
		}
		return nil
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}
