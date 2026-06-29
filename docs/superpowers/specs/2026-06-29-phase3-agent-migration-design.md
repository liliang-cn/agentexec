# Phase 3 — Migrate `agent-as-a-service` onto `agentcli`

Date: 2026-06-29
Status: Approved (brainstorming)
Module: `github.com/liliang-cn/agentcli`
Consumer migrated this phase: `agent-as-a-service` (`github.com/liliang-cn/agent-as-a-service`, repo root `/Users/liliang/Things/AI/projects/Agent`)

## Purpose

`Agent`'s `internal/provider` is a near-identical twin of the (now-migrated) `anywhered`
provider package, and its `internal/runtime` PTY runner matches too. Migrate `Agent`
off both onto `agentcli` (`cliagent` + `cliagent/pty`), functionally unchanged. This is
the second consumer (after `anywhered`) that validates the shared module.

## Decisions (resolved during brainstorming)

1. **SaaS system prompt → `Request.SystemPrompt`, set by the worker.** Agent's per-provider,
   tool-aware SaaS prompt moves into an app-side helper; the worker selects by
   `run.Provider` + tool-awareness and sets `Request.SystemPrompt`. `agentcli` injects it
   (claude `--append-system-prompt`; codex prepend). Module stays app-agnostic. (Chosen over
   app-local wrapper providers, which would re-fork command-building.)
2. **`agentcli` needs no changes.** Its API already covers everything Agent needs
   (`SystemPrompt`, `ExtraMCPServers`, `PermissionMode`/`Sandbox`, the options).
3. **`UserID`/`ProjectID`/`PolicyJSON` stay app-side** in Agent's `run` struct — no provider's
   command-building reads them (the worker reads `PolicyJSON` for cost/timeout). They do not
   enter `agentcli.Request`.

## Monorepo constraint (critical)

`/Users/liliang/Things/AI/projects/Agent` is a **monorepo in one git repo**: the Go backend
at the root, plus an Expo **React Native app at `apps/mobile/`**, a **`web/`** frontend
(`aas-web`), screenshots, `cortexdb.db`, etc. This migration touches **only the Go module**.
Every commit MUST be scoped to explicit Go paths (`internal/provider`, `internal/runtime`,
`internal/worker`, `go.mod`, `go.sum`) — never `git add -A`/`git add .`, and never touch
`apps/mobile/`, `web/`, image/binary artifacts, or `cortexdb.db*`. (The separate project
`/Users/liliang/Works/InceptionPad/automation-test` is unrelated and out of scope.)

## A. `agentcli` changes

None. The module is consumed as-is via a local `replace`.

## B. `Agent` changes (mirrors the anywhered migration)

References: `internal/provider/{claude,codex,gemini,plugin,linebuf,provider,echo,opencode}.go`,
`internal/worker/worker.go`, `internal/runtime/pty.go`.

### B.1 `internal/provider` shrinks to app-local

- **Rewrite** `echo.go` and `opencode.go` against `cliagent` types (`cliagent.Provider`/`Session`/
  `Request`/`CommandSpec`/`Event`/`Result`/`Capabilities`); use `&cliagent.LineBuffer{}`;
  `Capabilities` uses only cliagent fields (drop `Modes`/`SupportsStreamJSON`/`SupportsServerMode`);
  opencode validates `req.Mode` against a local list → `cliagent.ErrUnsupportedMode`. echo's
  shell script + `shellQuote` and the shared `linesToTerminalOutput` (emitting
  `cliagent.EventTerminalOutput{"line"}`) are preserved.
- **Create** `registry.go`: `LoadFromConfig(map[string]config.ProviderConfig) *cliagent.Registry`
  wiring `claude-code`/`codex`/`gemini-cli` via `cliagent.NewClaude/NewCodex/NewGemini` with the
  same options as anywhered (`WithName`, `WithModelEnv("CLAUDE_MODEL")`,
  `WithMCPConfig(".aas-mcp.json", true)`, `WithBaseEnv` incl. `MCP_TOOL_TIMEOUT=1800000` for
  claude, `WithAllowedModes`), plus app-local `echo`/`opencode-beta`.
- **Create** `saasprompt.go`: the moved SaaS prompt helpers (see B.3).
- **Delete** `claude.go`, `codex.go`, `gemini.go`, `plugin.go`, `linebuf.go`, `provider.go`
  and all their `_test.go`.

### B.2 Delete `internal/runtime`

Remove `internal/runtime/pty.go`; the worker uses `cliagent/pty` instead.

### B.3 SaaS prompt (`internal/provider/saasprompt.go`)

Move the prompt text out of the deleted claude/codex providers into app-side helpers,
verbatim except for the codex separator fix:

- `saasSystemPrompt(withTools bool) string` — claude, preserved verbatim (tool-aware: the
  artifact.save/human.await/CHECKPOINTS guidance is included iff `withTools`).
- `codexSaasPrompt() string` — codex, preserved **but with the trailing `\n\n` removed**.
  Rationale: `agentcli`'s codex prepend computes `SystemPrompt + "\n\n" + Prompt`; the original
  ended with `--- END SYSTEM ---\n\n`, so trimming the trailing `\n\n` makes the module's
  separator reproduce the exact original bytes (`--- END SYSTEM ---\n\n` + userPrompt).
