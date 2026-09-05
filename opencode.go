package agentexec

import (
	"context"
	"slices"
)

// opencode's `run --format json` prints one event per line, every one of them
// carrying the session id:
//
//	{"type":"text"|"reasoning"|"tool_use"|"step_start"|"step_finish"|"error",
//	 "timestamp":..., "sessionID":"ses_...", "part":{...}}
//
// A `tool_use` arrives once, after the tool has finished, with the call and
// its outcome in the same part (`state.input`, `state.output`, `state.status`),
// so one frame becomes a tool_call event followed by a tool_result event.
// Usage is per step in `step_finish` and is summed here. `error` is a session
// error — the prompt itself failed — and the CLI exits non-zero on it, so it
// is a verdict, not a warning.
//
// Permission requests in headless mode are auto-rejected unless `--auto` is
// given, which is what PermissionBypass maps to.

type opencodeProvider struct{ cfg providerConfig }

// NewOpencode returns an opencode Provider.
func NewOpencode(opts ...Option) Provider {
	return &opencodeProvider{cfg: resolveOptions("opencode", opts)}
}

func (p *opencodeProvider) Name() string { return p.cfg.name }

func (p *opencodeProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *opencodeProvider) NewSession() Session {
	return &opencodeSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type opencodeSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	sessionID string
	summary   string
	failed    bool
}

func (s *opencodeSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "run", "--format", "json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--session", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--auto")
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, promptWithSystem(req))
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *opencodeSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapOpencodeEvent), nil
}

func (s *opencodeSession) SessionID() string { return s.sessionID }

func (s *opencodeSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapOpencodeEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage, Failed: s.failed}, tail, nil
}

func (s *opencodeSession) mapOpencodeEvent(obj map[string]any) []Event {
	if s.sessionID == "" {
		s.sessionID = mapString(obj, "sessionID")
	}
	t := mapString(obj, "type")
	part := mapMap(obj, "part")
	switch t {
	case "text":
		text := mapString(part, "text")
		if text != "" {
			s.summary = text
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
	case "reasoning":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "reasoning", "text": mapString(part, "text")}}}
	case "tool_use":
		state := mapMap(part, "state")
		id := mapString(part, "callID")
		name := mapString(part, "tool")
		return []Event{
			{Type: EventToolCall, Payload: map[string]any{"id": id, "name": name, "input": state["input"], "raw": part}},
			{Type: EventToolResult, Payload: map[string]any{
				"id": id, "name": name, "status": mapString(state, "status"),
				"output": mapString(state, "output"), "error": mapString(state, "error"), "raw": part,
			}},
		}
	case "step_finish":
		tokens := mapMap(part, "tokens")
		s.usage.InputTokens += mapInt(tokens, "input")
		s.usage.OutputTokens += mapInt(tokens, "output") + mapInt(tokens, "reasoning")
		cache := mapMap(tokens, "cache")
		s.usage.CacheTokens += mapInt(cache, "read") + mapInt(cache, "write")
		if cost, ok := part["cost"].(float64); ok {
			s.usage.EstimatedCostUSD += cost
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "step_finish", "raw": obj}}}
	case "error":
		s.failed = true
		errObj := mapMap(obj, "error")
		msg := mapString(mapMap(errObj, "data"), "message")
		if msg == "" {
			msg = mapString(errObj, "name")
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "error", "text": msg, "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}
