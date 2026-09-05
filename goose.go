package agentexec

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// goose's `run --output-format stream-json` prints a three-line ASCII banner
// and then JSONL:
//
//	{"type":"message","message":{"role":"assistant"|"user","content":[
//	    {"type":"text","text":...} |
//	    {"type":"toolRequest","id":...,"toolCall":{"value":{"name","arguments"}}} |
//	    {"type":"toolResponse","id":...,"toolResult":{"value":{"content":[...],"isError"}}}]}}
//	{"type":"complete","total_tokens","input_tokens","output_tokens",
//	    "cache_read_input_tokens","cache_write_input_tokens"}
//
// Tool results come back as `user` messages, the same inversion Claude's
// stream has. The session id is printed nowhere in the JSON; it is on the
// second banner line as "<id> · <path>", so that line is sniffed before the
// JSON pass, and Resume is only as reliable as that banner's format.
//
// The prompt goes behind `-t`, the system prompt has its own `--system`, and
// the model and approval mode are environment variables (GOOSE_MODEL,
// GOOSE_MODE=auto), which is where BuildCommand puts them.

type gooseProvider struct{ cfg providerConfig }

// NewGoose returns a goose Provider.
func NewGoose(opts ...Option) Provider { return &gooseProvider{cfg: resolveOptions("goose", opts)} }

func (p *gooseProvider) Name() string { return p.cfg.name }

func (p *gooseProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *gooseProvider) NewSession() Session { return &gooseSession{cfg: p.cfg, lb: &LineBuffer{}} }

type gooseSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	sessionID string
	summary   string
}

func (s *gooseSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	env := map[string]string{}
	maps.Copy(env, s.cfg.baseEnv)
	argv := []string{s.cfg.binary, "run", "--output-format", "stream-json"}
	if model := resolveModel(s.cfg, req); model != "" {
		env["GOOSE_MODEL"] = model
	}
	if req.PermissionMode == PermissionBypass {
		env["GOOSE_MODE"] = "auto"
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", "--session-id", req.ResumeSessionID)
	}
	if req.SystemPrompt != "" {
		argv = append(argv, "--system", req.SystemPrompt)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--text", req.Prompt)
	return CommandSpec{Argv: argv, Env: mergeEnv(env, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *gooseSession) ParseChunk(chunk []byte) ([]Event, error) {
	return s.feed(s.lb.Feed(chunk)), nil
}

func (s *gooseSession) SessionID() string { return s.sessionID }

func (s *gooseSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	var tail []Event
	if !s.lb.Fed() && len(fullOutput) > 0 {
		tail = s.feed(s.lb.Feed(fullOutput))
	}
	tail = append(tail, s.feed(s.lb.Flush())...)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, tail, nil
}

// feed sniffs the banner for the session id, then does the usual JSON pass.
func (s *gooseSession) feed(lines []string) []Event {
	if s.sessionID == "" {
		for _, line := range lines {
			if strings.HasPrefix(line, "{") {
				continue
			}
			// The line above it reads "● new session · <provider> <model>"
			// and has the same separator; the id line is the one whose
			// right-hand side is the working directory.
			left, right, ok := strings.Cut(line, " · ")
			right = strings.TrimSpace(right)
			if !ok || right == "" || !strings.HasPrefix(right, "/") && !strings.HasPrefix(right, "~") {
				continue
			}
			if fields := strings.Fields(left); len(fields) > 0 {
				s.sessionID = fields[len(fields)-1]
				break
			}
		}
	}
	return mapJSONLines(lines, s.mapGooseEvent)
}

func (s *gooseSession) mapGooseEvent(obj map[string]any) []Event {
	t := mapString(obj, "type")
	switch t {
	case "message":
		msg := mapMap(obj, "message")
		role := mapString(msg, "role")
		parts, _ := msg["content"].([]any)
		var out []Event
		var text string
		for _, p := range parts {
			item, _ := p.(map[string]any)
			if item == nil {
				continue
			}
			switch mapString(item, "type") {
			case "text":
				if t := mapString(item, "text"); t != "" {
					if text != "" {
						text += "\n"
					}
					text += t
				}
			case "toolRequest":
				call := mapMap(mapMap(item, "toolCall"), "value")
				out = append(out, Event{Type: EventToolCall, Payload: map[string]any{
					"id": mapString(item, "id"), "name": mapString(call, "name"), "input": call["arguments"], "raw": item,
				}})
			case "toolResponse":
				result := mapMap(mapMap(item, "toolResult"), "value")
				isErr, _ := result["isError"].(bool)
				out = append(out, Event{Type: EventToolResult, Payload: map[string]any{
					"id": mapString(item, "id"), "output": joinTextParts(result["content"]), "is_error": isErr, "raw": item,
				}})
			}
		}
		if text != "" {
			if role == "assistant" {
				s.summary = text
			}
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": role, "text": text}})
		}
		if len(out) == 0 {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": role, "raw": obj}})
		}
		return out
	case "complete":
		s.usage.InputTokens = mapInt(obj, "input_tokens")
		s.usage.OutputTokens = mapInt(obj, "output_tokens")
		s.usage.CacheTokens = mapInt(obj, "cache_read_input_tokens") + mapInt(obj, "cache_write_input_tokens")
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}
