package agentexec

import (
	"context"
	"slices"
)

// cursor-agent needs about fifteen lines rather than a fourth parser: Cursor's
// `--output-format stream-json` is Claude Code's dialect, frame for frame — a
// `system` init, `assistant` frames whose message.content holds text and
// tool_use blocks, a `result` frame carrying is_error and session_id. So a
// cursor session is a claude session with its BuildCommand replaced, and every
// hard-won thing claude.go knows about that stream — that the model's answer is
// the assistant-role message and not the ten lifecycle frames around it, that
// is_error is a verdict the exit code does not carry — applies unchanged.
//
// Only the argv differs, and it differs in every flag: cursor-agent spells
// print `-p/--print` but has no `--verbose`, spells bypass `--force`, and spells
// "this directory is fine, do not ask" `--trust`.

type cursorProvider struct{ cfg providerConfig }

// NewCursor returns a cursor-agent Provider.
func NewCursor(opts ...Option) Provider {
	return &cursorProvider{cfg: resolveOptions("cursor-agent", opts)}
}

func (p *cursorProvider) Name() string { return p.cfg.name }

func (p *cursorProvider) Capabilities() Capabilities {
	// No Plugins and no MCP: cursor-agent has its own notion of both, this
	// provider passes neither, and claiming them would invite a caller to send
	// plugin dirs that BuildCommand silently drops.
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *cursorProvider) NewSession() Session {
	return &cursorSession{claudeSession: &claudeSession{cfg: p.cfg, lb: &LineBuffer{}}}
}

// cursorSession embeds the claude session so ParseChunk, Finalize and
// SessionID come along, and overrides only the command.
type cursorSession struct{ *claudeSession }

func (s *cursorSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	// There is no --append-system-prompt here, so policy text goes where codex
	// and gemini put it: in front of the prompt.
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}

	argv := []string{s.cfg.binary, "--print", "--output-format", "stream-json"}
	if model := s.resolveModel(req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--force")
	}
	if !req.Sandbox {
		// The headless posture, same as --skip-trust / --skip-git-repo-check
		// on the others: in a scratch directory the CLI has never seen, the
		// trust prompt is a prompt nobody is there to answer.
		argv = append(argv, "--trust", "--sandbox", "disabled")
	}
	argv = append(argv, req.ExtraArgs...)
	// The prompt is a positional argument and stays last, with no `--` in
	// front of it: `cursor-agent -p "<prompt>"` is the documented invocation
	// and the only one that has been seen to work.
	argv = append(argv, prompt)

	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}
