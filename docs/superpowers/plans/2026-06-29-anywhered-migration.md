# anywhered Migration Implementation Plan (Phase 2, Plan B)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate `github.com/liliang-cn/anywhere/anywhered` off its forked `internal/provider` + `internal/runtime` packages onto the external `github.com/liliang-cn/agentcli` module (`cliagent` + `cliagent/pty`), with no daemon behavior change.

**Architecture:** `agentcli` now supplies the `Provider`/`Session` interfaces and the `Request/Event/Result/Usage/Capabilities/PluginRef/Registry` types plus the claude/codex/gemini providers and the PTY runner. `anywhered` keeps a thin app-local `internal/provider` package containing only the app-specific `echo`/`opencode` providers and a `LoadFromConfig` adapter that wires config → `agentcli` constructors. `internal/runtime` is deleted. `manager.go` builds the AAS MCP bridge as `Request.ExtraMCPServers` (instead of via the `AAS_TOOL_SOCKET` env that the old claude provider sniffed) and sets `PermissionMode=PermissionBypass, Sandbox=false`.

**Tech Stack:** Go 1.25, local `replace` directive to the side-by-side `agentcli` checkout.

**Working directory:** `/Users/liliang/Things/AI/projects/anywhere/anywhered`. All `go`/`git` commands run from there. `agentcli` must be checked out at `/Users/liliang/Things/AI/projects/agentcli` (sibling at `../../agentcli`).

**Pre-req:** `agentcli` Plan A is merged to its `main` (done: the module API is final).

## Branching

This plan modifies the `anywhered` repo. Before Task 1, create a feature branch there:
```bash
cd /Users/liliang/Things/AI/projects/anywhere/anywhered
git checkout -b phase2-agentcli-migration
git status --short   # confirm clean tree
go test ./... 2>&1 | tail   # confirm baseline green before migrating
```

## API mapping (agentcli surface anywhered will use)

- Interfaces/types: `cliagent.Provider`, `cliagent.Session`, `cliagent.Request`, `cliagent.CommandSpec`, `cliagent.Event`, `cliagent.Result`, `cliagent.Usage`, `cliagent.Capabilities`, `cliagent.PluginRef`, `cliagent.Registry`, `cliagent.NewRegistry()`, `cliagent.ErrUnsupportedMode`, `cliagent.EventTerminalOutput`.
- Constructors/options: `cliagent.NewClaude/NewCodex/NewGemini`, `WithName/WithBinary/WithBaseEnv/WithModelEnv/WithMCPConfig/WithAllowedModes`.
- Line buffer: `&cliagent.LineBuffer{}` (zero value; `Feed`/`Flush` exported). There is no `NewLineBuffer()` — use the struct literal.
- `cliagent.Capabilities` fields are `{Streaming, Resume, Plugins, MCP, SupportsPTY, RequiresWorkspace}`. It has **no** `Modes`/`SupportsStreamJSON`/`SupportsServerMode` — drop those from echo/opencode (no production code reads them; mode validation for opencode uses a local list).
- PTY: `pty.Run(ctx, pty.Command{Argv, Env, WorkDir}, onChunk) (pty.Result, error)`, `pty.Result{ExitCode, Output}` — import `github.com/liliang-cn/agentcli/cliagent/pty`.

---

## File Structure (after migration)

`internal/provider/` shrinks to app-local files only:
| File | Action |
|---|---|
| `internal/provider/echo.go` | Rewrite against `cliagent` types |
| `internal/provider/opencode.go` | Rewrite against `cliagent` types (+ local `mergeEnv`) |
| `internal/provider/registry.go` | Create: `LoadFromConfig` adapter → `*cliagent.Registry` |
| `internal/provider/{claude,codex,gemini,plugin,linebuf,provider}.go` + their `_test.go` | Delete |
| `internal/runtime/pty.go` (whole dir) | Delete |
| `internal/orchestrator/manager.go` | Edit: imports, `cliagent.*` types, `Request` fields, `pty.Run` |
| `internal/orchestrator/manager_test.go` | Edit: stub uses `cliagent.*` |
| `internal/server/server_test.go` | Edit: stub uses `cliagent.*` |
| `cmd/anywhered/main.go` | No code change (still calls `provider.LoadFromConfig`) |
| `go.mod` / `go.sum` | Add require + local replace; tidy |

