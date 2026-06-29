package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// defaultEvents are the hook events installed when InstallOptions.Events is empty.
var defaultEvents = []string{"PreToolUse", "PostToolUse", "Notification", "Stop", "UserPromptSubmit"}

// InstallOptions configures hook installation. ClaudeDir/CodexDir override the
// default ~/.claude / ~/.codex locations (used by tests).
type InstallOptions struct {
	Command   string   // command the CLI should run for each event
	Events    []string // defaults to defaultEvents
	Matcher   string   // defaults to "*"
	ClaudeDir string   // overrides ~/.claude
	CodexDir  string   // overrides ~/.codex
}

func (o InstallOptions) events() []string {
	if len(o.Events) > 0 {
		return o.Events
	}
	return defaultEvents
}

func (o InstallOptions) matcher() string {
	if o.Matcher != "" {
		return o.Matcher
	}
	return "*"
}

func (o InstallOptions) claudeDir() (string, error) {
	if o.ClaudeDir != "" {
		return o.ClaudeDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

func (o InstallOptions) codexDir() (string, error) {
	if o.CodexDir != "" {
		return o.CodexDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// InstallClaude merges hook command registrations into <ClaudeDir>/settings.json,
// preserving all other settings. It is idempotent: re-running with the same
// command does not create duplicate hook groups.
func InstallClaude(opts InstallOptions) error {
	dir, err := opts.claudeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")

	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	for _, ev := range opts.events() {
		groups, _ := hooks[ev].([]any)
		if hookGroupExists(groups, opts.Command) {
			continue
		}
		groups = append(groups, map[string]any{
			"matcher": opts.matcher(),
			"hooks": []any{
				map[string]any{"type": "command", "command": opts.Command},
			},
		})
		hooks[ev] = groups
	}
	settings["hooks"] = hooks

	return writeJSON(path, settings)
}

// hookGroupExists reports whether any group already registers command.
func hookGroupExists(groups []any, command string) bool {
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := group["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range inner {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				return true
			}
		}
	}
	return false
}

// InstallCodex writes <CodexDir>/hooks.json and enables [features] hooks in
// <CodexDir>/config.toml. It is idempotent.
func InstallCodex(opts InstallOptions) error {
	dir, err := opts.codexDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Codex 0.141 requires the same nested shape as Claude
	// ({event: [{hooks: [{type, command}]}]}); a flat [{command}] entry is
	// silently ignored and the hook never fires.
	eventHooks := map[string]any{}
	for _, ev := range opts.events() {
		eventHooks[ev] = []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": opts.Command}},
		}}
	}
	if err := writeJSON(filepath.Join(dir, "hooks.json"), map[string]any{"hooks": eventHooks}); err != nil {
		return err
	}

	cfgPath := filepath.Join(dir, "config.toml")
	content := ""
	if data, err := os.ReadFile(cfgPath); err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(cfgPath, []byte(enableCodexHooks(content)), 0o644)
}

// enableCodexHooks ensures the config enables [features] hooks without
// duplicating an existing [features] table or an already-enabled flag.
func enableCodexHooks(content string) string {
	if strings.Contains(content, "hooks = true") {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "[features]" {
			inserted := append([]string{}, lines[:i+1]...)
			inserted = append(inserted, "hooks = true")
			inserted = append(inserted, lines[i+1:]...)
			return strings.Join(inserted, "\n")
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "[features]\nhooks = true\n"
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
