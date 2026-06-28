# Phase 2 — Migrate `anywhered` onto `agentcli` (and make the module a faithful superset)

Date: 2026-06-28
Status: Approved (brainstorming)
Module: `github.com/liliang-cn/agentcli`
Consumer migrated this phase: `anywhere/anywhered` (`github.com/liliang-cn/anywhere/anywhered`)

## Purpose

Phase 1 built `agentcli` from the *spec's reconciliation notes*. Surveying the
real code shows the module diverges from `anywhered`'s actual headless behavior
in concrete, behavior-affecting ways (provider names, factory shape,
`Capabilities` shape, headless bypass flags, `Payload["raw"]`, prompt-via-argv,
graceful PTY kill, plugin `${VAR}` rules). Phase 2 **revises `agentcli` into a
faithful superset** and migrates `anywhered` to depend on it, deleting
`anywhered`'s forked `internal/provider` and `internal/runtime/pty`, with
**byte-identical argv / env / events**.

## Decisions (resolved during brainstorming)

1. **Source of truth = the real app.** The module is revised to reproduce
   `anywhered`'s exact commands and events; the daemon's behavior does not change.
2. **Headless flags = explicit `Request` fields.** `PermissionMode` and `Sandbox`
   on `Request`; each provider translates them to its own flag strings (the
   module owns the mapping, so it is not re-forked per app).
3. **Module boundary = full superset including `BuildCommand`**, parameterized via
   functional options + `Request` fields. `anywhered` keeps only app-specific
   pieces (echo/opencode providers, a config→options adapter, the daemon hook
   transport).

## Shared-consumer reality (verified, corrects Phase 1)

- **anywhere/anywhered** — shares `agentcli`. This phase.
- **agent-as-a-service (`Agent`)** — its `internal/provider` is a near-identical
  twin of `anywhered`'s (same interfaces, same claude `--print … --append-system-prompt`,
  codex, gemini argv, same `runtime.Run` signature, same `AAS_TOOL_SOCKET` bridge,
  same `CLAUDE_MODEL`). Every difference is covered by this superset. Its SaaS
  system prompt moves from baked-in constructors to `Request.SystemPrompt`. **Phase 3.**
- **roma** — **removed from scope.** roma is a generic PTY *text-classifier*
  (`ROMA_*` marker regex + semantic stream signals on raw stdout). It does **not**
  parse stream-json, **not** use Claude/Codex hooks, **not** read transcripts,
  **not** track usage. The Phase 1 claim that roma would consume `hooks` is false.
  roma shares nothing and is not a consumer.

`UserID` / `ProjectID` / `PolicyJSON` stay **out** of `agentcli.Request` — no
provider's command-building reads them (verified: `anywhered` never sets them;
`Agent` only stores them in its DB). Each app carries them in its own run structs
and fills only the provider-relevant fields of `agentcli.Request`.

## A. `agentcli` module revisions

References to the real source: `anywhered/internal/provider/{claude,codex,gemini,plugin}.go`,
`anywhered/internal/runtime/pty.go`. The superset must reproduce these exactly
for the inputs `anywhered` supplies.

### A.1 Types (`cliagent/types.go`)

`Request` — add to the existing struct:

```go
ExtraMCPServers map[string]any  // caller-injected MCP servers (e.g. anywhered's AAS bridge), merged before plugins
PermissionMode  PermissionMode  // PermissionDefault | PermissionBypass
Sandbox         bool            // true = sandboxed (default); false = emit skip-sandbox/trust/git-check flags
```

`SystemPrompt`, `Model`, `Plugins`, `MCPConfigPath`, `ExtraArgs` already exist
and stay. `UserID/ProjectID/PolicyJSON` are **not** added.

```go
type PermissionMode string
const (
    PermissionDefault PermissionMode = ""
    PermissionBypass  PermissionMode = "bypass"
)
```

`Capabilities` — widen to the app-agnostic superset:

```go
type Capabilities struct {
    Streaming        bool
    Resume           bool
    Plugins          bool
    MCP              bool
    SupportsPTY      bool
    RequiresWorkspace bool
}
```

The app-specific `Modes []string` does **not** live in `Capabilities`; see A.6.

`ErrUnsupportedMode` — add `var ErrUnsupportedMode = errors.New("cliagent: unsupported mode")`.

### A.2 Options (`cliagent/provider.go`)

Keep `WithBinary`, `WithBaseEnv`, `WithModelEnv`. Add:

- `WithName(string)` — overrides the registered provider name (anywhered uses
  `claude-code` / `codex` / `gemini-cli`).
- `WithMCPConfig(filename string, strict bool)` — sets the merged-config filename
  written under `WorkspacePath` (anywhered: `.aas-mcp.json`) and whether to append
  `--strict-mcp-config`.
- `WithAllowedModes([]string)` — when non-empty, `BuildCommand` validates
  `Request.Mode` against the list and returns `ErrUnsupportedMode` otherwise.

**Model resolution (changed from Phase 1).** `WithModelEnv(key)` now means *source
the model value from `Request.Env[key]`* (Phase 1 instead injected the model into
the process env). The claude model value resolves as `Request.Model` if set, else
`Request.Env[key]` when `WithModelEnv` is set; whenever non-empty it is emitted as
a `--model <value>` flag. This reproduces anywhered exactly (it reads
`req.Env["CLAUDE_MODEL"]` and emits `--model <value>`).

`mergeEnv` must match `anywhered`'s semantics: drop empty **base** values, let
`Request.Env` override base.

`MCP_TOOL_TIMEOUT=1800000` is **not** special-cased in the module; anywhered
supplies it via `WithBaseEnv` (mergeEnv keeps it unless `Request.Env` overrides —
behaviorally identical to the old "set if absent").

### A.3 claude (`cliagent/claude.go`) — rewrite to match `claude.go` verbatim

Capabilities: `{Streaming, Resume, Plugins, MCP, SupportsPTY, RequiresWorkspace}` all true.

`BuildCommand` argv, in order:

```
<binary> --print --output-format stream-json --verbose
  [--permission-mode bypassPermissions]            # when PermissionMode==PermissionBypass
  [--model <value>]                                 # value = Request.Model, else Request.Env["CLAUDE_MODEL"] via WithModelEnv
  [--mcp-config <abs path> --strict-mcp-config]     # when merged servers non-empty AND WorkspacePath != ""
  [--plugin-dir <path>]...                          # one per plugin with non-empty Path
  [--resume <id> | --continue]                      # resume wins over continue
  <prompt>                                           # positional, last
```

MCP merge order (into one `mcpServers` map, last-write-wins): `Request.ExtraMCPServers`
first, then plugin `.mcp.json` servers (`loadPluginMCPServers`). Written via
`writeMCPConfig(WorkspacePath, servers)` to `<WorkspacePath>/<WithMCPConfig filename>`
(default chosen by app: `.aas-mcp.json`), absolute path passed to `--mcp-config`.

Env: `mergeEnv(baseEnv, Request.Env)`; nothing else added by the module.

Event mapping (`mapClaudeEvent`, byte-for-byte with the real mapper):

- `system` → `agent.message` `{role:"system", raw:obj}`
- `assistant` → for each `tool_use` block an `agent.tool_call` (payload = the block);
  then if combined text non-empty an `agent.message` `{role:"assistant", text}`;
  if neither, one `agent.message` `{role:"assistant", raw:obj}`
- `user` → `agent.tool_result` with payload = the whole obj
- `result` → `agent.message` `{role:"result", raw:obj}`
- `rate_limit_event` → `provider.rate_limit`; pull `rate_limit_info` and surface
  `status / rate_limit_type / resets_at / overage_status / is_using_overage` plus `raw:info`
- `""` → no event; default → `agent.message` `{role:t, raw:obj}`

