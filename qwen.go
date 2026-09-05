package agentexec

import (
	"context"
	"maps"
	"slices"
)

// Qwen Code is a gemini-cli fork whose `--output-format stream-json` is, as of
// 0.23, Claude Code's dialect rather than gemini's: a `system`/`init` frame
// carrying session_id, `assistant` frames with text and tool_use content
// blocks, `user` frames with tool_result blocks, and a `result` frame with
// is_error. So, like cursor-agent, a qwen session is a claude session with
// only BuildCommand replaced.
//
// The flags are its own: the prompt is positional (`-p` is deprecated and
// warns), bypass is `--yolo`, and there is no trust gate to skip — a headless
// run in a directory it has never seen just runs.

type qwenProvider struct{ cfg providerConfig }

// NewQwen returns a Qwen Code Provider.
func NewQwen(opts ...Option) Provider {
	return &qwenProvider{cfg: resolveOptions("qwen", opts)}
}

func (p *qwenProvider) Name() string { return p.cfg.name }

func (p *qwenProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *qwenProvider) NewSession() Session {
	return &qwenSession{claudeSession: &claudeSession{cfg: p.cfg, lb: &LineBuffer{}}}
}

type qwenSession struct{ *claudeSession }

func (s *qwenSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--output-format", "stream-json"}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	env := map[string]string{}
	maps.Copy(env, s.cfg.baseEnv)
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--yolo")
		// --yolo prints a paragraph about running unsandboxed to stderr, which
		// under a PTY lands in the parsed stream. The CLI names the variable
		// that silences it.
		env["QWEN_CODE_SUPPRESS_YOLO_WARNING"] = "1"
	}
	if req.SystemPrompt != "" {
		argv = append(argv, "--append-system-prompt", req.SystemPrompt)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, req.Prompt)
	return CommandSpec{Argv: argv, Env: mergeEnv(env, req.Env), WorkDir: req.WorkspacePath}, nil
}
