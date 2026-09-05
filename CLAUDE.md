# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`github.com/liliang-cn/agentexec` is a **library-only Go module** (no `main`, no binary) that
builds, invokes, and parses the **Claude Code**, **Codex**, **Gemini** and **cursor-agent** CLIs. It was extracted
from near-duplicate `internal/provider` packages in two apps so they could share one
implementation instead of forking it.

The name is the `os/exec` analogy: `os/exec` is for processes, `agentexec` is for agent CLIs.
Package name equals module name, so the import is one line and the call site reads
`agentexec.NewClaude()`.

Three packages, the root one being the library proper:

- `.` (package `agentexec`) — command construction, stream-json/JSONL parsing, usage accounting,
  `LineBuffer`, plugin MCP config merging, and discovery (`Discover` / `RegistryFrom` in
  `discover.go`: which of the four CLIs are on PATH, and a registry bound to their binaries).
  `cursor.go` is a claude session with only `BuildCommand` replaced — cursor-agent's stream-json
  is Claude Code's dialect frame for frame.
- `pty/` — a provider-agnostic PTY runner (only external dep: `github.com/creack/pty`).
- `hooks/` — Claude Code / Codex hook payload parsing, transcript reading, hook installation.
  It does not import `agentexec`.

## Commands

```bash
go build ./... && go vet ./... && go test ./...
go test . -run TestClaudeGoldenArgv               # single test
go test -race ./...
gofmt -l . && go mod tidy
```

Tests need no network and no real `claude`/`codex`/`gemini` binary — providers are exercised
through golden argv assertions and recorded JSONL line fixtures. The exception is `pty`,
which spawns a real PTY running `printf`/`cat`.

## The governing constraint: app-agnostic

**No business logic, no app config structs, no transport, no SaaS/policy text belongs in this
module.** App-specific concerns are injected by the caller — policy prose via `Request.SystemPrompt`,
anything else via `Request.ExtraArgs` / `Request.Env` / functional `Option`s. When a consumer needs
something new, the question is always whether it can be expressed as an injected value rather than
a new field with app semantics.

## Architecture

### Provider / Session lifecycle

`Provider` (`NewClaude`/`NewCodex`/`NewGemini`) is stateless and reusable; `NewSession()` returns a
stateful `Session` for exactly one invocation:

1. `BuildCommand(ctx, Request) (CommandSpec, error)` — pure argv/env/workdir construction.
2. `ParseChunk(chunk) ([]Event, error)` — called repeatedly with streamed bytes.
3. `Finalize(ctx, fullOutput, exitCode) (Result, []Event, error)` — terminal outcome + tail events.
4. `SessionID()` — the id sniffed out of the stream, for a later `Request.ResumeSessionID`.

Sessions accumulate `Usage`, session/thread id, and the summary as a side effect of parsing, so a
`Session` must not be reused across invocations. Construction options
(`WithName`/`WithBinary`/`WithBaseEnv`/`WithModelEnv`/`WithMCPConfig`/`WithAllowedModes`) replace
each consuming app's bespoke `ProviderConfig` struct; `Registry` maps names to providers.

### Parsing pipeline

Bytes → `LineBuffer.Feed` (strips `\r`, buffers the partial tail) → `mapJSONLines` → the provider's
own `map*Event` mapper → canonical `Event`s. `mapJSONLines` only attempts to parse `{`-leading
lines; everything else becomes `EventTerminalOutput` keyed `"line"`. Each provider normalizes its
frames onto the same five event types (`agent.message`, `agent.tool_call`, `agent.tool_result`,
`terminal.output`, `provider.rate_limit`), so app code never branches on provider.

`Finalize` handles both calling styles via `LineBuffer.Fed()`: a caller that streamed through
`ParseChunk` gets only the buffered tail; a caller that collected everything and passed it as
`fullOutput` gets it parsed there. Do not "simplify" this into a flush-only path — it silently
returns an empty `Result` for the collect-then-finalize caller.

### Provider divergences worth knowing

