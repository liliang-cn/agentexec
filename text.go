package agentexec

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// ErrNoArgv is returned by a text provider built without WithArgv when its
// binary is not one this library ships a template for.
var ErrNoArgv = errors.New("agentexec: text provider has no argv template")

// A text provider drives a CLI that answers in prose rather than JSON. Every
// line it prints is a terminal.output event, and Finalize adds one
// agent.message with role "assistant" holding the whole output, so a consumer
// keeps the one filter it already has for the dialect providers. There is no
// usage, no session id and no verdict, because the CLI states none.
//
// The argv comes from WithArgv, a template in which the argument containing
// "{prompt}" receives the prompt. That is the whole contract: which flags make
// a given CLI run once and exit is the caller's knowledge, or one of the
// ready-made constructors below.

type textProvider struct{ cfg providerConfig }

// NewText returns a Provider for a plain-text CLI. Set the binary with
// WithBinary and the arguments with WithArgv; WithModelFlag names the flag the
// model rides behind, if the CLI has one.
func NewText(opts ...Option) Provider { return &textProvider{cfg: resolveOptions("text", opts)} }

// NewAider returns a text Provider for aider with its one-shot flags filled in:
// `--message` carries the prompt, `--yes-always` answers every confirmation,
// and the rest turn off the terminal niceties that are noise in a captured
// stream. Auto-commits are left at aider's default; a caller that does not want
// them passes `--no-auto-commits` in ExtraArgs.
func NewAider(opts ...Option) Provider {
	base := []Option{
		WithArgv("--message", "{prompt}", "--yes-always", "--no-pretty", "--no-stream", "--no-check-update", "--no-fancy-input"),
		WithModelFlag("--model"),
	}
	return &textProvider{cfg: resolveOptions("aider", append(base, opts...))}
}

func (p *textProvider) Name() string { return p.cfg.name }

func (p *textProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *textProvider) NewSession() Session { return &textSession{cfg: p.cfg, lb: &LineBuffer{}} }

type textSession struct {
	cfg providerConfig
	lb  *LineBuffer
	out strings.Builder
}

func (s *textSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	if len(s.cfg.argv) == 0 {
		return CommandSpec{}, ErrNoArgv
	}
	prompt := promptWithSystem(req)
	argv := []string{s.cfg.binary}
	placed := false
	for _, a := range s.cfg.argv {
		if strings.Contains(a, "{prompt}") {
			a = strings.ReplaceAll(a, "{prompt}", prompt)
			placed = true
		}
		argv = append(argv, a)
	}
	if model := resolveModel(s.cfg, req); model != "" && s.cfg.modelFlag != "" {
		argv = append(argv, s.cfg.modelFlag, model)
	}
	argv = append(argv, req.ExtraArgs...)
	if !placed {
		argv = append(argv, prompt)
	}
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *textSession) ParseChunk(chunk []byte) ([]Event, error) {
	return s.lines(s.lb.Feed(chunk)), nil
}

func (s *textSession) SessionID() string { return "" }

func (s *textSession) Finalize(_ context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	var tail []Event
	if !s.lb.Fed() && len(fullOutput) > 0 {
		tail = s.lines(s.lb.Feed(fullOutput))
	}
	tail = append(tail, s.lines(s.lb.Flush())...)
	summary := strings.TrimSpace(s.out.String())
	if summary != "" {
		tail = append(tail, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": summary}})
	}
	return Result{ExitCode: exitCode, Summary: summary}, tail, nil
}

func (s *textSession) lines(lines []string) []Event {
	if len(lines) == 0 {
		return nil
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		s.out.WriteString(line)
		s.out.WriteByte('\n')
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, Event{Type: EventTerminalOutput, Payload: map[string]any{"line": line}})
	}
	return out
}
