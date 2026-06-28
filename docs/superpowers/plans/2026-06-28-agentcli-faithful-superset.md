# agentcli Faithful Superset Implementation Plan (Phase 2, Plan A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Revise the `agentcli` module so it reproduces `anywhered`'s real headless `claude`/`codex`/`gemini` behavior byte-for-byte (argv, env, events, usage), making it a faithful superset ready for `anywhered` (and later `Agent`) to depend on.

**Architecture:** Revise the existing Phase 1 `cliagent` package + `cliagent/pty` in place. Add `Request` fields (`PermissionMode`, `Sandbox`, `ExtraMCPServers`), widen `Capabilities`, add options (`WithName`, `WithMCPConfig`, `WithAllowedModes`) and change `WithModelEnv` semantics. Rewrite the three providers' `BuildCommand`/event-mapping/usage to match the real source verbatim, reconcile `mapJSONLines`/`plugin`/`pty`, and rewrite the Phase 1 tests to the new contract with golden argv/env assertions.

**Tech Stack:** Go 1.25, `github.com/creack/pty`, standard `testing`.

**Source of truth (port these verbatim, adjusting package/names):**
- `/Users/liliang/Things/AI/projects/anywhere/anywhered/internal/provider/claude.go`
- `/Users/liliang/Things/AI/projects/anywhere/anywhered/internal/provider/codex.go`
- `/Users/liliang/Things/AI/projects/anywhere/anywhered/internal/provider/gemini.go`
- `/Users/liliang/Things/AI/projects/anywhere/anywhered/internal/provider/plugin.go`
- `/Users/liliang/Things/AI/projects/anywhere/anywhered/internal/runtime/pty.go`

**Working directory:** `/Users/liliang/Things/AI/projects/agentcli`. All `go test`/`git` commands run from there.

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `cliagent/types.go` | `Request` (+ new fields), `PermissionMode`, widened `Capabilities`, `Event`, `Result`, `Usage`, `CommandSpec`, `PluginRef`, event consts | Modify |
| `cliagent/provider.go` | `Registry`, options (`WithName`/`WithMCPConfig`/`WithAllowedModes`/changed `WithModelEnv`), `providerConfig`, `ErrUnsupportedMode` | Modify |
| `cliagent/internal.go` | `mergeEnv` (drop-empty-base semantics), JSON map accessors | Modify |
| `cliagent/jsonlines.go` | `mapJSONLines` → `terminal.output{"line"}` | Modify |
| `cliagent/plugin.go` | `loadPluginMCPServers(plugins,env)`, `resolveRefs` (uppercase-only), `writeMCPConfig(dir,filename,servers)` | Rewrite |
| `cliagent/claude.go` | claude provider/session — real argv/events/usage | Rewrite |
| `cliagent/codex.go` | codex provider/session — real argv/events/usage | Rewrite |
| `cliagent/gemini.go` | gemini provider/session — real argv/events/usage | Rewrite |
| `cliagent/pty/pty.go` | PTY runner — setsid, winsize, SIGINT→SIGKILL | Modify |
| `cliagent/*_test.go`, `cliagent/pty/pty_test.go` | rewritten to the new contract + golden argv/env | Rewrite |

---

## Task 1: Types, options, errors

**Files:**
- Modify: `cliagent/types.go`
- Modify: `cliagent/provider.go`
- Modify: `cliagent/provider_test.go`

This task is **purely additive** — it adds fields/options without changing any
existing signature, so the Phase 1 provider files keep compiling. The
cross-cutting `mergeEnv` signature change is deferred to Task 4 (the first
provider rewrite), where the other providers' call sites are updated in the same
commit.

- [ ] **Step 1: Rewrite the options/registry test to the new contract**

Replace the body of `cliagent/provider_test.go` with:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run 'Registry|Options' 2>&1 | head`
Expected: FAIL — `undefined: WithName`, `cfg.name`, `cfg.mcpFilename`, `cfg.strictMCP`, `cfg.allowedModes`, `WithMCPConfig`, `WithAllowedModes`.

- [ ] **Step 3: Add the new `Request` fields, `PermissionMode`, widen `Capabilities`**

In `cliagent/types.go`, add inside `Request` (after `Plugins`):

```go
	ExtraMCPServers map[string]any // caller-injected MCP servers, merged before plugin servers
	PermissionMode  PermissionMode // PermissionDefault | PermissionBypass
	Sandbox         bool           // true = sandboxed (default); false = emit skip-sandbox/trust/git-check flags
```

Add the enum near the top of `types.go` (after the package/import block):

```go
// PermissionMode selects whether a provider bypasses its approval prompts.
type PermissionMode string

const (
	PermissionDefault PermissionMode = ""
	PermissionBypass  PermissionMode = "bypass"
)
```

Replace the `Capabilities` struct with the widened superset:

```go
// Capabilities describes which app-agnostic features a provider supports.
type Capabilities struct {
	Streaming         bool
	Resume            bool
	Plugins           bool
	MCP               bool
	SupportsPTY       bool
	RequiresWorkspace bool
}
```

- [ ] **Step 4: Add options, `providerConfig` fields, `ErrUnsupportedMode`, change `WithModelEnv` doc**

In `cliagent/provider.go`, replace the `providerConfig` struct and the options block with:

```go
// providerConfig holds resolved construction options shared by all providers.
type providerConfig struct {
	name         string
	binary       string
	baseEnv      map[string]string
	modelEnv     string   // env key to source the model value from (emitted as the provider's model flag)
	mcpFilename  string   // merged MCP config filename written under WorkspacePath
	strictMCP    bool     // append the provider's strict-mcp flag when a config is written
	allowedModes []string // when non-empty, BuildCommand validates Request.Mode against this
}

// Option configures a provider constructor.
type Option func(*providerConfig)

// WithBinary overrides the CLI binary path/name.
func WithBinary(path string) Option { return func(c *providerConfig) { c.binary = path } }

// WithBaseEnv sets a base environment applied to every command.
func WithBaseEnv(env map[string]string) Option {
	return func(c *providerConfig) {
		c.baseEnv = make(map[string]string, len(env))
		maps.Copy(c.baseEnv, env)
	}
}

// WithModelEnv names the Request.Env key to source the model value from. The
// resolved value is emitted as the provider's model flag (e.g. claude --model).
func WithModelEnv(key string) Option { return func(c *providerConfig) { c.modelEnv = key } }

// WithName overrides the registered provider name.
func WithName(name string) Option { return func(c *providerConfig) { c.name = name } }