Usage: `result` frame is canonical (`setUsageFromObject`: input/output, cache =
`cache_creation_input_tokens + cache_read_input_tokens`, `EstimatedCostUSD =
total_cost_usd`); assistant frames accumulate into a `fallback` only while no
result seen; `sawResult` selects the source in `Finalize`. Model name captured
from `assistant.message.model`. `session_id` sniffed from any frame.

### A.4 codex (`cliagent/codex.go`) — rewrite to match `codex.go`

`BuildCommand` argv:

```
<binary> exec [resume <id>] --json
  [--skip-git-repo-check]                          # when Sandbox==false
  [--dangerously-bypass-approvals-and-sandbox]     # when Sandbox==false OR PermissionMode==PermissionBypass
  -- <prompt>
```

(anywhered passes `PermissionBypass` + `Sandbox=false` → both flags, exact match.)
`SystemPrompt`, if set, is prepended to the prompt (for `Agent`). Env = `mergeEnv`.

Event mapping (`mapCodexEvent`): sniff `thread_id`; `turn.completed` →
`captureCodexUsage` (`input_tokens`, `output_tokens += reasoning_output_tokens`,
cache = `cached_input_tokens`) + `agent.message{role:"result", raw}`;
`thread.started`/`turn.started` → `agent.message{role:"system", raw}`;
`item.completed` dispatch on `item.type`: `agent_message`→`agent.message{role:"assistant",text}`,
`function_call|tool_call|command_execution`→`agent.tool_call{payload:item}`,
`function_call_output|tool_result|command_output`→`agent.tool_result{payload:item}`,
default→`agent.message{role:itemType, raw:item}`; no-item → `agent.message{payload:obj}`;
`""`→none; default→`agent.message{role:t, raw:obj}`. `SessionID()` = threadID.

### A.5 gemini (`cliagent/gemini.go`) — rewrite to match `gemini.go`

`BuildCommand` argv:

```
<binary> --prompt <prompt> --output-format stream-json
  [--yolo]        # when PermissionMode==PermissionBypass
  [--skip-trust]  # when Sandbox==false
```

(anywhered → both.) Output format is **stream-json**, not `json` (Phase 1 bug).
Event mapping (`mapGeminiEvent`): `init`→set `usage.Model`, `agent.message{role:"system",raw}`;
`message` by role: `user`→`{role:"user",text}`, `assistant`→`{role:"assistant",text,delta:bool}`,
else `{payload:obj}`; `tool_call|tool_use`→`agent.tool_call{payload:obj}`;
`tool_result|tool_response`→`agent.tool_result{payload:obj}`; `result`→`captureGeminiUsage`
+ `agent.message{role:"result",raw}`. `captureGeminiUsage` reads the **aggregate**
`stats.{input_tokens, output_tokens, cached}` (Phase 1 read the wrong per-model
schema). `SessionID()` = "".

### A.6 Mode validation

`WithAllowedModes` lets `BuildCommand` reproduce the real `ErrUnsupportedMode`
gate. anywhered constructs each provider with its modes list (claude:
`headless-code, terminal-task, browser-task, desktop-task, computer-task`; codex
& gemini: `headless-code, terminal-task`). The module owns the check; the list is
injected per app.

### A.7 mapJSONLines (`cliagent/jsonlines.go`)

Non-JSON / non-`{`/`[`-leading lines → `terminal.output` `{"line": line}` (Phase 1
used `"text"`). Every JSON frame event carries `Payload["raw"]` where the real
mapper does, so consumers' stats extraction keeps working.

### A.8 plugin (`cliagent/plugin.go`) — match `plugin.go`

