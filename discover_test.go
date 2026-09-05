package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeFakeCLI drops an executable script under dir and returns its path. The
// whole suite runs against these: a real `claude` costs money, needs a login,
// and is not installed on a build machine, so nothing here may touch one.
func writeFakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

// isolatePATH empties PATH for the test, so Discover sees only what the test
// put in front of it. Without this the suite's results depend on which agent
// CLIs the developer happens to have installed.
func isolatePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "")
}

func TestDiscoverUsesBinaryOverrideAndReadsVersion(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "claude", "#!/bin/sh\necho '2.1.3 (Claude Code)'\n")

	found := Discover(map[string]string{"claude": binary})
	if len(found) != 1 {
		t.Fatalf("expected only the overridden agent, got %+v", found)
	}
	got := found[0]
	if got.Name != "claude" {
		t.Errorf("name = %q, want claude", got.Name)
	}
	if got.Binary != binary {
		t.Errorf("binary = %q, want %q", got.Binary, binary)
	}
	if got.Version != "2.1.3 (Claude Code)" {
		t.Errorf("version = %q, want the first line of --version output", got.Version)
	}
	if !got.Streaming || !got.Resume {
		t.Errorf("claude should report streaming and resume, got %+v", got)
	}
}

func TestDiscoverSkipsAgentsThatAreNotInstalled(t *testing.T) {
	isolatePATH(t)
	if found := Discover(nil); len(found) != 0 {
		t.Fatalf("expected nothing on an empty PATH, got %+v", found)
	}
	// A stale override pointing at nothing must drop the agent rather than
	// list one whose Binary cannot be executed.
	missing := filepath.Join(t.TempDir(), "not-there")
	if found := Discover(map[string]string{"claude": missing}); len(found) != 0 {
		t.Fatalf("expected a missing override to drop the agent, got %+v", found)
	}
}

func TestDiscoverVersionStaysEmptyWhenTheCLIRefuses(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "gemini", "#!/bin/sh\necho 'not logged in' >&2\nexit 1\n")

	found := Discover(map[string]string{"gemini": binary})
	if len(found) != 1 {
		t.Fatalf("expected the agent to be listed anyway, got %+v", found)
	}
	if found[0].Version != "" {
		t.Errorf("version = %q, want empty when --version failed", found[0].Version)
	}
	if found[0].Resume {
		t.Error("gemini has no session id in its stream, so it must not claim resume")
	}
}

func TestDiscoverPlacesAnAliasOntoItsRealDialect(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	binary := writeFakeCLI(t, dir, "claude-work", "#!/bin/sh\necho 9.9.9\n")

	found := Discover(map[string]string{"claude-work": binary})
	if len(found) != 1 {
		t.Fatalf("expected the alias to be listed, got %+v", found)
	}
	if !found[0].Streaming {
		t.Error("an alias of claude should inherit claude's traits")
	}

	// A name that matches no known CLI is dropped: RegistryFrom could not
	// build a runner for it, and listing an agent nothing can run is a promise
	// broken one call later.
	other := writeFakeCLI(t, dir, "wibble", "#!/bin/sh\nexit 0\n")
	if found := Discover(map[string]string{"wibble": other}); len(found) != 0 {
		t.Fatalf("expected an unplaceable name to be dropped, got %+v", found)
	}
}

func TestResolveTraitsSubstringOrder(t *testing.T) {
	// "copilot" contains "pi"; the longer key must win.
	for alias, want := range map[string]dialect{
		"copilot-work": dialectCopilot,
		"pi-nightly":   dialectPi,
		"qwen-beta":    dialectQwen,
		"my-kimi":      dialectKimi,
		"opencode2":    dialectOpencode,
		"goose-dev":    dialectGoose,
		"hermes-2":     dialectHermes,
		"aider-main":   dialectAider,
		"agy-canary":   dialectAgy,
	} {
		got, ok := resolveTraits(alias, "/opt/bin/"+alias)
		if !ok || got.dialect != want {
			t.Errorf("%s -> %v (ok=%v), want %s", alias, got.dialect, ok, want)
		}
	}
}

