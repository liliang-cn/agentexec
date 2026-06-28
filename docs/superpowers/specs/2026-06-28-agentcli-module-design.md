# agentcli — Shared Go Module Design (Phase 1)

Date: 2026-06-28
Status: Approved (brainstorming)
Module: `github.com/liliang-cn/agentcli` (new standalone repo at `~/Things/AI/projects/agentcli`)

## Purpose

Three projects build/invoke and parse the **Claude Code** and **Codex** CLIs and
(in one case) their **hooks**. `anywhere` (`anywhered`) and `Agent`
(`agent-as-a-service`) carry a **near-duplicated** `internal/provider` package
(command building + stream-json/JSONL parsing + usage + LineBuffer + plugin MCP
config + PTY runner). `anywhere` additionally implements Claude Code / Codex hook
parsing + installation. Extract the shared, app-agnostic core into one module so
all three can depend on it instead of forking it.

## Phase scope

**Phase 1 (this spec): build the `agentcli` module, standalone, with tests. No
consumers migrated yet.** Later phases migrate `anywhere`, then `Agent`, then add
`hooks` to `roma`. ROMA's PTY-text-classification paradigm is out of scope for
`cliagent` (it would only consume `hooks`, and `LineBuffer`/parsers if it ever
moves to structured output).

## Design principles

- **App-agnostic.** No business logic, no app config structs, no transport
  (HTTP/daemon). App-specific concerns (SaaS system prompt, policy JSON, daemon
  inbox POST) are **injected by the caller**, not baked in.
- **Reconcile the two near-duplicates into one superset API**, parameterizing the
  bits where `anywhere` and `Agent` diverge.
- Pure, isolated, testable units; no live CLIs needed for unit tests.

## Module layout

```
github.com/liliang-cn/agentcli
├── go.mod                       (module path; Go 1.25; dep: github.com/creack/pty)
├── cliagent/
│   ├── types.go                 Request, CommandSpec, Event, Result, Usage,
│   │                            Capabilities, PluginRef, canonical Event type consts
│   ├── provider.go              Provider, Session interfaces; Registry
│   ├── linebuf.go               LineBuffer (Feed/Flush)
│   ├── jsonlines.go             mapJSONLines(lines, mapper)
│   ├── claude.go                claude Provider/Session: BuildCommand + stream-json parse + usage
│   ├── codex.go                 codex Provider/Session: BuildCommand + --json parse + usage
│   ├── gemini.go                gemini Provider/Session (Agent has it; include, optional to register)
│   ├── plugin.go                loadPluginMCPServers, resolveRefs, writeMCPConfig
│   └── *_test.go
├── cliagent/pty/
│   ├── pty.go                   Run(ctx, argv, env, workDir, onChunk) (Result, error)
│   └── pty_test.go
└── hooks/
    ├── payload.go               HookEvent payload types + ParsePayload(io.Reader)
    ├── transcript.go            LastAssistantText(path) — Claude JSONL transcript reverse scan
    ├── install.go               InstallClaude(opts), InstallCodex(opts) — settings.json / hooks.json
    └── *_test.go
```

## cliagent API (reconciled superset)

```go
// Request is the app-agnostic input to BuildCommand. App-specific policy text is
// passed via SystemPrompt; anything else via ExtraArgs/Env. (No PolicyJSON/UserID/
// ProjectID/saas baked in — those stay in the app.)
type Request struct {
    RunID           string
    Mode            string            // free-form, app-defined
    Prompt          string
    SystemPrompt    string            // claude: --append-system-prompt; codex/gemini: prepended to prompt
    WorkspacePath   string
    Model           string            // optional; provider maps to its model flag
    Env             map[string]string
    Plugins         []PluginRef       // claude --plugin-dir + .mcp.json merge
    MCPConfigPath   string            // optional precomputed --mcp-config path
    ResumeSessionID string            // claude --resume / codex resume <id>
    Continue        bool              // claude --continue
    ExtraArgs       []string          // escape hatch appended before the prompt
}

type CommandSpec struct { Argv []string; Env []string; WorkDir string; Stdin []byte }

type Event struct { Type string; Payload map[string]any }

// Canonical event types (string consts): EventAgentMessage="agent.message",
// EventToolCall="agent.tool_call", EventToolResult="agent.tool_result",
// EventTerminalOutput="terminal.output", EventRateLimit="provider.rate_limit".

type Usage struct {
    Model            string
    InputTokens      int64
    OutputTokens     int64
    CacheTokens      int64
    EstimatedCostUSD float64
}

type Result struct { ExitCode int; Summary string; Usage Usage }

type Capabilities struct { Streaming bool; Resume bool; Plugins bool; MCP bool }

type PluginRef struct { Name, Path string }

type Provider interface {
    Name() string
    Capabilities() Capabilities
    NewSession() Session
}

type Session interface {
    BuildCommand(ctx context.Context, req Request) (CommandSpec, error)
    ParseChunk(chunk []byte) ([]Event, error)        // feeds LineBuffer + provider mapper
    Finalize(ctx context.Context, fullOutput []byte, exitCode int) (Result, []Event, error)
    SessionID() string                               // sniffed session/thread id (for resume)
}

type Registry struct{ /* name -> Provider */ }
func NewRegistry() *Registry
func (r *Registry) Register(p Provider)
func (r *Registry) Get(name string) (Provider, error)
func (r *Registry) Names() []string

// Provider constructors (binary path + default env overridable):
func NewClaude(opts ...Option) Provider
func NewCodex(opts ...Option) Provider
func NewGemini(opts ...Option) Provider
// Option: WithBinary(path), WithBaseEnv(map), WithModelEnv(key) — functional options
// replace each app's bespoke ProviderConfig struct.
```