---

## Task 1: Add agentcli dependency (go.mod replace)

**Files:** `go.mod`

- [ ] **Step 1: Add the require + local replace (do NOT tidy yet)**

Run:
```bash
go mod edit -require=github.com/liliang-cn/agentcli@v0.0.0
go mod edit -replace=github.com/liliang-cn/agentcli=../../agentcli
```

- [ ] **Step 2: Verify the dep resolves and the existing build is still green**

Run: `go build ./... 2>&1 | tail`
Expected: clean. (The require is currently unused by code, which is fine for `go build`. Do NOT run `go mod tidy` yet — it would strip the unused require before Task 2 introduces the imports.)

- [ ] **Step 3: Commit**

```bash
git add go.mod
git commit -m "anywhered: add agentcli dependency via local replace"
```

---

## Task 2: Cut over to agentcli

This is one coherent change: the package only compiles once all pieces move together. Make every edit below, then verify build + tests green, then `go mod tidy`, then commit once.

**Files:** rewrite `internal/provider/echo.go`, `internal/provider/opencode.go`; create `internal/provider/registry.go`; delete `internal/provider/{claude,codex,gemini,plugin,linebuf,provider}.go` and their `_test.go`; delete `internal/runtime/pty.go`; edit `internal/orchestrator/manager.go`, `internal/orchestrator/manager_test.go`, `internal/server/server_test.go`.

- [ ] **Step 1: Rewrite `internal/provider/echo.go`**

Replace the entire file with:

```go
package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agentcli/cliagent"
)

// echoFactory is a built-in zero-dependency provider used for smoke tests and
// demos. BuildCommand returns a `/bin/sh -c` invocation that prints the prompt.
type echoFactory struct{}

func NewEcho() cliagent.Provider { return echoFactory{} }

func (echoFactory) Name() string { return "echo" }

func (echoFactory) Capabilities() cliagent.Capabilities {
	return cliagent.Capabilities{SupportsPTY: true}
}

func (echoFactory) NewSession() cliagent.Session { return &echoSession{lb: &cliagent.LineBuffer{}} }

type echoSession struct {
	lb *cliagent.LineBuffer
}

func (*echoSession) BuildCommand(ctx context.Context, req cliagent.Request) (cliagent.CommandSpec, error) {
	script := fmt.Sprintf(
		"echo 'agent: running echo provider for run %s'; "+
			"echo 'agent: prompt was:'; "+
			"printf '%%s\\n' %s; "+
			"echo 'agent: --- environment probe ---'; "+
			"id || true; "+
			"uname -a || true; "+
			"echo 'pwd:' $(pwd); "+
			"echo 'mounts:' $(awk '$5 ~ /^\\/(workspace|aas|tmp)/ {print $5}' /proc/self/mountinfo 2>/dev/null | sort -u | tr '\\n' ' '); "+
			"sleep 6; "+
			"echo 'agent: done'",
		shellQuote(req.RunID),
		shellQuote(req.Prompt),
	)
	return cliagent.CommandSpec{
		Argv:    []string{"/bin/sh", "-c", script},
		WorkDir: req.WorkspacePath,
	}, nil
}

func (s *echoSession) ParseChunk(chunk []byte) ([]cliagent.Event, error) {
	return linesToTerminalOutput(s.lb.Feed(chunk)), nil
}

func (s *echoSession) Finalize(ctx context.Context, full []byte, exitCode int) (cliagent.Result, []cliagent.Event, error) {
	return cliagent.Result{ExitCode: exitCode, Summary: "echo completed"}, linesToTerminalOutput(s.lb.Flush()), nil
}

func (*echoSession) SessionID() string { return "" }

// linesToTerminalOutput wraps raw lines as terminal.output events (shared by
// echo and opencode).
func linesToTerminalOutput(lines []string) []cliagent.Event {
	if len(lines) == 0 {
		return nil
	}
	out := make([]cliagent.Event, 0, len(lines))
	for _, line := range lines {
		out = append(out, cliagent.Event{
			Type:    cliagent.EventTerminalOutput,
			Payload: map[string]any{"line": line},
		})
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 2: Rewrite `internal/provider/opencode.go`**

Replace the entire file with:

```go
package provider

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/liliang-cn/agentcli/cliagent"
	"github.com/liliang-cn/anywhere/anywhered/internal/config"
)

