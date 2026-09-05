package agentexec

import (
	"context"
	"slices"
)

// Kimi CLI's `--print --output-format stream-json` prints one chat message per
// line in its kosong Message shape: {"role", "content", "tool_calls"?,
// "tool_call_id"?}. There is no init frame, no result frame, no usage and no
// session id — the stream is the transcript and nothing else. Two things about
// it are easy to get wrong:
//
//   - `content` is a string when the message has exactly one text part and a
//     list of {"type","text"} parts otherwise. Both mean the same thing.
//   - Errors are not frames. "LLM not set" arrives as a plain text line on
//     stdout, so it surfaces as terminal.output, and the exit code is the only
//     verdict there is.
//
// `--print` implies `--yolo`, so PermissionBypass adds nothing.

type kimiProvider struct{ cfg providerConfig }

// NewKimi returns a Kimi CLI Provider.
func NewKimi(opts ...Option) Provider { return &kimiProvider{cfg: resolveOptions("kimi", opts)} }

func (p *kimiProvider) Name() string { return p.cfg.name }

func (p *kimiProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *kimiProvider) NewSession() Session { return &kimiSession{cfg: p.cfg, lb: &LineBuffer{}} }

type kimiSession struct {
	cfg     providerConfig
	lb      *LineBuffer
	summary string
}

func (s *kimiSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--print", "--output-format", "stream-json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--session", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	argv = append(argv, req.ExtraArgs...)
	// The prompt is a flag value, not positional, and stays last.
	argv = append(argv, "--prompt", promptWithSystem(req))
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *kimiSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapKimiEvent), nil
}

func (s *kimiSession) SessionID() string { return "" }

func (s *kimiSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapKimiEvent)
	return Result{ExitCode: exitCode, Summary: s.summary}, tail, nil
}

func (s *kimiSession) mapKimiEvent(obj map[string]any) []Event {
	role := mapString(obj, "role")
	switch role {
	case "assistant":
		var out []Event
		calls, _ := obj["tool_calls"].([]any)
		for _, c := range calls {
			call, _ := c.(map[string]any)
			if call == nil {
				continue
			}
			fn := mapMap(call, "function")
			out = append(out, Event{Type: EventToolCall, Payload: map[string]any{
				"id":    mapString(call, "id"),
				"name":  mapString(fn, "name"),
				"input": mapString(fn, "arguments"),
				"raw":   call,
			}})
		}
		if text := joinTextParts(obj["content"]); text != "" {
			s.summary = text
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}})
		}
		if len(out) == 0 {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "raw": obj}})
		}
		return out
	case "tool":
		return []Event{{Type: EventToolResult, Payload: map[string]any{
			"id":     mapString(obj, "tool_call_id"),
			"output": joinTextParts(obj["content"]),
			"raw":    obj,
		}}}
	case "user", "system":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": role, "text": joinTextParts(obj["content"])}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": role, "raw": obj}}}
	}
}
