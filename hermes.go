package agentexec

import (
	"context"
	"encoding/json"
	"os"
	"slices"
)

// Hermes Agent's one-shot mode, `-z <prompt>`, prints only the final response
// text: no banner, no spinner, no tool previews, no session line, approvals
// bypassed. Everything the text leaves out — tokens, cost, model, session id
// and a `failed` flag — it will write as JSON to the path given with
// `--usage-file`, "even when the run fails, so pipelines can always account
// for spend". So a hermes session is a text session that creates that file in
// BuildCommand and reads it back in Finalize.
//
// `hermes chat -q` is the other non-interactive entry and is not used: it
// prints the banner, retry chatter and a "Resume this session with:" trailer
// around the answer.

type hermesProvider struct{ cfg providerConfig }

// NewHermes returns a Hermes Agent Provider.
func NewHermes(opts ...Option) Provider { return &hermesProvider{cfg: resolveOptions("hermes", opts)} }

func (p *hermesProvider) Name() string { return p.cfg.name }

func (p *hermesProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *hermesProvider) NewSession() Session {
	return &hermesSession{textSession: &textSession{cfg: p.cfg, lb: &LineBuffer{}}}
}

type hermesSession struct {
	*textSession
	usagePath string
	sessionID string
}

func (s *hermesSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !slices.Contains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	f, err := os.CreateTemp("", "agentexec-hermes-usage-*.json")
	if err != nil {
		return CommandSpec{}, err
	}
	f.Close()
	s.usagePath = f.Name()

	argv := []string{s.cfg.binary, "--usage-file", s.usagePath}
	if model := resolveModel(s.cfg, req); model != "" {
		argv = append(argv, "--model", model)
	}
	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--oneshot", promptWithSystem(req))
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *hermesSession) SessionID() string { return s.sessionID }

func (s *hermesSession) Finalize(ctx context.Context, fullOutput []byte, exitCode int) (Result, []Event, error) {
	res, tail, err := s.textSession.Finalize(ctx, fullOutput, exitCode)
	if err != nil {
		return res, tail, err
	}
	if s.usagePath == "" {
		return res, tail, nil
	}
	data, readErr := os.ReadFile(s.usagePath)
	os.Remove(s.usagePath)
	if readErr != nil || len(data) == 0 {
		// The file is best effort on hermes's side too; a missing report
		// leaves the text result as it is.
		return res, tail, nil
	}
	var report struct {
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		InputTokens      int64   `json:"input_tokens"`
		OutputTokens     int64   `json:"output_tokens"`
		CacheReadTokens  int64   `json:"cache_read_tokens"`
		CacheWriteTokens int64   `json:"cache_write_tokens"`
		ReasoningTokens  int64   `json:"reasoning_tokens"`
		Model            string  `json:"model"`
		SessionID        string  `json:"session_id"`
		Failed           bool    `json:"failed"`
	}
	if json.Unmarshal(data, &report) != nil {
		return res, tail, nil
	}
	s.sessionID = report.SessionID
	res.Usage = Usage{
		Model:            report.Model,
		InputTokens:      report.InputTokens,
		OutputTokens:     report.OutputTokens + report.ReasoningTokens,
		CacheTokens:      report.CacheReadTokens + report.CacheWriteTokens,
		EstimatedCostUSD: report.EstimatedCostUSD,
	}
	res.Failed = report.Failed
	return res, tail, nil
}