func TestRegistryFromBuildsARunnerForEveryDiscoveredAgent(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	// Every builtin, so a dialect added to the table without a constructor
	// fails here rather than at a caller's Get.
	overrides := map[string]string{}
	for name := range builtins {
		overrides[name] = writeFakeCLI(t, dir, name, "#!/bin/sh\nexit 0\n")
	}
	reg := RegistryFrom(Discover(overrides))
	if got := reg.Names(); len(got) != len(builtins) {
		t.Fatalf("registry has %d providers for %d builtins: %v", len(got), len(builtins), got)
	}

	for name, binary := range overrides {
		provider, err := reg.Get(name)
		if err != nil {
			t.Fatalf("registry has no provider for %s: %v", name, err)
		}
		spec, err := provider.NewSession().BuildCommand(context.Background(), Request{
			Prompt:        "say OK",
			WorkspacePath: dir,
		})
		if err != nil {
			t.Fatalf("%s BuildCommand: %v", name, err)
		}
		if spec.Argv[0] != binary {
			t.Errorf("%s argv[0] = %q, want the overridden binary %q", name, spec.Argv[0], binary)
		}
		// gemini passes the prompt through --prompt rather than as the last
		// operand, so this asserts only that it made it into the command.
		if !slices.Contains(spec.Argv, "say OK") {
			t.Errorf("%s argv does not carry the prompt: %v", name, spec.Argv)
		}
	}
}

// Options a caller hands RegistryFrom reach every provider, after the name and
// binary discovery decided. This is how an app injects its own concerns — an
// MCP config filename, a base env — without this library carrying them.
func TestRegistryFromAppliesCallerOptions(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	reg := RegistryFrom(
		Discover(map[string]string{"claude": writeFakeCLI(t, dir, "claude", "#!/bin/sh\nexit 0\n")}),
		WithMCPConfig(".app-mcp.json", true),
	)
	provider, err := reg.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := provider.NewSession().BuildCommand(context.Background(), Request{
		Prompt: "say OK", WorkspacePath: dir, NoMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Argv, filepath.Join(dir, ".app-mcp.json")) {
		t.Errorf("the caller's MCP filename did not reach the provider: %v", spec.Argv)
	}
}

// The headless flags are the difference between a run and a refusal: in a
// scratch directory that is not a trusted git repo, codex and gemini both stop
// and ask. Request.Sandbox's zero value is what emits them, so this asserts we
// are relying on it rather than quietly not passing it.
func TestZeroSandboxEmitsTheBypassFlags(t *testing.T) {
	isolatePATH(t)
	dir := t.TempDir()
	overrides := map[string]string{
		"codex":        writeFakeCLI(t, dir, "codex", "#!/bin/sh\nexit 0\n"),
		"gemini":       writeFakeCLI(t, dir, "gemini", "#!/bin/sh\nexit 0\n"),
		"cursor-agent": writeFakeCLI(t, dir, "cursor-agent", "#!/bin/sh\nexit 0\n"),
	}
	reg := RegistryFrom(Discover(overrides))

	want := map[string]string{
		"codex":        "--skip-git-repo-check",
		"gemini":       "--skip-trust",
		"cursor-agent": "--trust",
	}
	for name, flag := range want {
		provider, err := reg.Get(name)
		if err != nil {
			t.Fatalf("registry has no provider for %s: %v", name, err)
		}
		spec, err := provider.NewSession().BuildCommand(context.Background(), Request{
			Prompt:        "say OK",
			WorkspacePath: dir,
		})
		if err != nil {
			t.Fatalf("%s BuildCommand: %v", name, err)
		}
		if !slices.Contains(spec.Argv, flag) {
			t.Errorf("%s argv is missing %s: %v", name, flag, spec.Argv)
		}
	}
}