// WithMCPConfig sets the merged MCP config filename written under WorkspacePath
// and whether to append the provider's strict-mcp flag.
func WithMCPConfig(filename string, strict bool) Option {
	return func(c *providerConfig) { c.mcpFilename = filename; c.strictMCP = strict }
}

// WithAllowedModes restricts Request.Mode; an unlisted mode yields ErrUnsupportedMode.
func WithAllowedModes(modes []string) Option {
	return func(c *providerConfig) { c.allowedModes = append([]string(nil), modes...) }
}

// resolveOptions applies opts onto a config defaulting name+binary to def.
func resolveOptions(def string, opts []Option) providerConfig {
	cfg := providerConfig{name: def, binary: def, baseEnv: map[string]string{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
```

Add at the top of `provider.go` (after imports):

```go
// ErrUnsupportedMode is returned by BuildCommand when Request.Mode is not allowed.
var ErrUnsupportedMode = errors.New("cliagent: unsupported mode")
```

Ensure `provider.go` imports `errors` and `maps` (and keeps `fmt`, `sort`).

- [ ] **Step 5: Run the test to verify it passes (additive only — package still compiles)**

Run: `go test ./cliagent/ -run 'Registry|Options' -v 2>&1 | tail -20`
Expected: PASS for all `TestRegistry*` and `TestOptions*`. Because this task only
adds fields/options, the existing Phase 1 provider files and their tests still
compile and pass; `mergeEnv` keeps its Phase 1 4-arg signature until Task 4.

- [ ] **Step 6: Commit**

```bash
git add cliagent/types.go cliagent/provider.go cliagent/provider_test.go
git commit -m "agentcli: add PermissionMode/Sandbox/ExtraMCPServers, widen Capabilities, new options"
```

---

## Task 2: mapJSONLines → terminal.output {"line"}

**Files:**
- Modify: `cliagent/jsonlines.go`
- Modify: `cliagent/jsonlines_test.go`

- [ ] **Step 1: Rewrite the test to the new contract**

Replace `cliagent/jsonlines_test.go` with:

```go
package cliagent

import "testing"

func TestMapJSONLinesDispatchesObjects(t *testing.T) {
	mapper := func(o map[string]any) []Event {
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": o["role"]}}}
	}
	ev := mapJSONLines([]string{`{"role":"assistant"}`}, mapper)
	if len(ev) != 1 || ev[0].Payload["role"] != "assistant" {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesNonJSONUsesLineKey(t *testing.T) {
	ev := mapJSONLines([]string{"plain log"}, func(map[string]any) []Event { return nil })
	if len(ev) != 1 || ev[0].Type != EventTerminalOutput || ev[0].Payload["line"] != "plain log" {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesSkipsBlank(t *testing.T) {
	if ev := mapJSONLines([]string{""}, func(map[string]any) []Event { return nil }); len(ev) != 0 {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesArrayLeadingTreatedAsLine(t *testing.T) {
	// A '['-leading line is not an object; emit as terminal output.
	ev := mapJSONLines([]string{`[1,2]`}, func(map[string]any) []Event { return nil })
	if len(ev) != 1 || ev[0].Type != EventTerminalOutput || ev[0].Payload["line"] != "[1,2]" {
		t.Fatalf("ev = %v", ev)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run MapJSONLines 2>&1 | head`
Expected: FAIL — payload key is `"text"`, and the `[`-leading line is parsed/mapped rather than emitted as a line.

- [ ] **Step 3: Rewrite `mapJSONLines`**

Replace `cliagent/jsonlines.go` with:

```go
package cliagent

import (
	"encoding/json"
	"strings"
)

// mapJSONLines routes each line to mapper if it is a JSON object, otherwise
// emits it as a terminal-output event keyed "line". Blank lines are skipped.
// Lines that do not begin with '{' are treated as terminal output (matching the
// real CLI wrappers, which only attempt to parse object-leading lines).
func mapJSONLines(lines []string, mapper func(map[string]any) []Event) []Event {
	if len(lines) == 0 {
		return nil
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != '{' {
			out = append(out, Event{Type: EventTerminalOutput, Payload: map[string]any{"line": line}})
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
			out = append(out, Event{Type: EventTerminalOutput, Payload: map[string]any{"line": line}})
			continue
		}
		out = append(out, mapper(obj)...)
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cliagent/ -run MapJSONLines 2>&1 | tail`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cliagent/jsonlines.go cliagent/jsonlines_test.go
git commit -m "agentcli: mapJSONLines emits terminal.output keyed line, only parses {-leading"
```

---

## Task 3: plugin.go — env-aware loader, uppercase refs, filename arg

**Files:**
- Rewrite: `cliagent/plugin.go`
- Rewrite: `cliagent/plugin_test.go`

- [ ] **Step 1: Rewrite the test to the new contract**

Replace `cliagent/plugin_test.go` with:

```go
package cliagent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMCP(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPluginMCPServersMergesAndResolves(t *testing.T) {
	p1 := t.TempDir()
	writeMCP(t, p1, `{"mcpServers":{"a":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/a","env":{"K":"${TOKEN}"}}}}`)
	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}}, map[string]string{"TOKEN": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	a := servers["a"].(map[string]any)
	if a["command"] != p1+"/bin/a" {
		t.Fatalf("CLAUDE_PLUGIN_ROOT not resolved: %v", a["command"])
	}
	if a["env"].(map[string]any)["K"] != "secret" {
		t.Fatalf("TOKEN not resolved: %v", a["env"])
	}
}

func TestLoadPluginMCPServersMissingDirIsError(t *testing.T) {
	if _, err := loadPluginMCPServers([]PluginRef{{Name: "x", Path: "/no/such/dir"}}, nil); err == nil {
		t.Fatal("missing plugin dir should error")
	}
}

func TestLoadPluginMCPServersEmptyPathIsError(t *testing.T) {
	if _, err := loadPluginMCPServers([]PluginRef{{Name: "x", Path: ""}}, nil); err == nil {
		t.Fatal("empty path should error")
	}
}

func TestLoadPluginMCPServersNoMCPFileSkips(t *testing.T) {
	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p", Path: t.TempDir()}}, nil)
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers=%v err=%v", servers, err)
	}
}

func TestResolveRefsUppercaseOnly(t *testing.T) {
	// lowercase ${token} must NOT be substituted (uppercase-only pattern).
	got := resolveRefs(map[string]any{"c": "${token}-${UP}"}, map[string]string{"token": "x", "UP": "y"})
	if got.(map[string]any)["c"] != "${token}-y" {
		t.Fatalf("got %v", got.(map[string]any)["c"])
	}
}

func TestWriteMCPConfigUsesFilenameAndAbsPath(t *testing.T) {
	dir := t.TempDir()
	path, err := writeMCPConfig(dir, ".aas-mcp.json", map[string]any{"a": map[string]any{"command": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != ".aas-mcp.json" || !filepath.IsAbs(path) {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run 'Plugin|ResolveRefs|MCPConfig' 2>&1 | head`
Expected: FAIL — `loadPluginMCPServers` takes 1 arg, `writeMCPConfig` takes 2 args, lowercase refs resolve.

- [ ] **Step 3: Rewrite `plugin.go`**

Replace `cliagent/plugin.go` with (ported from `anywhered/internal/provider/plugin.go`, plus the filename-arg `writeMCPConfig` and the public `WritePluginMCPConfig`):

```go
package cliagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const pluginMCPFile = ".mcp.json"

// loadPluginMCPServers reads each plugin's .mcp.json (if present), resolves
// ${CLAUDE_PLUGIN_ROOT} and ${ENV_VAR} references against env, and returns a
// merged mcpServers map. A missing plugin directory (or empty Path) is an error;
// a plugin without .mcp.json contributes nothing. Last write wins.
func loadPluginMCPServers(plugins []PluginRef, env map[string]string) (map[string]any, error) {
	merged := map[string]any{}
	for _, p := range plugins {
		if p.Path == "" {
			return nil, fmt.Errorf("plugin %q has empty Path", p.Name)
		}
		info, err := os.Stat(p.Path)
		if err != nil {
			return nil, fmt.Errorf("plugin %q at %s: %w", p.Name, p.Path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("plugin %q at %s: not a directory", p.Name, p.Path)
		}
		raw, err := os.ReadFile(filepath.Join(p.Path, pluginMCPFile))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s/.mcp.json: %w", p.Path, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s/.mcp.json: %w", p.Path, err)
		}
		servers, _ := parsed["mcpServers"].(map[string]any)
		if servers == nil {
			continue
		}
		vars := make(map[string]string, len(env)+1)
		for k, v := range env {
			vars[k] = v
		}
		vars["CLAUDE_PLUGIN_ROOT"] = p.Path
		resolved := resolveRefs(servers, vars).(map[string]any)
		for name, server := range resolved {
			merged[name] = server
		}
	}
	return merged, nil
}

// refPattern matches ${VAR_NAME} (uppercase + underscore only).
var refPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// resolveRefs walks a parsed JSON value substituting ${VAR} references in
// strings in place. Unknown variables stay literal.
func resolveRefs(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		return refPattern.ReplaceAllStringFunc(x, func(m string) string {
			key := m[2 : len(m)-1]
			if val, ok := vars[key]; ok {
				return val
			}
			return m
		})
	case map[string]any:
		for k, vv := range x {
			x[k] = resolveRefs(vv, vars)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = resolveRefs(vv, vars)
		}
		return x
	default:
		return v
	}
}

// writeMCPConfig writes {"mcpServers": servers} to dir/filename (0600) and
// returns the absolute path.
func writeMCPConfig(dir, filename string, servers map[string]any) (string, error) {
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, filename)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs, nil
	}
	return p, nil
}

// WritePluginMCPConfig loads + resolves plugin MCP servers and writes a merged
// config into outDir/filename, returning its path (empty if nothing to write).
func WritePluginMCPConfig(plugins []PluginRef, env map[string]string, outDir, filename string) (string, error) {
	servers, err := loadPluginMCPServers(plugins, env)
	if err != nil {
		return "", err
	}
	if len(servers) == 0 {
		return "", nil
	}
	return writeMCPConfig(outDir, filename, servers)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cliagent/ -run 'Plugin|ResolveRefs|MCPConfig' 2>&1 | tail`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cliagent/plugin.go cliagent/plugin_test.go
git commit -m "agentcli: plugin loader takes env (CLAUDE_PLUGIN_ROOT), uppercase-only refs, filename arg"
```

---

## Task 4: claude provider — real argv, events, usage

**Files:**
- Modify: `cliagent/internal.go` (mergeEnv → 2-arg)
- Modify: `cliagent/codex.go`, `cliagent/gemini.go` (mechanical call-site update only; full rewrites in Tasks 5–6)
- Rewrite: `cliagent/claude.go`
- Rewrite: `cliagent/claude_test.go`

This task changes the cross-cutting `mergeEnv` signature. To keep the package
compiling, it also mechanically updates the still-Phase-1 `codex.go`/`gemini.go`
call sites (their behavior is unaffected — they passed empty model args before).

- [ ] **Step 1: Write the golden + behavior tests**

Replace `cliagent/claude_test.go` with:

```go
package cliagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newClaudeAAS() Session {
	return NewClaude(
		WithName("claude-code"),
		WithModelEnv("CLAUDE_MODEL"),
		WithMCPConfig(".aas-mcp.json", true),
		WithAllowedModes([]string{"headless-code", "terminal-task"}),
	).NewSession()
}

func TestClaudeMeta(t *testing.T) {
	p := NewClaude(WithName("claude-code"))
	if p.Name() != "claude-code" {
		t.Fatalf("name=%q", p.Name())
	}
	c := p.Capabilities()
	if !c.Streaming || !c.Resume || !c.Plugins || !c.MCP || !c.SupportsPTY || !c.RequiresWorkspace {
		t.Fatalf("caps=%+v", c)
	}
}

func TestClaudeGoldenArgv(t *testing.T) {
	spec, err := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", WorkspacePath: "/work",
		PermissionMode: PermissionBypass,
		Env:            map[string]string{"CLAUDE_MODEL": "opus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude", "--print", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions", "--model", "opus", "do it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
	if spec.WorkDir != "/work" {
		t.Fatalf("workdir=%q", spec.WorkDir)
	}
}

func TestClaudeUnsupportedMode(t *testing.T) {
	_, err := newClaudeAAS().BuildCommand(context.Background(), Request{Mode: "nope", Prompt: "x"})
	if err == nil {
		t.Fatal("expected ErrUnsupportedMode")
	}
}

func TestClaudeResumeWinsOverContinue(t *testing.T) {
	spec, _ := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", ResumeSessionID: "s1", Continue: true,
	})
	if !slices.Contains(spec.Argv, "--resume") || slices.Contains(spec.Argv, "--continue") {
		t.Fatalf("argv=%v", spec.Argv)
	}
}

func TestClaudeMCPMergeWritesConfigAndStrict(t *testing.T) {
	ws := t.TempDir()
	plug := t.TempDir()
	writeMCP(t, plug, `{"mcpServers":{"p":{"command":"pc"}}}`)
	spec, err := newClaudeAAS().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", WorkspacePath: ws,
		ExtraMCPServers: map[string]any{"aas": map[string]any{"command": "exe"}},
		Plugins:         []PluginRef{{Name: "p", Path: plug}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Argv, "--mcp-config") || !slices.Contains(spec.Argv, "--strict-mcp-config") {
		t.Fatalf("argv=%v", spec.Argv)
	}
	if !slices.Contains(spec.Argv, "--plugin-dir") {
		t.Fatalf("argv missing --plugin-dir: %v", spec.Argv)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".aas-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"aas"`) || !contains(string(data), `"p"`) {
		t.Fatalf("merged config = %s", data)
	}
}

func TestClaudeEnvDropsEmptyBaseKeepsTimeout(t *testing.T) {
	s := NewClaude(WithName("claude-code"), WithBaseEnv(map[string]string{
		"MCP_TOOL_TIMEOUT": "1800000", "EMPTY": "",
	}), WithAllowedModes([]string{"headless-code"})).NewSession()
	spec, _ := s.BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "p"})
	if !slices.Contains(spec.Env, "MCP_TOOL_TIMEOUT=1800000") {
		t.Fatalf("env=%v", spec.Env)
	}
	for _, e := range spec.Env {
		if e == "EMPTY=" {
			t.Fatalf("empty base value should be dropped: %v", spec.Env)
		}
	}
}

func TestClaudeParseAssistantTextAndToolCall(t *testing.T) {
	s := newClaudeAAS()
	line := `{"type":"assistant","session_id":"s9","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},{"type":"text","text":"hi"}]}}` + "\n"
	ev, _ := s.ParseChunk([]byte(line))
	if findEvent(ev, EventToolCall) == nil {
		t.Fatalf("no tool_call: %v", ev)
	}
	msg := findEvent(ev, EventAgentMessage)
	if msg == nil || msg.Payload["text"] != "hi" {
		t.Fatalf("msg=%v", msg)
	}
	if s.SessionID() != "s9" {
		t.Fatalf("sid=%q", s.SessionID())
	}
}

func TestClaudeSystemFrameCarriesRaw(t *testing.T) {
	s := newClaudeAAS()
	ev, _ := s.ParseChunk([]byte(`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}` + "\n"))
	m := findEvent(ev, EventAgentMessage)
	if m == nil || m.Payload["role"] != "system" || m.Payload["raw"] == nil {
		t.Fatalf("system event=%v", m)
	}
}

func TestClaudeUsageResultCanonical(t *testing.T) {
	s := newClaudeAAS()
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(),
		[]byte(`{"type":"result","result":"done","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2},"total_cost_usd":0.05}`+"\n"), 0)
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 4 || res.Usage.CacheTokens != 2 || res.Usage.EstimatedCostUSD != 0.05 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestClaudeUsageFallbackWithoutResult(t *testing.T) {
	s := newClaudeAAS()
	s.ParseChunk([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n"))
	res, _, _ := s.Finalize(context.Background(), nil, 0)
	if res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 1 {
		t.Fatalf("fallback usage=%+v", res.Usage)
	}
}
```

Also add these shared test helpers in a new file `cliagent/helpers_test.go` (used
across provider tests). `assertContainsPair` is included so the still-Phase-1
`codex_test.go`/`gemini_test.go` keep compiling until Tasks 5–6 replace them
(it becomes unused afterward, which Go permits for package-level functions):

```go
package cliagent

import "testing"

func findEvent(events []Event, typ string) *Event {
	for i := range events {
		if events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func assertContainsPair(t *testing.T, argv []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return
		}
	}
	t.Fatalf("argv missing %q %q: %v", flag, val, argv)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run Claude 2>&1 | head`
Expected: FAIL — the old claude implementation produces `-p` (not `--print`), stdin prompt, no `--permission-mode`, no `--strict-mcp-config`, system frame returns nil, wrong usage source.

- [ ] **Step 3: Change `mergeEnv` to 2-arg and fix the codex/gemini call sites**

In `cliagent/internal.go`, replace `mergeEnv` with the 2-arg version:

```go
// mergeEnv combines the provider base env and the request env into a sorted
// "KEY=VALUE" slice. Empty base values are dropped; Request.Env overrides base.
func mergeEnv(base, reqEnv map[string]string) []string {
	merged := make(map[string]string, len(base)+len(reqEnv))
	for k, v := range base {
		if v != "" {
			merged[k] = v
		}
	}
	maps.Copy(merged, reqEnv)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
```

Then, in the still-Phase-1 `cliagent/codex.go` and `cliagent/gemini.go`, change
each `mergeEnv(s.cfg.baseEnv, req.Env, s.cfg.modelEnv, req.Model)` call to
`mergeEnv(s.cfg.baseEnv, req.Env)`. (These files are fully rewritten in Tasks 5–6;
this is only to keep the package compiling now.)

- [ ] **Step 4: Rewrite `claude.go`**

Replace `cliagent/claude.go` with (ported from `anywhered/internal/provider/claude.go`, parameterized by `providerConfig`):

```go
package cliagent

import "context"

type claudeProvider struct{ cfg providerConfig }

// NewClaude returns a Claude Code Provider.
func NewClaude(opts ...Option) Provider { return &claudeProvider{cfg: resolveOptions("claude", opts)} }

func (p *claudeProvider) Name() string { return p.cfg.name }

func (p *claudeProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, Plugins: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *claudeProvider) NewSession() Session {
	return &claudeSession{cfg: p.cfg, lb: &LineBuffer{}}
}

type claudeSession struct {
	cfg       providerConfig
	lb        *LineBuffer
	usage     Usage
	fallback  Usage
	sawResult bool
	sessionID string
	summary   string
}

func (s *claudeSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !sliceContains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	argv := []string{s.cfg.binary, "--print", "--output-format", "stream-json", "--verbose"}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--permission-mode", "bypassPermissions")
	}
	if model := s.resolveModel(req); model != "" {
		argv = append(argv, "--model", model)
	}

	// Merge MCP servers: ExtraMCPServers first, then plugin .mcp.json (last wins).
	servers := map[string]any{}
	for k, v := range req.ExtraMCPServers {
		servers[k] = v
	}
	if len(req.Plugins) > 0 {
		pluginServers, err := loadPluginMCPServers(req.Plugins, req.Env)
		if err != nil {
			return CommandSpec{}, err
		}
		for k, v := range pluginServers {
			servers[k] = v
		}
	}
	if len(servers) > 0 && req.WorkspacePath != "" {
		filename := s.cfg.mcpFilename
		if filename == "" {
			filename = ".mcp-config.json"
		}
		if mcpPath, err := writeMCPConfig(req.WorkspacePath, filename, servers); err == nil {
			argv = append(argv, "--mcp-config", mcpPath)
			if s.cfg.strictMCP {
				argv = append(argv, "--strict-mcp-config")
			}
		}
	}
	for _, p := range req.Plugins {
		if p.Path != "" {
			argv = append(argv, "--plugin-dir", p.Path)
		}
	}

	if req.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.ResumeSessionID)
	} else if req.Continue {
		argv = append(argv, "--continue")
	}
	if req.SystemPrompt != "" {
		argv = append(argv, "--append-system-prompt", req.SystemPrompt)
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, req.Prompt)

	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *claudeSession) resolveModel(req Request) string {
	if req.Model != "" {
		return req.Model
	}
	if s.cfg.modelEnv != "" {
		return req.Env[s.cfg.modelEnv]
	}
	return ""
}

func (s *claudeSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapClaudeEventWithUsage), nil
}

func (s *claudeSession) SessionID() string { return s.sessionID }

func (s *claudeSession) Finalize(_ context.Context, full []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(full)
	lines = append(lines, s.lb.Flush()...)
	tail := mapJSONLines(lines, s.mapClaudeEventWithUsage)
	final := s.usage
	if !s.sawResult {
		final.InputTokens = s.fallback.InputTokens
		final.OutputTokens = s.fallback.OutputTokens
		final.CacheTokens = s.fallback.CacheTokens
	}
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: final}, tail, nil
}

func (s *claudeSession) mapClaudeEventWithUsage(obj map[string]any) []Event {
	if s.sessionID == "" {
		if sid, _ := obj["session_id"].(string); sid != "" {
			s.sessionID = sid
		}
	}
	if t, _ := obj["type"].(string); t == "result" {
		s.sawResult = true
		s.summary = mapString(obj, "result")
		setUsageFromObject(obj, &s.usage)
	} else if msg, ok := obj["message"].(map[string]any); ok {
		if model, _ := msg["model"].(string); model != "" && s.usage.Model == "" {
			s.usage.Model = model
		}
		if !s.sawResult {
			addUsageFromObject(msg, &s.fallback)
		}
	}
	return mapClaudeEvent(obj)
}

func mapClaudeEvent(obj map[string]any) []Event {
	t, _ := obj["type"].(string)
	switch t {
	case "system":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "assistant":
		text, tools := extractAssistantContent(obj)
		out := make([]Event, 0, len(tools)+1)
		for _, tc := range tools {
			out = append(out, Event{Type: EventToolCall, Payload: tc})
		}
		if text != "" {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}})
		}
		if len(out) == 0 {
			out = append(out, Event{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "raw": obj}})
		}
		return out
	case "user":
		return []Event{{Type: EventToolResult, Payload: obj}}
	case "result":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "rate_limit_event":
		info, _ := obj["rate_limit_info"].(map[string]any)
		if info == nil {
			return nil
		}
		payload := map[string]any{"raw": info}
		if v, ok := info["status"].(string); ok {
			payload["status"] = v
		}
		if v, ok := info["rateLimitType"].(string); ok {
			payload["rate_limit_type"] = v
		}
		if v, ok := info["resetsAt"].(float64); ok {
			payload["resets_at"] = int64(v)
		}
		if v, ok := info["overageStatus"].(string); ok {
			payload["overage_status"] = v
		}
		if v, ok := info["isUsingOverage"].(bool); ok {
			payload["is_using_overage"] = v
		}
		return []Event{{Type: EventRateLimit, Payload: payload}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func extractAssistantContent(obj map[string]any) (string, []map[string]any) {
	msg, _ := obj["message"].(map[string]any)
	if msg == nil {
		return "", nil
	}
	content, _ := msg["content"].([]any)
	var text string
	var tools []map[string]any
	for _, c := range content {
		item, _ := c.(map[string]any)
		if item == nil {
			continue
		}
		switch item["type"] {
		case "text":
			if t, ok := item["text"].(string); ok {
				if text != "" {
					text += "\n"
				}
				text += t
			}
		case "tool_use":
			tools = append(tools, item)
		}
	}
	return text, tools
}

func setUsageFromObject(obj map[string]any, into *Usage) {
	if cost, ok := obj["total_cost_usd"].(float64); ok && cost > 0 {
		into.EstimatedCostUSD = cost
	}
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		into.InputTokens = int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		into.OutputTokens = int64(v)
	}
	cache := int64(0)
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		cache += int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		cache += int64(v)
	}
	into.CacheTokens = cache
}

func addUsageFromObject(obj map[string]any, into *Usage) {
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		into.InputTokens += int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		into.OutputTokens += int64(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		into.CacheTokens += int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		into.CacheTokens += int64(v)
	}
}

// sliceContains reports whether s contains v.
func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

Note: `mapString` already exists in `internal.go`. The event consts
(`EventToolResult`/`EventToolCall`/`EventAgentMessage`/`EventRateLimit`) are
unchanged from Phase 1.

- [ ] **Step 5: Run the tests to verify claude passes and the package compiles**

Run: `go test ./cliagent/ -run 'Claude|Codex|Gemini' 2>&1 | tail`
Expected: PASS — the new `Claude*` tests pass; the still-Phase-1 `Codex*`/`Gemini*`
tests also pass (the mergeEnv call-site change is behavior-neutral for them).

- [ ] **Step 6: Commit**

```bash
git add cliagent/internal.go cliagent/codex.go cliagent/gemini.go cliagent/claude.go cliagent/claude_test.go cliagent/helpers_test.go
git commit -m "agentcli: rewrite claude provider to real argv/events/usage; mergeEnv->2-arg"
```

---

## Task 5: codex provider — real argv, events, usage

**Files:**
- Rewrite: `cliagent/codex.go`
- Rewrite: `cliagent/codex_test.go`

- [ ] **Step 1: Write the golden + behavior tests**

Replace `cliagent/codex_test.go` with:

```go
package cliagent

import (
	"context"
	"slices"
	"testing"
)

func newCodex() Session {
	return NewCodex(WithName("codex"), WithAllowedModes([]string{"headless-code", "terminal-task"})).NewSession()
}

func TestCodexMeta(t *testing.T) {
	p := NewCodex(WithName("codex"))
	if p.Name() != "codex" || !p.Capabilities().Streaming {
		t.Fatalf("meta wrong")
	}
}

func TestCodexGoldenArgv(t *testing.T) {
	spec, err := newCodex().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", WorkspacePath: "/w",
		PermissionMode: PermissionBypass, Sandbox: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--", "do it"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
}

func TestCodexResumeArgv(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "p", ResumeSessionID: "th7", Sandbox: false,
	})
	want := []string{"codex", "exec", "resume", "th7", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--", "p"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%v", spec.Argv)
	}
}

func TestCodexSandboxedOmitsBypass(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "p", Sandbox: true})
	if slices.Contains(spec.Argv, "--dangerously-bypass-approvals-and-sandbox") || slices.Contains(spec.Argv, "--skip-git-repo-check") {
		t.Fatalf("sandboxed run should omit bypass flags: %v", spec.Argv)
	}
}

func TestCodexSystemPromptPrepended(t *testing.T) {
	spec, _ := newCodex().BuildCommand(context.Background(), Request{Mode: "headless-code", Prompt: "go", SystemPrompt: "be careful", Sandbox: true})
	if spec.Argv[len(spec.Argv)-1] != "be careful\n\ngo" {
		t.Fatalf("prompt=%q", spec.Argv[len(spec.Argv)-1])
	}
}

func TestCodexAssistantAndUsage(t *testing.T) {
	s := newCodex()
	s.ParseChunk([]byte(`{"type":"thread.started","thread_id":"th9"}` + "\n"))
	ev, _ := s.ParseChunk([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"All set"}}` + "\n"))
	if m := findEvent(ev, EventAgentMessage); m == nil || m.Payload["text"] != "All set" {
		t.Fatalf("msg=%v", ev)
	}
	res, _, _ := s.Finalize(context.Background(),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":40,"cached_input_tokens":12,"reasoning_output_tokens":8}}`+"\n"), 0)
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 48 || res.Usage.CacheTokens != 12 {
		t.Fatalf("usage=%+v", res.Usage)
	}
	if s.SessionID() != "th9" {
		t.Fatalf("sid=%q", s.SessionID())
	}
}

func TestCodexCommandExecutionIsToolCall(t *testing.T) {
	s := newCodex()
	ev, _ := s.ParseChunk([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}` + "\n"))
	if findEvent(ev, EventToolCall) == nil {
		t.Fatalf("no tool_call: %v", ev)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run Codex 2>&1 | head`
Expected: FAIL — old codex omits `--skip-git-repo-check`/`--dangerously-bypass-approvals-and-sandbox`/`--`, and folds reasoning tokens differently.

- [ ] **Step 3: Rewrite `codex.go`**

Replace `cliagent/codex.go` with (ported from `anywhered/internal/provider/codex.go`):

```go
package cliagent

import "context"

type codexProvider struct{ cfg providerConfig }

// NewCodex returns a Codex Provider.
func NewCodex(opts ...Option) Provider { return &codexProvider{cfg: resolveOptions("codex", opts)} }

func (p *codexProvider) Name() string { return p.cfg.name }

func (p *codexProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Resume: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *codexProvider) NewSession() Session { return &codexSession{cfg: p.cfg, lb: &LineBuffer{}} }

type codexSession struct {
	cfg      providerConfig
	lb       *LineBuffer
	usage    Usage
	threadID string
	summary  string
}

func (s *codexSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !sliceContains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}
	argv := []string{s.cfg.binary, "exec"}
	if req.ResumeSessionID != "" {
		argv = append(argv, "resume", req.ResumeSessionID)
	}
	argv = append(argv, "--json")
	if !req.Sandbox {
		argv = append(argv, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox")
	} else if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	}
	argv = append(argv, req.ExtraArgs...)
	argv = append(argv, "--", prompt)
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *codexSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapCodexEvent), nil
}

func (s *codexSession) SessionID() string { return s.threadID }

func (s *codexSession) Finalize(_ context.Context, full []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(full)
	lines = append(lines, s.lb.Flush()...)
	tail := mapJSONLines(lines, s.mapCodexEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, tail, nil
}

func (s *codexSession) mapCodexEvent(obj map[string]any) []Event {
	if s.threadID == "" {
		if tid, _ := obj["thread_id"].(string); tid != "" {
			s.threadID = tid
		}
	}
	t, _ := obj["type"].(string)
	switch t {
	case "turn.completed":
		captureCodexUsage(obj, &s.usage)
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "thread.started", "turn.started":
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "item.completed":
		item, _ := obj["item"].(map[string]any)
		if item == nil {
			return []Event{{Type: EventAgentMessage, Payload: obj}}
		}
		switch it, _ := item["type"].(string); it {
		case "agent_message":
			text, _ := item["text"].(string)
			s.summary = text
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text}}}
		case "function_call", "tool_call", "command_execution":
			return []Event{{Type: EventToolCall, Payload: item}}
		case "function_call_output", "tool_result", "command_output":
			return []Event{{Type: EventToolResult, Payload: item}}
		default:
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": it, "raw": item}}}
		}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func captureCodexUsage(obj map[string]any, into *Usage) {
	usage, _ := obj["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		into.InputTokens = int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		into.OutputTokens = int64(v)
	}
	if v, ok := usage["cached_input_tokens"].(float64); ok {
		into.CacheTokens = int64(v)
	}
	if v, ok := usage["reasoning_output_tokens"].(float64); ok {
		into.OutputTokens += int64(v)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cliagent/ -run Codex 2>&1 | tail`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cliagent/codex.go cliagent/codex_test.go
git commit -m "agentcli: rewrite codex provider to real argv/events/usage with golden tests"
```

---

## Task 6: gemini provider — real argv, events, usage

**Files:**
- Rewrite: `cliagent/gemini.go`
- Rewrite: `cliagent/gemini_test.go`

- [ ] **Step 1: Write the golden + behavior tests**

Replace `cliagent/gemini_test.go` with:

```go
package cliagent

import (
	"context"
	"slices"
	"testing"
)

func newGemini() Session {
	return NewGemini(WithName("gemini-cli"), WithAllowedModes([]string{"headless-code", "terminal-task"})).NewSession()
}

func TestGeminiMeta(t *testing.T) {
	if NewGemini(WithName("gemini-cli")).Name() != "gemini-cli" {
		t.Fatal("name")
	}
}

func TestGeminiGoldenArgv(t *testing.T) {
	spec, err := newGemini().BuildCommand(context.Background(), Request{
		Mode: "headless-code", Prompt: "do it", PermissionMode: PermissionBypass, Sandbox: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini", "--prompt", "do it", "--output-format", "stream-json", "--yolo", "--skip-trust"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=\n%v\nwant\n%v", spec.Argv, want)
	}
}

func TestGeminiInitAndResult(t *testing.T) {
	s := newGemini()
	ev, _ := s.ParseChunk([]byte(`{"type":"init","session_id":"x","model":"auto-gemini-3"}` + "\n"))
	if m := findEvent(ev, EventAgentMessage); m == nil || m.Payload["role"] != "system" || m.Payload["raw"] == nil {
		t.Fatalf("init=%v", ev)
	}
	res, _, _ := s.Finalize(context.Background(),
		[]byte(`{"type":"result","status":"success","stats":{"input_tokens":50,"output_tokens":20,"cached":5}}`+"\n"), 0)
	if res.Usage.Model != "auto-gemini-3" || res.Usage.InputTokens != 50 || res.Usage.OutputTokens != 20 || res.Usage.CacheTokens != 5 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestGeminiAssistantDelta(t *testing.T) {
	s := newGemini()
	ev, _ := s.ParseChunk([]byte(`{"type":"message","role":"assistant","content":"partial","delta":true}` + "\n"))
	m := findEvent(ev, EventAgentMessage)
	if m == nil || m.Payload["text"] != "partial" || m.Payload["delta"] != true {
		t.Fatalf("assistant=%v", m)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/ -run Gemini 2>&1 | head`
Expected: FAIL — old gemini uses `--output-format json`, stdin prompt, and parses the wrong stats schema.

- [ ] **Step 3: Rewrite `gemini.go`**

Replace `cliagent/gemini.go` with (ported from `anywhered/internal/provider/gemini.go`):

```go
package cliagent

import "context"

type geminiProvider struct{ cfg providerConfig }

// NewGemini returns a Gemini Provider.
func NewGemini(opts ...Option) Provider { return &geminiProvider{cfg: resolveOptions("gemini", opts)} }

func (p *geminiProvider) Name() string { return p.cfg.name }

func (p *geminiProvider) Capabilities() Capabilities {
	return Capabilities{Streaming: true, MCP: true, SupportsPTY: true, RequiresWorkspace: true}
}

func (p *geminiProvider) NewSession() Session { return &geminiSession{cfg: p.cfg, lb: &LineBuffer{}} }

type geminiSession struct {
	cfg     providerConfig
	lb      *LineBuffer
	usage   Usage
	summary string
}

func (s *geminiSession) BuildCommand(_ context.Context, req Request) (CommandSpec, error) {
	if len(s.cfg.allowedModes) > 0 && !sliceContains(s.cfg.allowedModes, req.Mode) {
		return CommandSpec{}, ErrUnsupportedMode
	}
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + req.Prompt
	}
	argv := []string{s.cfg.binary, "--prompt", prompt, "--output-format", "stream-json"}
	if req.PermissionMode == PermissionBypass {
		argv = append(argv, "--yolo")
	}
	if !req.Sandbox {
		argv = append(argv, "--skip-trust")
	}
	argv = append(argv, req.ExtraArgs...)
	return CommandSpec{Argv: argv, Env: mergeEnv(s.cfg.baseEnv, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *geminiSession) ParseChunk(chunk []byte) ([]Event, error) {
	return mapJSONLines(s.lb.Feed(chunk), s.mapGeminiEvent), nil
}

func (s *geminiSession) SessionID() string { return "" }

func (s *geminiSession) Finalize(_ context.Context, full []byte, exitCode int) (Result, []Event, error) {
	lines := s.lb.Feed(full)
	lines = append(lines, s.lb.Flush()...)
	tail := mapJSONLines(lines, s.mapGeminiEvent)
	return Result{ExitCode: exitCode, Summary: s.summary, Usage: s.usage}, tail, nil
}

func (s *geminiSession) mapGeminiEvent(obj map[string]any) []Event {
	t, _ := obj["type"].(string)
	switch t {
	case "init":
		if m, _ := obj["model"].(string); m != "" {
			s.usage.Model = m
		}
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "system", "raw": obj}}}
	case "message":
		role, _ := obj["role"].(string)
		text, _ := obj["content"].(string)
		switch role {
		case "user":
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "user", "text": text}}}
		case "assistant":
			s.summary = text
			return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "assistant", "text": text, "delta": obj["delta"] == true}}}
		default:
			return []Event{{Type: EventAgentMessage, Payload: obj}}
		}
	case "tool_call", "tool_use":
		return []Event{{Type: EventToolCall, Payload: obj}}
	case "tool_result", "tool_response":
		return []Event{{Type: EventToolResult, Payload: obj}}
	case "result":
		captureGeminiUsage(obj, &s.usage)
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": "result", "raw": obj}}}
	case "":
		return nil
	default:
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": t, "raw": obj}}}
	}
}

func captureGeminiUsage(obj map[string]any, into *Usage) {
	stats, _ := obj["stats"].(map[string]any)
	if stats == nil {
		return
	}
	if v, ok := stats["input_tokens"].(float64); ok {
		into.InputTokens = int64(v)
	}
	if v, ok := stats["output_tokens"].(float64); ok {
		into.OutputTokens = int64(v)
	}
	if v, ok := stats["cached"].(float64); ok {
		into.CacheTokens = int64(v)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cliagent/ -run Gemini 2>&1 | tail`
Expected: PASS.

- [ ] **Step 5: Run the whole cliagent package + gofmt**

Run: `go test ./cliagent/ 2>&1 | tail && gofmt -l cliagent/`
Expected: `ok`, and gofmt prints nothing. If gofmt lists files, run `gofmt -w cliagent/` and re-run tests.

- [ ] **Step 6: Commit**

```bash
git add cliagent/gemini.go cliagent/gemini_test.go
git commit -m "agentcli: rewrite gemini provider to real argv/events/usage with golden tests"
```

---

## Task 7: PTY runner — setsid, winsize, SIGINT→SIGKILL

**Files:**
- Modify: `cliagent/pty/pty.go`
- Modify: `cliagent/pty/pty_test.go`

- [ ] **Step 1: Add a winsize/tree-kill test (keep existing passing tests)**

Append to `cliagent/pty/pty_test.go`:

```go
func TestRunReportsSizeViaStty(t *testing.T) {
	// The PTY is sized 40x120; `stty size` prints "rows cols".
	res, err := Run(context.Background(), Command{Argv: []string{"sh", "-c", "stty size"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "40 120") {
		t.Fatalf("stty size = %q, want '40 120'", res.Output)
	}
}
```

(The existing `TestRunContextCancellation` already asserts prompt kill; the SIGINT→SIGKILL path is exercised there.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cliagent/pty/ -run Stty 2>&1 | head`
Expected: FAIL — current runner does not call `pty.Setsize`, so the size is the default 0x0/80x24, not 40x120.

- [ ] **Step 3: Update `pty.go` to match the real runner**

Replace `cliagent/pty/pty.go` with:

```go
// Package pty runs a command under a pseudo-terminal, streaming its combined
// output to a callback and capturing it. It knows nothing about providers.
package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Command describes a process to run under a PTY. It maps from cliagent.CommandSpec.
type Command struct {
	Argv    []string
	Env     []string // "KEY=VALUE" overrides applied on top of os.Environ()
	WorkDir string
	Stdin   []byte // written to the PTY once started (optional)
	Rows    uint16 // defaults to 40
	Cols    uint16 // defaults to 120
}

// Result is the outcome of a PTY run.
type Result struct {
	ExitCode int
	Output   []byte
}

// Run starts cmd under a PTY in its own process group, forwarding output chunks
// to onChunk and accumulating Result.Output. On ctx cancellation the child gets
// SIGINT, then SIGKILL after 2s, and ctx.Err() is returned.
func Run(ctx context.Context, cmd Command, onChunk func([]byte)) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("pty: empty argv")
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Env = append(os.Environ(), cmd.Env...)
	c.Dir = cmd.WorkDir
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	ptmx, err := pty.Start(c)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = ptmx.Close() }()

	rows, cols := cmd.Rows, cmd.Cols
	if rows == 0 {
		rows = 40
	}
	if cols == 0 {
		cols = 120
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})

	if len(cmd.Stdin) > 0 {
		go func() { _, _ = ptmx.Write(cmd.Stdin) }()
	}

	var (
		fullBuf bytes.Buffer
		bufMu   sync.Mutex
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				bufMu.Lock()
				fullBuf.Write(chunk)
				bufMu.Unlock()
				if onChunk != nil {
					onChunk(chunk)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGINT)
				select {
				case <-time.After(2 * time.Second):
					_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
				case <-done:
				}
			}
		case <-done:
		}
	}()

	waitErr := c.Wait()
	close(done)
	wg.Wait()

	res := Result{Output: fullBuf.Bytes()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		res.ExitCode = -1
		return res, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if waitErr != nil && !errors.Is(waitErr, io.EOF) {
		res.ExitCode = -1
		return res, waitErr
	}
	return res, nil
}
```

- [ ] **Step 4: Run the pty tests to verify they pass**

Run: `go test ./cliagent/pty/ 2>&1 | tail`
Expected: PASS (including `TestRunReportsSizeViaStty` and the existing tests). If `TestRunFeedsStdin` flakes because `head -n1` plus PTY echo reorders output, it still asserts `Contains`, which holds.

- [ ] **Step 5: Commit**

```bash
git add cliagent/pty/pty.go cliagent/pty/pty_test.go
git commit -m "agentcli/pty: setsid process group, 40x120 winsize, SIGINT->SIGKILL tree kill"
```

---

## Task 8: Full green — race, vet, fmt, tidy

**Files:** none (verification only)

- [ ] **Step 1: Run the full suite with the race detector**

Run: `go test -race -count=1 ./... 2>&1 | tail`
Expected: `ok` for `cliagent`, `cliagent/pty`, and `hooks` (hooks is untouched this plan and must still pass).

- [ ] **Step 2: Vet, fmt, tidy**

Run: `go vet ./... && gofmt -l . && go mod tidy && echo CLEAN`
Expected: no vet output, `gofmt -l` prints nothing, `CLEAN` printed. If `gofmt -l` lists files, `gofmt -w .` then re-run Step 1.

- [ ] **Step 3: Commit any tidy/fmt changes**

```bash
git add -A && git commit -m "agentcli: tidy + fmt after Phase 2 superset revision" || echo "nothing to commit"
```

---

## Self-Review (completed by plan author)

**Spec coverage:** A.1 types → Task 1; A.2 options/mergeEnv → Task 1; A.3 claude → Task 4; A.4 codex → Task 5; A.5 gemini → Task 6; A.6 mode validation → Tasks 4–6 (`WithAllowedModes`); A.7 mapJSONLines → Task 2; A.8 plugin → Task 3; A.9 pty → Task 7; A.10 test rewrites + golden tests → Tasks 1–7; C verification → Task 8. The `hooks` package is intentionally untouched in Plan A (it already matches the spec's pure helpers); its app-side reconciliation is Plan B.

**Type consistency:** `providerConfig` fields (`name/binary/baseEnv/modelEnv/mcpFilename/strictMCP/allowedModes`) defined in Task 1 are used identically in Tasks 4–6. `mergeEnv(base, reqEnv)` (2-arg) defined in Task 1 is called with 2 args in Tasks 4–6. `writeMCPConfig(dir, filename, servers)` (3-arg) defined in Task 3 is called with 3 args in Task 4. `loadPluginMCPServers(plugins, env)` defined in Task 3 is called with 2 args in Task 4. Event consts (`EventAgentMessage/EventToolCall/EventToolResult/EventRateLimit/EventTerminalOutput`) are the Phase 1 names, unchanged. `findEvent`/`contains` live once in `helpers_test.go` (Task 4) and are reused by codex/gemini tests.

**Placeholder scan:** no TBD/TODO; every code step includes complete code.

---

## Next

After Plan A is green and committed, write **Plan B: `anywhered` migration** (delete forked `internal/provider` + `internal/runtime/pty`, switch types, app-side `LoadFromConfig` adapter, `manager.go` Request fields + AAS bridge + `pty.Run`, `hooks.go` swap to `agentcli/hooks`, `go.mod` local `replace`, echo smoke run) per spec section B.