- `LineBuffer`: `Feed([]byte) []string`, `Flush() []string` (strip `\r`, buffer partial tail).
- `mapJSONLines(lines []string, mapper func(map[string]any) []Event) []Event`: JSON-object
  lines → mapper; non-JSON lines → `Event{EventTerminalOutput, {"text": line}}`.
- claude `mapClaudeEvent` handles frames `system|assistant|user|result|rate_limit_event`,
  `extractAssistantContent`, and usage via `setUsageFromObject`/`addUsageFromObject`.
- codex `mapCodexEvent` handles `thread.started|turn.started|item.completed|turn.completed`
  with `item.type` sub-dispatch, usage via `captureCodexUsage`.

## hooks API

```go
// HookEvent is the parsed Claude Code / Codex hook payload (read from hook stdin).
type HookEvent struct {
    EventName       string   // PreToolUse|PostToolUse|Notification|Stop|SubagentStop|UserPromptSubmit|SessionStart
    SessionID       string
    Cwd             string
    ToolName        string
    ToolInput       map[string]any
    Command         string   // convenience: tool_input.command if present
    LastAssistant   string   // last_assistant_message if present
    TranscriptPath  string
    Raw             map[string]any
}

func ParsePayload(r io.Reader) (HookEvent, error)        // bounded read + JSON parse
func Summarize(ev HookEvent) string                       // short human line per event type
func LastAssistantText(transcriptPath string) (string, error)  // reverse-scan Claude JSONL

// Installers register a command for the standard events; transport is the caller's job.
type InstallOptions struct {
    Command string   // e.g. "/path/to/exe hook"   (event name appended by caller convention)
    Events  []string // default: PreToolUse, PostToolUse, Notification, Stop, UserPromptSubmit
    Matcher string   // default "*"
}
func InstallClaude(opts InstallOptions) error             // merge into ~/.claude/settings.json
func InstallCodex(opts InstallOptions) error              // write ~/.codex/hooks.json + enable [features] hooks
```

- `ParsePayload`, `Summarize`, `LastAssistantText` are pure (fixtures testable).
- Installers take a `home`/path override for tests (e.g. `InstallOptions.ClaudeDir`,
  `CodexDir`, defaulting to `~/.claude` / `~/.codex`) so tests use `t.TempDir()`.

## Reconciliation notes (anywhere vs Agent)

- `anywhere` has an `echo` provider (test stub) — leave it in the apps, not the module.
- `Agent` injects a SaaS system prompt + `PolicyJSON` — these become caller-supplied
  `Request.SystemPrompt` (and the app keeps policy logic). No SaaS text in the module.
- `anywhere` has `Plugins`/plugin MCP; `Agent` has `gemini`. The module includes BOTH
  (`PluginRef`/`plugin.go` and `gemini.go`); apps register only what they use.
- Both apps' `ProviderConfig{Enabled,Binary,Env}` → functional `Option`s on the
  constructors; the module does not import either app's config package.

## Testing

- `linebuf`, `jsonlines`: table-driven, pure.
- `claude.go`/`codex.go`/`gemini.go`: feed recorded stream-json/JSONL line fixtures →
  assert emitted `Event`s + accumulated `Usage`. No live CLI.
- `BuildCommand`: assert argv/env/stdin for representative `Request`s (resume, model,
  plugins, system prompt → claude `--append-system-prompt` vs codex prepend).
- `plugin.go`: temp `.mcp.json` fixtures, `${VAR}` resolution, merged write.
- `pty`: integration-style (real PTY running `printf`/`cat`), like the source repos.
- `hooks`: payload fixtures, transcript JSONL fixtures, installers against `t.TempDir()`.
- CI: `go test ./...` with no network, no real claude/codex.

## Non-goals (Phase 1)

- Migrating `anywhere`/`Agent`/`roma` (later phases, each its own spec/plan).
- App transport (daemon inbox POST), app config, SaaS policy text.
- ROMA's PTY-text classifier / ROMA_* markers (stay in roma).
- Releasing/tagging (a later step once a consumer validates the API).

## Phase 1 build order

1. Repo init + `go.mod` + `cliagent` types/interfaces.
2. `LineBuffer` + `mapJSONLines`.
3. claude provider (BuildCommand + parse + usage) + tests.
4. codex provider (+ gemini) + tests.
5. `plugin.go` + tests.
6. `cliagent/pty` runner + tests.
7. `hooks` package (payload/transcript/summarize/installers) + tests.
8. `go test ./...` green; tag-ready but untagged until a consumer (Phase 2) validates.