| | claude | codex | gemini | cursor-agent |
|---|---|---|---|---|
| `SystemPrompt` | `--append-system-prompt` | prepended to the prompt | prepended to the prompt | prepended to the prompt |
| Bypass | `--permission-mode bypassPermissions` | `--dangerously-bypass-approvals-and-sandbox` | `--yolo` | `--force` |
| Headless (`Sandbox` false) | — | `--skip-git-repo-check` | `--skip-trust` | `--trust --sandbox disabled` |
| Session id | `session_id` | `thread_id` | none | `session_id` (claude dialect) |
| MCP / plugins | yes | — | — | — |

`Request.Sandbox` is deliberately inverted: the **zero value means headless**, emitting the
skip-sandbox/skip-trust/skip-git-check flags. `true` means run inside the CLI's own approval flow.

## Non-obvious invariants (each is a bug that was already paid for)

- **`Event.Payload["role"]` is how you find the model's actual answer.** Claude's `system` init,
  hook lifecycle frames, and the `result` summary all map to `EventAgentMessage`. Filter on
  `Type == EventAgentMessage && Payload["role"] == "assistant"`. Filtering on non-empty
  `Payload["text"]` happens to work today but is not a contract.
- **`Result.Failed` is not the exit code.** Only Claude reports a verdict (`is_error` on its
  result frame); a revoked OAuth token yields an assistant message, `subtype: "success"`, and exit
  zero. Codex uses its `error` item for warnings too, so inventing a verdict there would mark
  healthy turns failed — it stays `false` for codex and gemini on purpose.
- **`--mcp-config` is variadic.** Its path must not be the last flag before the prompt, or the CLI
  eats the prompt as a second config path. `claude.go` appends `--strict-mcp-config` after it to
  guarantee this, which is why `NoMCP` forces the strict flag.
- **`Request.NoMCP` is distinct from an empty `ExtraMCPServers`.** No servers means no
  `--mcp-config` at all, which means the CLI loads every MCP server the operator has configured —
  wrong when the CLI is being used as an inference backend. `NoMCP` writes an empty config instead.
- **Codex hooks need the same nested shape as Claude** (`{event: [{hooks: [{type, command}]}]}`).
  A flat `[{command}]` entry is accepted and silently never fires (verified on Codex 0.141).
- **`hooks.SlashCommand` spans four events.** `/compact` arrives as `PreCompact` with trigger
  `manual`, `/clear` as `SessionEnd` with reason `clear`, a typed command as `UserPromptSubmit`,
  and an agent-run one as `PostToolUse` for tool `SlashCommand`. Keep that knowledge in `slash.go`
  rather than letting consumers rediscover it.
- **`hooks.TurnText` vs `LastAssistantText`.** The latter returns only the closing message, which
  for a narrating agent is "done". `TurnText` returns every assistant block since the last prompt a
  human actually typed — user lines carrying only `tool_result` blocks don't count as prompts.
- Both installers are **idempotent** and merge into existing settings; never rewrite
  `~/.claude/settings.json` wholesale.

## Consumers and versioning

**Renamed from `agentcli` (package `cliagent`) at v0.1.4.** The old name read as "a CLI agent"
— a binary — when this is a library that drives other people's agent CLIs, and the module/package
pair were two orderings of the same two words. Tags up to `v0.1.4` are still resolvable under the
old path; anything new is tagged under `github.com/liliang-cn/agentexec`. Downstream consumers pin
real tags (no local `replace`), so a path change only reaches them once a tag is published:

- `~/Things/AI/projects/anywhere/anywhered`
- `~/Things/dev/apps/Agent` (`agent-as-a-service`)
- `~/Things/AI/base/agent-go` (`pkg/agent/builtin_tools_cliagent.go` — `cli_agent_list` /
  `cli_agent_run`; it calls `Discover` and `RegistryFrom` and owns nothing about the CLIs itself.
  Discovery and the cursor provider used to live there as `pkg/agent/cliagents`; they moved here
  because which CLIs exist and how to drive them is this library's question, not an agent
  framework's.)

Any change to `Request`, `Event` shapes, or emitted argv ripples into both. `docs/superpowers/`
holds the design specs and migration plans that produced this module; they predate the rename and
still say `agentcli`/`cliagent` throughout — they are dated records, left as written.
