package cliagent

import "testing"

// stubProvider is a minimal Provider for registry tests.
type stubProvider struct{ name string }

func (s stubProvider) Name() string               { return s.name }
func (s stubProvider) Capabilities() Capabilities { return Capabilities{} }
func (s stubProvider) NewSession() Session        { return nil }

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(stubProvider{name: "claude"})

	p, err := r.Get("claude")
	if err != nil {
		t.Fatalf("Get(claude) returned error: %v", err)
	}
	if p.Name() != "claude" {
		t.Fatalf("got provider %q, want claude", p.Name())
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("Get(nope) expected error, got nil")
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(stubProvider{name: "codex"})
	r.Register(stubProvider{name: "claude"})
	r.Register(stubProvider{name: "gemini"})

	names := r.Names()
	want := []string{"claude", "codex", "gemini"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names() = %v, want sorted %v", names, want)
		}
	}
}

func TestRegistryRegisterOverwrites(t *testing.T) {
	r := NewRegistry()
	r.Register(stubProvider{name: "claude"})
	r.Register(stubProvider{name: "claude"})
	if names := r.Names(); len(names) != 1 {
		t.Fatalf("re-register should overwrite, got %v", names)
	}
}

// Options resolve onto a providerConfig.
func TestOptionsResolve(t *testing.T) {
	cfg := resolveOptions("claude", []Option{
		WithBinary("/custom/claude"),
		WithBaseEnv(map[string]string{"FOO": "bar"}),
		WithModelEnv("ANTHROPIC_MODEL"),
	})
	if cfg.binary != "/custom/claude" {
		t.Fatalf("binary = %q, want /custom/claude", cfg.binary)
	}
	if cfg.baseEnv["FOO"] != "bar" {
		t.Fatalf("baseEnv FOO = %q, want bar", cfg.baseEnv["FOO"])
	}
	if cfg.modelEnv != "ANTHROPIC_MODEL" {
		t.Fatalf("modelEnv = %q, want ANTHROPIC_MODEL", cfg.modelEnv)
	}
}

func TestRegistrySessionStub(t *testing.T) {
	if (stubProvider{}).NewSession() != nil {
		t.Fatal("stub NewSession should be nil")
	}
}

func TestOptionsDefaultBinary(t *testing.T) {
	cfg := resolveOptions("claude", nil)
	if cfg.binary != "claude" {
		t.Fatalf("default binary = %q, want claude", cfg.binary)
	}
}
