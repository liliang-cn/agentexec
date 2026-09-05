package agentexec

import (
	"context"
	"slices"
	"strings"
)

// agy (the Antigravity CLI) prints `--output-format stream-json` as NDJSON
// where the discriminator is `event` and the payload sits under a key of the
// same name:
//
//	{"event":"init","init":{...}}
//	{"event":"step_update","step_update":{"step_type":...,"tool_name"?,"text_delta"?,...}}
//	{"event":"result","result":{"conversation_id","status","response","error",
//	    "num_turns","duration_seconds","usage":{"input_tokens","output_tokens",
//	    "thinking_tokens","cache_read_tokens","total_tokens"}}}
//
// Only the result frame has been seen from a real run on the machine this was
// written on (the account was not signed in, which is itself reported as a
// result with status "ERROR" and exit 1). The init and step_update field names
// come from the binary's own struct tags, so those two are mapped
// defensively: whatever carries text becomes an assistant message, whatever
// names a tool becomes a tool call, and the rest is passed through raw.
//
// `--print` takes the next argument as its prompt and does not stop at a
// flag: `--print --output-format stream-json "task"` runs the prompt
// "--output-format". The prompt flag therefore goes last.

type agyProvider struct{ cfg providerConfig }

// NewAgy returns an Antigravity CLI Provider.
func NewAgy(opts ...Option) Provider { return &agyProvider{cfg: resolveOptions("agy", opts)} }

func (p *agyProvider) Name() string { return p.cfg.name }

func (p *agyProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *agyProvider) NewSession() Session { return &agySession{cfg: p.cfg, lb: &LineBuffer{}} }

type agySession struct {
	cfg            providerConfig
	lb             *LineBuffer
	usage          Usage
	conversationID string
	summary        string
	failed         bool
}

func (s *agySession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--output-format", "stream-json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--conversation", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--print", promptWithSystem(req))
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *agySession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapAgyEvent), nil
}

func (s *agySession) SessionID() string { return s.conversationID }

func (s *agySession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	tail := finishOutput(s.lb, fullOutput, s.mapAgyEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage, Failed: s.failed}, tail, nil
}

func (s *agySession) mapAgyEvent(obj map[string]any) []Event {
	ev := mapString(obj, "event")
	body := mapMap(obj, ev)
	if s.conversationID == "" {
		for _, m := range []map[string]any{obj, body} {
			if id := mapString(m, "conversation_id"); id != "" {
				s.conversationID = id
				break
			}
		}
	}
	switch ev {
	case "init":
		if m := mapString(body, "model"); m != "" {
			s.usage.Model = m
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "step_update":
		if delta := mapString(body, "text_delta"); delta != "" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": delta, "delta": true}}}
		}
		if text := mapString(body, "text"); text != "" {
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
		}
		if name := mapString(body, "tool_name"); name != "" {
			return []Event{{Type: EventToolCall, Payload: map[string]any{
				"name": name, "input": body["tool_info"], "step_type": mapString(body, "step_type"), "raw": body,
			}}}
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "step", "step_type": mapString(body, "step_type"), "raw": body}}}
	case "result":
		s.summary = mapString(body, "response")
		s.failed = mapString(body, "error") != "" || strings.EqualFold(mapString(body, "status"), "error")
		usage := mapMap(body, "usage")
		s.usage.InputTokens = mapInt(usage, "input_tokens")
		s.usage.OutputTokens = mapInt(usage, "output_tokens") + mapInt(usage, "thinking_tokens")
		s.usage.CacheTokens = mapInt(usage, "cache_read_tokens")
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": ev, "raw": obj}}}
	}
}
