package agentexec

import (
	"context"
	"slices"
)

// GitHub Copilot CLI's `-p ... --output-format json` is JSONL of
// {"type":"<area>.<event>","data":{...}} session events — user.message,
// assistant.message, tool.execution_start, tool.execution_complete,
// session.error and a dozen ephemeral session.* notices — closed by one
// {"type":"result","sessionId","exitCode","usage"} frame. The result's
// exitCode is the CLI's own verdict on the turn and is what Failed reads.
//
// Two things the output does not carry: token usage (the result's usage block
// counts premium requests and code changes, and assistant.usage frames are not
// emitted in print mode, so only the per-message outputTokens is summed), and
// an assistant.message when the model only called tools (content is "" then,
// and the calls ride in toolRequests). Tool calls are taken from
// tool.execution_start instead, which carries the same fields once.
//
// `--allow-all-tools` is required for non-interactive mode by the CLI's own
// help; that and `--no-ask-user` are the headless posture. `--yolo` is the
// bypass and already implies the former.

type copilotProvider struct{ cfg providerConfig }

// NewCopilot returns a GitHub Copilot CLI Provider.
func NewCopilot(opts ...Option) Provider {
	return &copilotProvider{cfg: resolveOptions("copilot", opts)}
}

func (p *copilotProvider) Name() string { return p.cfg.name }

func (p *copilotProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *copilotProvider) NewSession() Session {
	return &copilotSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type copilotSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	sessionID string
	summary   string
	failed    bool
}

func (s *copilotSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--output-format", "json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		// --resume takes an optional value, so it must be attached with "=":
		// a separate argument would be read as the next flag.
		argv = append(argv, "--resume="+req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	switch {
	case req.PermissionMode == PermissionBypass:
		argv = append(argv, "--yolo")
	case !req.Sandbox:
		argv = append(argv, "--allow-all-tools")
	}
	if !req.Sandbox {
		argv = append(argv, "--no-ask-user")
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--prompt", promptWithSystem(req))
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *copilotSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapCopilotEvent), nil
}

func (s *copilotSession) SessionID() string { return s.sessionID }

func (s *copilotSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapCopilotEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage, Failed: s.failed}, tail, nil
}

func (s *copilotSession) mapCopilotEvent(obj map[string]any) []Event {
	t := mapString(obj, "type")
	data := mapMap(obj, "data")
	switch t {
	case "result":
		if sid := mapString(obj, "sessionId"); sid != "" {
			s.sessionID = sid
		}
		s.failed = mapInt(obj, "exitCode") != 0
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "session.start":
		if sid := mapString(data, "sessionId"); sid != "" && s.sessionID == "" {
			s.sessionID = sid
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "assistant.message":
		s.usage.OutputTokens += mapInt(data, "outputTokens")
		text := mapString(data, "content")
		if text == "" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "raw": obj}}}
		}
		s.summary = text
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
	case "assistant.message_delta":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": mapString(data, "deltaContent"), "delta": true}}}
	case "assistant.usage":
		if m := mapString(data, "model"); m != "" {
			s.usage.Model = m
		}
		s.usage.InputTokens += mapInt(data, "inputTokens")
		s.usage.CacheTokens += mapInt(data, "cacheReadTokens")
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "usage", "raw": obj}}}
	case "tool.execution_start":
		return []Event{{Type: EventToolCall, Payload: map[string]any{
			"id": mapString(data, "toolCallId"), "name": mapString(data, "toolName"), "input": data["arguments"], "raw": data,
		}}}
	case "tool.execution_complete":
		success, _ := data["success"].(bool)
		return []Event{{Type: EventToolResult, Payload: map[string]any{
			"id": mapString(data, "toolCallId"), "success": success,
			"output": mapString(mapMap(data, "result"), "content"), "raw": data,
		}}}
	case "user.message":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "user", "text": mapString(data, "content")}}}
	case "session.error":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "error", "text": mapString(data, "message"), "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}
