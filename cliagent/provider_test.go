package cliagent

import "testing"

type stubProvider struct{ name string }

func (s stubProvider) Name() string               { return s.name }
func (s stubProvider) Capabilities() Capabilities { return Capabilities{} }
func (s stubProvider) NewSession() Session        { return nil }

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(stubProvider{name: "claude-code"})
	p, err := r.Get("claude-code")
	if err != nil || p.Name() != "claude-code" {
		t.Fatalf("Get = %v, %v", p, err)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	if _, err := NewRegistry().Get("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(stubProvider{name: "codex"})
	r.Register(stubProvider{name: "claude-code"})
	got := r.Names()
	if len(got) != 2 || got[0] != "claude-code" || got[1] != "codex" {
		t.Fatalf("Names = %v", got)
	}
}

func TestOptionsResolveDefaultsAndOverrides(t *testing.T) {
	cfg := resolveOptions("claude", []Option{
		WithBinary("/custom/claude"),
		WithBaseEnv(map[string]string{"FOO": "bar"}),
		WithModelEnv("CLAUDE_MODEL"),
		WithName("claude-code"),
		WithMCPConfig(".aas-mcp.json", true),
		WithAllowedModes([]string{"headless-code"}),
	})
	if cfg.binary != "/custom/claude" || cfg.baseEnv["FOO"] != "bar" ||
		cfg.modelEnv != "CLAUDE_MODEL" || cfg.name != "claude-code" ||
		cfg.mcpFilename != ".aas-mcp.json" || !cfg.strictMCP ||
		len(cfg.allowedModes) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestOptionsDefaultName(t *testing.T) {
	if cfg := resolveOptions("claude", nil); cfg.name != "claude" || cfg.binary != "claude" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