// opencodeModes are the modes the beta OpenCode provider advertises.
var opencodeModes = []string{"headless-code", "terminal-task"}

// opencodeFactory wraps the OpenCode CLI (beta). MVP only ships the one-shot
// `opencode run` path; output is treated as raw terminal text.
type opencodeFactory struct {
	binary string
	envCfg map[string]string
}

func NewOpenCode(c config.ProviderConfig) cliagent.Provider {
	bin := c.Binary
	if bin == "" {
		bin = "opencode"
	}
	return opencodeFactory{binary: bin, envCfg: c.Env}
}

func (opencodeFactory) Name() string { return "opencode-beta" }

func (opencodeFactory) Capabilities() cliagent.Capabilities {
	return cliagent.Capabilities{SupportsPTY: true, RequiresWorkspace: true}
}

func (f opencodeFactory) NewSession() cliagent.Session {
	return &opencodeSession{binary: f.binary, envCfg: f.envCfg, lb: &cliagent.LineBuffer{}}
}

type opencodeSession struct {
	binary string
	envCfg map[string]string
	lb     *cliagent.LineBuffer
}

func (s *opencodeSession) BuildCommand(ctx context.Context, req cliagent.Request) (cliagent.CommandSpec, error) {
	if !slices.Contains(opencodeModes, req.Mode) {
		return cliagent.CommandSpec{}, fmt.Errorf("%w: %s", cliagent.ErrUnsupportedMode, req.Mode)
	}
	argv := []string{s.binary, "run", req.Prompt}
	return cliagent.CommandSpec{Argv: argv, Env: mergeEnv(s.envCfg, req.Env), WorkDir: req.WorkspacePath}, nil
}

func (s *opencodeSession) ParseChunk(chunk []byte) ([]cliagent.Event, error) {
	return linesToTerminalOutput(s.lb.Feed(chunk)), nil
}

func (s *opencodeSession) Finalize(ctx context.Context, full []byte, exitCode int) (cliagent.Result, []cliagent.Event, error) {
	return cliagent.Result{ExitCode: exitCode}, linesToTerminalOutput(s.lb.Flush()), nil
}

func (*opencodeSession) SessionID() string { return "" }

