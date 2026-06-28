package cliagent

import "context"

// geminiProvider builds and parses the Gemini CLI in JSON output mode. Gemini
// emits a single JSON result object (response text + token stats) rather than a
// per-event stream, so parsing happens primarily in Finalize.
type geminiProvider struct{ cfg providerConfig }

// NewGemini returns a Gemini Provider.
func NewGemini(opts ...Option) Provider {
	return &geminiProvider{cfg: resolveOptions("gemini", opts)}
}

func (p *geminiProvider) Name() string { return "gemini" }

func (p *geminiProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: false, Plugins: false, MCP: true}
}

func (p *geminiProvider) NewSession() Session {
	return &geminiSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type geminiSession struct {
	cfg     providerConfig
	lb      *LineBuffer
	usage   Usage
	summary string
}

func (s *geminiSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	argv := []string{s.cfg.binary, "--output-format", "json"}
	if req.Model != "" && s.cfg.modelEnv == "" {
		argv = append(argv, "-m", req.Model)
	}
	argv = append(argv, req.ExtraArgs...)

	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}

	return CommandSpec{
		Argv:    argv,
		Env:     mergeEnv(s.cfg.baseEnv, req.Env, s.cfg.modelEnv, req.Model),
		WorkDir: req.WorkspacePath,
		Stdin:   []byte(prompt),
	}, nil
}

func (s *geminiSession) ParseChunk(chunk []byte) ([]Event, error) {
	lines := s.lb.Feed(chunk)
	return mapJSONLines(lines, s.mapGeminiEvent), nil
}

func (s *geminiSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(fullOutput)
	lines = append(lines, s.lb.Flush()...)
	events := mapJSONLines(lines, s.mapGeminiEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, events, nil
}

func (s *geminiSession) SessionID() string { return "" }

// mapGeminiEvent normalizes the Gemini JSON result object.
func (s *geminiSession) mapGeminiEvent(obj map[string]any) []Event {
	var events []Event
	if resp, ok := obj["response"].(string); ok {
		s.summary = resp
		events = append(events, Event{Type: EventAgentMessage, Payload: map[string]any{"text": resp}})
	}
	if stats := mapObject(obj, "stats"); stats != nil {
		s.captureGeminiUsage(stats)
	}
	return events
}

// captureGeminiUsage sums token stats across all reported models.
func (s *geminiSession) captureGeminiUsage(stats map[string]any) {
	models := mapObject(stats, "models")
	for _, v := range models {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		tokens := mapObject(m, "tokens")
		if tokens == nil {
			continue
		}
		s.usage.InputTokens += mapInt64(tokens, "prompt")
		s.usage.OutputTokens += mapInt64(tokens, "candidates")
		s.usage.CacheTokens += mapInt64(tokens, "cached")
	}
}