- `saasPromptFor(provider string, withTools bool) string` — returns `saasSystemPrompt(withTools)`
  for `claude-code`, `codexSaasPrompt()` for `codex`, and `""` for `gemini-cli`/`opencode-beta`/`echo`.

### B.4 `internal/worker/worker.go`

- Imports: drop `internal/provider` references to the deleted types; use `cliagent` +
  `cliagent/pty`. Keep importing the app-local `provider` package only for `LoadFromConfig`
  and `saasPromptFor`. The plugin refs the worker resolves become `[]cliagent.PluginRef`
  (the `PluginRef` type now comes from `cliagent`); update that construction accordingly.
- The `provider.Request{...}` literal → `cliagent.Request{...}`:
  - Keep: `RunID, Mode, Prompt, WorkspacePath, Env, Plugins, ResumeSessionID`.
  - **Drop** `UserID`, `ProjectID`, `PolicyJSON` from the literal (the worker keeps reading them
    from `run` directly where it already does — cost cap / timeout).
  - **Add** `PermissionMode: cliagent.PermissionBypass`, `Sandbox: false`,
    `ExtraMCPServers: extraMCP`, and `SystemPrompt: provider.saasPromptFor(run.Provider, toolsEnabled)`
    where `toolsEnabled` is true when the per-run tool server is active (the same condition that
    sets `Env["AAS_TOOL_SOCKET"]`).
  - Build `extraMCP` (the AAS human.await MCP bridge) the same way the deleted claude.go did —
    from `os.Executable()` + the bridge subcommand/args, keyed `"aas"` — in the worker block
    where the socket is in scope. (Plan must copy the exact bridge command/args from the current
    `internal/provider/claude.go` so it is byte-identical.)
- `runtime.Run(runCtx, spec.Argv, spec.Env, spec.WorkDir, onChunk)` →
  `pty.Run(runCtx, pty.Command{Argv: spec.Argv, Env: spec.Env, WorkDir: spec.WorkDir}, onChunk)`.
  The `onChunk`/`ParseChunk`/`recordRateLimit`/`emit` loop, `Finalize`, and the
  `store.InsertProviderUsage` persistence of `finalRes.Usage` are unchanged.

### B.5 Plugin handling

Agent's claude loaded plugin `.mcp.json` servers and merged them with the AAS bridge into
`.aas-mcp.json`. In the new design this is handled inside `agentcli`'s claude `BuildCommand`
from `Request.Plugins` + `Request.ExtraMCPServers` (exactly as for anywhered). The worker keeps
resolving `pluginRefs` and passes them as `Request.Plugins`; `agentcli` writes the merged
`.aas-mcp.json` + `--mcp-config --strict-mcp-config`. No app-local plugin code remains.

### B.6 `go.mod`

`require github.com/liliang-cn/agentcli v0.0.0` + `replace github.com/liliang-cn/agentcli => ../agentcli`
(Agent's repo root is one level above the `agentcli` checkout: `projects/Agent` → `projects/agentcli`).

## C. Test stubs

The earlier survey found Agent's provider tests use real providers + fixtures, not Session-interface
mock stubs. The plan must re-check for any `provider.{Provider,Session,Request,...}` references in
`*_test.go` (and elsewhere) and swap them to `cliagent.*`. The deleted claude/codex/gemini/plugin/linebuf
tests go away with their sources; any remaining test referencing deleted symbols is fixed or removed.

## D. Verification

- `go build ./...`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .` green/clean
  in the Agent repo.
- Provider-usage DB persistence path intact (`store.InsertProviderUsage` still receives `finalRes.Usage`).
- Argv/prompt parity: claude `--append-system-prompt` carries `saasSystemPrompt(withTools)` byte-for-byte;
  codex prompt = `codexSaasPrompt()`(trimmed)`\n\n`+userPrompt byte-for-byte; the AAS bridge server map is
  identical to the old claude.go. One accepted non-byte difference: claude's order-independent flags follow
  `agentcli`'s fixed order (functionally identical).
- An echo-provider smoke run through the worker if a non-interactive entrypoint exists; otherwise rely on
  the `-race` suite (which exercises the drive loop) and `agentcli`'s golden tests, reported honestly.

## Non-goals (Phase 3)

- The React Native app (`apps/mobile/`), the `web/` frontend, and all non-Go artifacts — untouched.
- Hooks (Agent has none; it uses webhooks).
- Releasing/tagging `agentcli` (still consumed via local `replace`).
- The unrelated `automation-test` project.

## Risks

- **Monorepo commit scoping** — the single biggest operational risk; mitigated by explicit-path
  `git add` and a `git status` check before every commit (never `-A`).
- **SaaS prompt byte parity** — rests on copying `saasSystemPrompt` verbatim and the codex
  trailing-`\n\n` trim; covered by a parity check in the plan.
- **Local `replace` coupling** — Agent builds only with `agentcli` at `../agentcli`; drop once tagged.
- **`go.sum`/`creack/pty` version** — Agent already requires `creack/pty v1.1.24` (same as agentcli);
  `go mod tidy` should reconcile without conflict.