`loadPluginMCPServers(plugins []PluginRef, env map[string]string) (map[string]any, error)`:
**error** on empty `Path` or missing/Non-dir plugin directory; skip a plugin with
no `.mcp.json`; per-plugin variable pool = `env` + `CLAUDE_PLUGIN_ROOT=<plugin path>`;
resolve refs; merge last-write-wins. `resolveRefs` matches `${VAR}` with
**uppercase-only** `[A-Z_][A-Z0-9_]*`, unknown vars left literal, in-place over
map/slice/string. `writeMCPConfig(dir, servers)` writes `{"mcpServers":servers}`
to `<dir>/<filename>` (0600) and returns the absolute path. The exported
`WritePluginMCPConfig` from Phase 1 is reconciled to this signature/behavior.

### A.9 pty (`cliagent/pty/pty.go`) — match `runtime/pty.go`

Adopt: `SysProcAttr{Setsid:true}` process group; `pty.Setsize` 40×120 (configurable
via a `Command.Winsize` field defaulting to 40×120); concurrent read goroutine with
mutex-guarded full buffer; cancellation = `SIGINT` to `-pid`, 2s grace, then
`SIGKILL`; return nil error on `*exec.ExitError` (exit code captured) and on `io.EOF`.
Keep the `Command{Argv,Env,WorkDir,Stdin}` struct (Stdin unused by anywhered; kept
for providers that pass prompt via stdin). On ctx cancel the runner still returns
`ctx.Err()` (the daemon checks `ctx.Err()` itself, so behavior is unchanged); this is
the one intentional deviation from the old runner's nil-on-cancel, and it is
behavior-neutral for the caller.

### A.10 Phase 1 test rewrites

Phase 1 tests encode the simpler behavior and must be rewritten to the new
contract (TDD: update the test to the real expectation, watch it fail, implement).
Add **golden argv/env tests** that assert the exact `claude` / `codex` / `gemini`
command strings `anywhered` produces today.

## B. `anywhered` migration

### B.1 Deletions

Remove `internal/provider/{claude,codex,gemini,linebuf,plugin}.go` and their
tests, and `internal/runtime/pty.go`. **Keep** `internal/provider/echo.go` and
`opencode.go` (app stubs), reimplemented against `agentcli.Provider/Session`.

### B.2 Type switch

`anywhered` code referencing `provider.Request/Event/Result/Usage/Capabilities/
PluginRef/Provider/Session` switches to the `agentcli` equivalents. The interface
method signatures are **identical**, so orchestrator/server call sites barely
change. Test stubs (`fakeProvider`/`fakeSession`, `fastProvider`/`fastSession`)
implement `agentcli` interfaces (e.g. `Capabilities{SupportsPTY:true}`).

### B.3 `LoadFromConfig` adapter (stays app-side)

Keep an app-side `LoadFromConfig(map[string]config.ProviderConfig) *agentcli.Registry`:

```
"claude-code": agentcli.NewClaude(
    WithName("claude-code"), WithBinary(or "claude"), WithBaseEnv(pc.Env),
    WithModelEnv("CLAUDE_MODEL"), WithMCPConfig(".aas-mcp.json", true),
    WithAllowedModes(claudeModes))
"codex":      agentcli.NewCodex(WithName("codex"), WithBinary(or "codex"), WithBaseEnv(pc.Env), WithAllowedModes(codexModes))
"gemini-cli": agentcli.NewGemini(WithName("gemini-cli"), WithBinary(or "gemini"), WithBaseEnv(pc.Env), WithAllowedModes(geminiModes))
"echo" / "opencode-beta": app-local providers
```

`MCP_TOOL_TIMEOUT=1800000` is folded into the claude provider's `WithBaseEnv`.

### B.4 `manager.go`

Build the request with the new fields and inject the AAS bridge as MCP servers
instead of via env detection:

```go
extra := map[string]any{}
if sock != "" {
    if exe, err := os.Executable(); err == nil {
        extra["aas"] = map[string]any{"command": exe, "args": []string{"mcp-bridge", "--socket", sock}}
    }
}
spec, err := sess.BuildCommand(ctx, agentcli.Request{
    RunID: run.ID, Mode: "headless-code", Prompt: prompt, WorkspacePath: run.Cwd,
    Env: reqEnv, ResumeSessionID: run.resumeSessionID, Continue: run.continueLast,
    PermissionMode: agentcli.PermissionBypass, Sandbox: false, ExtraMCPServers: extra,
})
```

