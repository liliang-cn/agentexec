package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON in %s: %v", path, err)
	}
	return m
}

func TestInstallClaudeCreatesSettings(t *testing.T) {
	dir := t.TempDir()
	err := InstallClaude(InstallOptions{
		Command:   "/bin/myhook",
		ClaudeDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := readJSON(t, filepath.Join(dir, "settings.json"))
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing hooks: %v", settings)
	}
	// Default events should all be present.
	for _, ev := range []string{"PreToolUse", "PostToolUse", "Notification", "Stop", "UserPromptSubmit"} {
		if hooks[ev] == nil {
			t.Fatalf("missing default event %q in %v", ev, hooks)
		}
	}
}

func TestInstallClaudePreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	existing := `{"model":"opus","hooks":{"PreToolUse":[]}}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(existing), 0o644)

	if err := InstallClaude(InstallOptions{Command: "/bin/h", ClaudeDir: dir, Events: []string{"Stop"}}); err != nil {
		t.Fatal(err)
	}
	settings := readJSON(t, filepath.Join(dir, "settings.json"))
	if settings["model"] != "opus" {
		t.Fatalf("existing key 'model' lost: %v", settings)
	}
}

func TestInstallClaudeIdempotent(t *testing.T) {
	dir := t.TempDir()
	opts := InstallOptions{Command: "/bin/h", ClaudeDir: dir, Events: []string{"Stop"}}
	InstallClaude(opts)
	InstallClaude(opts)

	settings := readJSON(t, filepath.Join(dir, "settings.json"))
	stop := settings["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 {
		t.Fatalf("re-install duplicated hook group: %v", stop)
	}
}

func TestInstallCodexWritesHooksAndEnables(t *testing.T) {
	dir := t.TempDir()
	if err := InstallCodex(InstallOptions{Command: "/bin/h", CodexDir: dir, Events: []string{"PreToolUse"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks.json")); err != nil {
		t.Fatalf("hooks.json not written: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "hooks = true") {
		t.Fatalf("config.toml did not enable hooks: %s", cfg)
	}
	// Codex 0.141 only fires hooks written in the nested Claude-style shape:
	// {event: [{hooks: [{type, command}]}]}. A flat [{command}] is ignored.
	raw, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("hooks.json invalid: %v", err)
	}
	g := parsed.Hooks["PreToolUse"]
	if len(g) != 1 || len(g[0].Hooks) != 1 || g[0].Hooks[0].Type != "command" || g[0].Hooks[0].Command != "/bin/h" {
		t.Fatalf("PreToolUse not in nested {hooks:[{type,command}]} shape: %s", raw)
	}
}

func TestInstallCodexInsertsUnderExistingFeatures(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[features]\nweb_search = true\n"), 0o644)
	if err := InstallCodex(InstallOptions{Command: "/bin/h", CodexDir: dir}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !strings.Contains(string(cfg), "hooks = true") {
		t.Fatalf("hooks not enabled: %s", cfg)
	}
	if strings.Count(string(cfg), "[features]") != 1 {
		t.Fatalf("duplicated [features] table: %s", cfg)
	}
	if !strings.Contains(string(cfg), "web_search = true") {
		t.Fatalf("existing feature lost: %s", cfg)
	}
}

func TestInstallCodexEnableIdempotent(t *testing.T) {
	dir := t.TempDir()
	InstallCodex(InstallOptions{Command: "/bin/h", CodexDir: dir})
	InstallCodex(InstallOptions{Command: "/bin/h", CodexDir: dir})
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Count(string(cfg), "hooks = true") != 1 {
		t.Fatalf("hooks enabled twice: %s", cfg)
	}
}