// mergeEnv merges base + overrides into a sorted "KEY=VALUE" slice, dropping
// empty base values; overrides win.
func mergeEnv(base, over map[string]string) []string {
	merged := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		if v != "" {
			merged[k] = v
		}
	}
	for k, v := range over {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 3: Create `internal/provider/registry.go`**

```go
package provider

import (
	"github.com/liliang-cn/agentcli/cliagent"
	"github.com/liliang-cn/anywhere/anywhered/internal/config"
)

// claudeModes / agentModes mirror the modes the upstream providers used to
// advertise; BuildCommand returns ErrUnsupportedMode for anything else.
var (
	claudeModes = []string{"headless-code", "terminal-task", "browser-task", "desktop-task", "computer-task"}
	agentModes  = []string{"headless-code", "terminal-task"}
)

// LoadFromConfig builds a cliagent.Registry from `providers:` config: external
// CLI providers (claude/codex/gemini) via agentcli constructors, plus the
// app-local echo/opencode providers. Disabled entries are skipped.
func LoadFromConfig(cfg map[string]config.ProviderConfig) *cliagent.Registry {
	reg := cliagent.NewRegistry()
	for name, pc := range cfg {
		if !pc.Enabled {
			continue
		}
		switch name {
		case "claude-code":
			reg.Register(cliagent.NewClaude(claudeOpts(pc)...))
		case "codex":
			reg.Register(cliagent.NewCodex(agentOpts("codex", pc)...))
		case "gemini-cli":
			reg.Register(cliagent.NewGemini(agentOpts("gemini-cli", pc)...))
		case "opencode-beta":
			reg.Register(NewOpenCode(pc))
		case "echo":
			reg.Register(NewEcho())
		}
	}
	return reg
}

func claudeOpts(pc config.ProviderConfig) []cliagent.Option {
	// Default MCP_TOOL_TIMEOUT to 30min (the human.await tool blocks long);
	// pc.Env overrides it. mergeEnv lets a per-request Env override base too.
	base := map[string]string{"MCP_TOOL_TIMEOUT": "1800000"}
	for k, v := range pc.Env {
		base[k] = v
	}
	opts := []cliagent.Option{
		cliagent.WithName("claude-code"),
		cliagent.WithBaseEnv(base),
		cliagent.WithModelEnv("CLAUDE_MODEL"),
		cliagent.WithMCPConfig(".aas-mcp.json", true),
		cliagent.WithAllowedModes(claudeModes),
	}
	if pc.Binary != "" {
		opts = append(opts, cliagent.WithBinary(pc.Binary))
	}
	return opts
}

func agentOpts(name string, pc config.ProviderConfig) []cliagent.Option {
	opts := []cliagent.Option{
		cliagent.WithName(name),
		cliagent.WithBaseEnv(pc.Env),
		cliagent.WithAllowedModes(agentModes),
	}
	if pc.Binary != "" {
		opts = append(opts, cliagent.WithBinary(pc.Binary))
	}
	return opts
}
```

- [ ] **Step 4: Delete the migrated provider files and the runtime package**

```bash
git rm internal/provider/claude.go internal/provider/claude_test.go \
       internal/provider/codex.go internal/provider/codex_test.go \
       internal/provider/gemini.go internal/provider/gemini_test.go \
       internal/provider/plugin.go internal/provider/plugin_test.go \
       internal/provider/linebuf.go internal/provider/linebuf_test.go \
       internal/provider/provider.go \
       internal/runtime/pty.go
```

(If any of these paths does not exist, drop it from the command. After this, `internal/runtime/` should be empty — git removing the only file removes the dir.)

- [ ] **Step 5: Edit `internal/orchestrator/manager.go` — imports**

In the import block, REMOVE these two lines:
```go
	"github.com/liliang-cn/anywhere/anywhered/internal/provider"
	"github.com/liliang-cn/anywhere/anywhered/internal/runtime"
```
and ADD:
```go
	"github.com/liliang-cn/agentcli/cliagent"
	"github.com/liliang-cn/agentcli/cliagent/pty"
```

- [ ] **Step 6: Edit `internal/orchestrator/manager.go` — type references**

Replace `*provider.Registry` → `*cliagent.Registry` (two sites: the `Manager.reg` struct field and the `NewManager` parameter). Replace `prov provider.Provider` → `prov cliagent.Provider` in the `drive` signature.

- [ ] **Step 7: Edit `internal/orchestrator/manager.go` — AAS bridge + Request**

In `drive`, the block that stands up the tool server currently ends with `reqEnv["AAS_TOOL_SOCKET"] = sock`. Build the MCP bridge there and pass it via `ExtraMCPServers`. Change this block:

```go
	reqEnv := map[string]string{"ANYWHERE_APP_RUN": run.ID}
	if m.cps != nil && run.Cwd != "" {
		sock := filepath.Join(os.TempDir(), "aw-"+run.ID+".sock")
		ts, terr := toolserver.New(sock, toolserver.Config{
			RunID: run.ID,
			Human: humanGate{m.cps},
			Emit:  func(typ string, payload map[string]any) { m.publish(run, typ, payload) },
		})
		if terr == nil {
			ts.Start()
			defer ts.Stop(context.Background())
			reqEnv["AAS_TOOL_SOCKET"] = sock
		}
	}

	sess := prov.NewSession()
	spec, err := sess.BuildCommand(ctx, provider.Request{
		RunID:           run.ID,
		Mode:            "headless-code", // providers that validate mode (claude-code) need it
		Prompt:          prompt,
		WorkspacePath:   run.Cwd,
		Env:             reqEnv,
		ResumeSessionID: run.resumeSessionID,
		Continue:        run.continueLast,
	})
```

to:

```go
	reqEnv := map[string]string{"ANYWHERE_APP_RUN": run.ID}
	extraMCP := map[string]any{}
	if m.cps != nil && run.Cwd != "" {
		sock := filepath.Join(os.TempDir(), "aw-"+run.ID+".sock")
		ts, terr := toolserver.New(sock, toolserver.Config{
			RunID: run.ID,
			Human: humanGate{m.cps},
			Emit:  func(typ string, payload map[string]any) { m.publish(run, typ, payload) },
		})
		if terr == nil {
			ts.Start()
			defer ts.Stop(context.Background())
			reqEnv["AAS_TOOL_SOCKET"] = sock
			// Wire the per-run human.await MCP bridge so claude can reach the
			// tool server. The bridge connects via --socket; agentcli no longer
			// sniffs AAS_TOOL_SOCKET, so inject the server map explicitly.
			if exe, eerr := os.Executable(); eerr == nil {
				extraMCP["aas"] = map[string]any{
					"command": exe,
					"args":    []string{"mcp-bridge", "--socket", sock},
				}
			}
		}
	}

	sess := prov.NewSession()
	spec, err := sess.BuildCommand(ctx, cliagent.Request{
		RunID:           run.ID,
		Mode:            "headless-code", // providers that validate mode (claude-code) need it
		Prompt:          prompt,
		WorkspacePath:   run.Cwd,
		Env:             reqEnv,
		ResumeSessionID: run.resumeSessionID,
		Continue:        run.continueLast,
		PermissionMode:  cliagent.PermissionBypass,
		Sandbox:         false,
		ExtraMCPServers: extraMCP,
	})
```

- [ ] **Step 8: Edit `internal/orchestrator/manager.go` — pty.Run call**

Replace:
```go
	res, runErr := runtime.Run(ctx, spec.Argv, spec.Env, spec.WorkDir, onChunk)
```
with:
```go
	res, runErr := pty.Run(ctx, pty.Command{Argv: spec.Argv, Env: spec.Env, WorkDir: spec.WorkDir}, onChunk)
```
(The rest — `res.Output`, `res.ExitCode`, `finalRes`, `captureStats`, `Payload["raw"]` — is unchanged; `pty.Result` has the same `ExitCode`/`Output` fields.)

- [ ] **Step 9: Edit the two test stubs**

In `internal/orchestrator/manager_test.go`: change the import `"github.com/liliang-cn/anywhere/anywhered/internal/provider"` → `"github.com/liliang-cn/agentcli/cliagent"`, and in the `fakeProvider`/`fakeSession` definitions replace every `provider.` with `cliagent.` (i.e. `provider.Capabilities`→`cliagent.Capabilities`, `provider.Session`→`cliagent.Session`, `provider.Request`→`cliagent.Request`, `provider.CommandSpec`→`cliagent.CommandSpec`, `provider.Event`→`cliagent.Event`, `provider.Result`→`cliagent.Result`). Leave all other logic (e.g. `events.TypeAgentMessage`) unchanged.

In `internal/server/server_test.go`: same — import swap and `provider.`→`cliagent.` in `fastProvider`/`fastSession`.

- [ ] **Step 10: Build, fix, test, tidy**

Run, in order:
```bash
go build ./... 2>&1 | tail
go mod tidy 2>&1 | tail
go build ./... 2>&1 | tail
go test ./... 2>&1 | tail
gofmt -l . ; go vet ./... 2>&1 | tail
```
Expected: build clean, tests green, `gofmt -l` prints nothing, vet clean. If `go build` reports a leftover `provider.X` reference, fix that site to `cliagent.X` (or to the app-local `provider.LoadFromConfig` if it's the registry adapter in main.go — that stays `provider.LoadFromConfig`). If a deleted-file symbol is still referenced, that reference was missed — resolve it.

Note: `cmd/anywhered/main.go` should need NO change — it calls `provider.LoadFromConfig(...)` which still exists (now returns `*cliagent.Registry`) and passes the result to `orchestrator.NewManager`. Confirm it compiles; only touch it if the compiler points there.

- [ ] **Step 11: Commit**

```bash
git add -A
git commit -m "anywhered: migrate provider+runtime onto agentcli; AAS bridge via ExtraMCPServers"
```

---

## Task 3: Verify behavior parity

**Files:** none (verification only)

- [ ] **Step 1: Full race suite + vet + fmt**

```bash
go test -race -count=1 ./... 2>&1 | tail -20
go vet ./... && gofmt -l . && echo CLEAN
```
Expected: all packages green under `-race`; vet/fmt clean.

- [ ] **Step 2: Argv parity spot-check (golden, no live CLI)**

Add a temporary throwaway test (or a scratch `main`) is NOT required. Instead, confirm by inspection that the claude provider, configured as in `registry.go` (`WithName("claude-code")`, `WithModelEnv("CLAUDE_MODEL")`, `WithMCPConfig(".aas-mcp.json", true)`, `WithAllowedModes(claudeModes)`, base env `MCP_TOOL_TIMEOUT=1800000`) and driven with `Request{Mode:"headless-code", PermissionMode:PermissionBypass, Sandbox:false, Env:{"CLAUDE_MODEL":...,"ANYWHERE_APP_RUN":...,"AAS_TOOL_SOCKET":...}, ExtraMCPServers:{"aas":...}, WorkspacePath:...}` produces the same `claude --print --output-format stream-json --verbose --permission-mode bypassPermissions --model <m> --mcp-config <ws>/.aas-mcp.json --strict-mcp-config [--plugin-dir...] <prompt>` argv and `MCP_TOOL_TIMEOUT=1800000` env that the pre-migration daemon produced. This is already covered by agentcli's golden tests; just confirm the registry wiring matches.

- [ ] **Step 3: Echo smoke run through the daemon**

Start the daemon and drive an echo run end-to-end to confirm the orchestrator still streams events, scrollback, and reaches a terminal state. Use a random high port (per house rule, e.g. 43517):

```bash
# Build and run the daemon on a random high port; create + drive an echo run.
go build -o /tmp/anywhered-mig ./cmd/anywhered
# Follow the project's normal "create run" path (CLI/WS/API) against the echo
# provider in a scratch workspace; confirm: events stream, terminal exits, final
# state = completed. If a scripted smoke entrypoint exists, prefer it.
```
If there is no easy non-interactive smoke entrypoint, report that and rely on the `-race` test suite + the agentcli golden tests as the parity evidence (do not claim a smoke run that wasn't performed).

- [ ] **Step 4: Report**

Summarize: build/test/-race/vet/fmt results, whether the echo smoke run was performed and its outcome, and any residual `provider.*`/`runtime.*` references (should be none outside the app-local `internal/provider` package).

---

## Self-Review (completed by plan author)

**Spec coverage (spec section B):** B.1 deletions → Task 2 Step 4; B.2 type switch → Task 2 Steps 5–9; B.3 LoadFromConfig adapter → Task 2 Step 3; B.4 manager.go (Request fields + AAS bridge + pty.Run) → Task 2 Steps 7–8; B.5 hooks.go → **see note below**; B.6 go.mod replace → Task 1. Verification (spec C) → Task 3.

**hooks.go note:** Spec B.5 (swap `cmd/anywhered/hooks.go` to `agentcli/hooks.ParsePayload/Summarize/LastAssistantText`, keeping the daemon transport app-side) is **deferred to a follow-up** and intentionally NOT in this plan. Rationale: `hooks.go` does not import `internal/provider`, so it neither blocks the compile nor depends on this cutover; it is an independent, optional dedup. This plan's scope is the provider/runtime cutover that makes anywhered depend on agentcli. Folding hooks in would mix an unrelated change into the cutover commit. Track it as Plan C if desired.

**Type consistency:** `cliagent.Capabilities` has no `Modes`/`SupportsStreamJSON`/`SupportsServerMode` — echo/opencode drop them (Task 2 Steps 1–2); opencode validates modes via the local `opencodeModes` + `slices.Contains`. `mergeEnv` is reintroduced app-locally in `opencode.go` (Task 2 Step 2) since the old package-level one is deleted; `linesToTerminalOutput`/`shellQuote` live in `echo.go` and are shared within the package. `LoadFromConfig` returns `*cliagent.Registry`, consumed unchanged by `main.go` and `NewManager`.

**Placeholder scan:** no TBD/TODO; every code step has complete code.

---

## Risks

- **Local `replace` coupling** — anywhered builds only with `agentcli` at `../../agentcli`. Acceptable for the unreleased pair; drop the replace once agentcli is tagged.
- **Behavior parity** — mitigated by agentcli's golden argv/event/usage tests (Plan A) + the `-race` suite + echo smoke run. The one deliberate internal change is the AAS bridge moving from env-sniff to `ExtraMCPServers`, which produces the same merged `.aas-mcp.json`.
- **`go mod tidy` + replace** — a local `replace` needs no `go.sum` entry for agentcli; agentcli's own dep (`creack/pty v1.1.24`) already matches anywhered's. If tidy reports a version mismatch, align the `creack/pty` version.