Replace `runtime.Run(ctx, spec.Argv, spec.Env, spec.WorkDir, onChunk)` with
`pty.Run(ctx, pty.Command{Argv: spec.Argv, Env: spec.Env, WorkDir: spec.WorkDir}, onChunk)`
and use `pty.Result`. `captureStats(run, ev.Payload)` is unchanged and keeps
reading `Payload["raw"]`.

### B.5 `hooks.go`

Replace the inline payload parsing, `hookSummary`, and `lastAssistantText` with
`agentcli/hooks.{ParsePayload, Summarize, LastAssistantText}`. `Summarize` is
ported to anywhered's exact strings (`→ Bash: cmd`, `✓ Bash`, `turn complete`,
`you: …`, `needs your attention`, default = event name). The daemon transport
stays app-side: `runHook` (inbox POST, pairing token, `ANYWHERE_APP_RUN`,
`TMUX_PANE`, Stop-transcript override), and `installHooks` / `installCodexHooks`
(per-event `<exe> hook <Event> [codex]` command, `stripAnywhereHooks` owner-tag
idempotency, codex `SessionStart`, `~/.codex` existence, printed UX) **remain in
`anywhered`** — they are daemon-command/owner-tag/UX-specific. The module's
`InstallClaude/InstallCodex` exist for future generic consumers but are not used
here.

### B.6 `go.mod`

`require github.com/liliang-cn/agentcli v0.0.0` with
`replace github.com/liliang-cn/agentcli => ../../agentcli` (local, untagged).
Tagging is deferred until this migration validates the API (then the replace can
be dropped for a pinned version).

## C. Testing & verification

- **agentcli**: rewritten unit tests (TDD) + golden argv/env tests reproducing the
  exact `anywhered` commands; `go test -race ./...` green; `go vet`, `gofmt` clean.
- **anywhered**: `go build ./...` green; existing orchestrator/server tests pass
  against the new types; an **echo-provider smoke run** through the daemon confirms
  the orchestrator still drives a run end-to-end (events stream, scrollback, final
  state). Spot-check a real `claude-code` headless run if a CLI is available.

## D. Build order

1. Revise `agentcli` types/options/errors (A.1, A.2) + rewrite Phase 1 tests to
   the new contract.
2. Rewrite claude/codex/gemini providers + golden argv/event/usage tests (A.3–A.6).
3. Reconcile `mapJSONLines`, `plugin`, `pty` (A.7–A.9); `go test -race ./...` green.
4. `anywhered`: add `go.mod` replace; switch types; app-side `LoadFromConfig`
   adapter; reimplement echo/opencode against `agentcli`.
5. `anywhered`: `manager.go` (Request fields + AAS bridge + `pty.Run`); delete
   `internal/runtime/pty.go` and the migrated provider files.
6. `anywhered`: `hooks.go` swap to `agentcli/hooks` helpers (keep transport app-side).
7. `go build ./...` + tests green; echo smoke run.

## Non-goals (Phase 2)

- Migrating `Agent` (Phase 3, validated as a clean fit) or any roma work (out of scope).
- Releasing/tagging `agentcli` (deferred until this migration validates the API).
- Changing the daemon's transport, inbox, checkpoints, store, or service lifecycle.
- Generalizing the module's hook *installers* (anywhered keeps its own).

## Risks

- **Local `replace` coupling** — `anywhered` builds only with `agentcli` checked out
  at `../../agentcli` until a tag exists; acceptable for an unreleased pair.
- **Behavior parity** — mitigated by golden argv/env tests and the echo smoke run.
- **`Summarize` string parity** — ported verbatim; covered by a fixture test.
- **Uppercase-only plugin `${VAR}`** — a deliberate tightening to match the real
  resolver; flagged in case any lowercase-ref plugin config exists.
