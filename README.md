# agentexec

What `os/exec` is to processes, `agentexec` is to agent CLIs: run **Claude
Code**, **Codex**, **Gemini**, **cursor-agent**, **Qwen Code**, **Kimi CLI**,
**opencode**, **GitHub Copilot CLI**, **goose**, **pi**, **agy**, **Hermes**
and **aider** from Go — build the argv, parse what they stream back, and get
one canonical event and result shape out the other end.

The CLIs are the most capable agent runtimes most people already have installed
— and each one takes different flags, streams a different JSON dialect, and
reports usage and failure differently. This library is the part every app that
shells out to them ends up writing, extracted once:

- **Command building** — modes, models, system prompts, resume/continue,
  sandbox posture, MCP config, plugin directories, per-provider flag names
- **Stream parsing** — `stream-json` / JSONL frames normalized to one `Event`
  type across providers
- **Usage and result accounting** — tokens, cost, and an honest failure verdict
- **A PTY runner** — some CLIs behave differently without a terminal
- **Hook helpers** — install hook commands into `~/.claude` / `~/.codex`, then
  parse the payloads they send you

No app config, no transport, no business logic, no opinion about where output
goes. Everything app-specific is injected by the caller.

```sh
go get github.com/liliang-cn/agentexec
```

Requires Go 1.25. Only dependency: `github.com/creack/pty`.

## Usage

One turn, end to end:

```go
sess := agentexec.NewClaude().NewSession()

spec, err := sess.BuildCommand(ctx, agentexec.Request{
	Prompt:         "summarise README.md in one line",
	WorkspacePath:  "/path/to/repo",
	PermissionMode: agentexec.PermissionBypass,
	NoMCP:          true,
})
if err != nil {
	return err
}

var answer strings.Builder
res, err := pty.Run(ctx, pty.Command{
	Argv: spec.Argv, Env: spec.Env, WorkDir: spec.WorkDir, Stdin: spec.Stdin,
}, func(chunk []byte) {
	events, _ := sess.ParseChunk(chunk)
	for _, e := range events {
		// Lifecycle frames land on agent.message too — role separates them.
		if e.Type == agentexec.EventAgentMessage && e.Payload["role"] == "assistant" {
			answer.WriteString(e.Payload["text"].(string))
		}
	}
})
if err != nil {
	return err
}

out, _, err := sess.Finalize(ctx, res.Output, res.ExitCode)
// out.Failed, out.Usage.InputTokens, out.Usage.EstimatedCostUSD, out.Summary
```

`Finalize` also parses non-streamed output, so a caller that never wired
`ParseChunk` still gets a filled-in `Result`.

Providers register into a `Registry` when an app supports more than one:

```go
reg := agentexec.NewRegistry()
reg.Register(agentexec.NewClaude())
reg.Register(agentexec.NewCodex(agentexec.WithBinary("/opt/homebrew/bin/codex")))
reg.Register(agentexec.NewGemini())

p, err := reg.Get("codex")          // reg.Names() is sorted
caps := p.Capabilities()            // Streaming, Resume, Plugins, MCP, SupportsPTY, …
```

Options: `WithBinary`, `WithName`, `WithBaseEnv`, `WithModelEnv`,
`WithMCPConfig`, `WithAllowedModes`. `Request.Mode` is free-form and
app-defined; `BuildCommand` returns `ErrUnsupportedMode` when the provider was
configured with an allowlist that excludes it.

## Discovery

Which of the thirteen are on this machine, and a registry ready to run them:

```go
found := agentexec.Discover(nil)                       // []Installed: Name, Binary, Version, Streaming, Resume
reg := agentexec.RegistryFrom(found, agentexec.WithMCPConfig(".app-mcp.json", true))
```

The map argument overrides a binary path per name and admits aliases:
`{"claude-work": "/opt/claude-beta"}` is listed as `claude-work` and driven
as claude. A name that matches none of the known dialects is dropped.

`Installed` means the binary exists. Whether the account behind it is still
signed in is only knowable by running it — `Version` comes from `--version`,
which every one of them answers even with an expired login.

## Which CLI speaks what

| CLI | constructor | headless argv | stream | session id | verdict |
|---|---|---|---|---|---|
| claude | `NewClaude` | `claude --print --output-format stream-json` | Claude dialect | `session_id` | `is_error` |
| codex | `NewCodex` | `codex exec --json` | JSONL items | `thread_id` | none |
| gemini | `NewGemini` | `gemini --prompt … --output-format stream-json` | gemini | none | none |
| cursor-agent | `NewCursor` | `cursor-agent --print --output-format stream-json` | Claude dialect | `session_id` | `is_error` |
| qwen | `NewQwen` | `qwen --output-format stream-json <prompt>` | Claude dialect | `session_id` | `is_error` |
| kimi | `NewKimi` | `kimi --print --output-format stream-json --prompt …` | one chat message per line | none | none |
| opencode | `NewOpencode` | `opencode run --format json <prompt>` | `{type, sessionID, part}` | `sessionID` | `error` frame |
| copilot | `NewCopilot` | `copilot --output-format json --allow-all-tools --prompt …` | `{type:"area.event", data}` | `result.sessionId` | `result.exitCode` |
| goose | `NewGoose` | `goose run --output-format stream-json --text …` | `{type:"message"}` + `complete` | banner line | none |
| pi | `NewPi` | `pi --print --mode json <prompt>` | agent-core events | `session.id` | `stopReason:"error"` |
| agy | `NewAgy` | `agy --output-format stream-json --print …` | `{event, <event>:{…}}` | `conversation_id` | `result.status` |
| hermes | `NewHermes` | `hermes --usage-file … --oneshot …` | plain text + usage file | usage file | usage file `failed` |
| aider | `NewAider` | `aider --message … --yes-always …` | plain text | none | none |

The last two are text providers. `NewText` builds one for any other CLI that
answers in prose: `WithBinary`, `WithArgv("--run", "{prompt}")` and optionally
`WithModelFlag("--model")`. Every output line is a `terminal.output` event and
`Finalize` adds one assistant-role `agent.message` holding the whole answer.

## Four behaviours worth knowing before you rely on them

Each of these is a bug someone has to hit once. They are decided here so that
does not have to keep happening.

**`Result.Failed` is not the exit code.** A `claude` whose OAuth token has been
revoked writes "Failed to authenticate" as an assistant message, sets
`is_error` on its result frame, and **exits zero**. Read only the message and
the exit code and you write an authentication failure into a file as if it were
the model's answer. `Failed` is read only where the CLI states a verdict (see
the table above); Codex's `error` item also carries warnings, so mapping it
would mark healthy turns failed, and Gemini, kimi, goose and aider have no
signal at all — for those `Failed` stays false rather than being invented.

**Filter `agent.message` by role.** Provider lifecycle frames — Claude's
`system` init, its hook events, the `result` summary — map onto
`EventAgentMessage` as well, distinguished by `Payload["role"]`. One "say OK"
call produced eleven of them, ten being hook lifecycle. Filtering on `text`
being non-empty happens to work today; that is a coincidence of the current
mapping, not a contract.

**`NoMCP` is not the same as no MCP servers.** An empty `ExtraMCPServers` map
means no `--mcp-config` flag, which means the CLI loads everything the developer
has configured — right for an interactive session, wrong for using the CLI as an
inference backend, where booting every server took longer than the model spent
thinking and the call could reach the operator's own servers. `NoMCP: true`
passes an explicitly empty config instead. It yields to `ExtraMCPServers` and
`Plugins` when those supply servers.

**`Sandbox` is false by default, and that means headless.** The zero value emits
the skip-sandbox / trust / skip-git-check flags — the posture you want when an
app is driving the CLI unattended. Set it to `true` to run inside the CLI's own
sandbox and approval flow.

## Hooks

`hooks` handles the other direction: the CLI calling your app.

```go
hooks.InstallClaude(hooks.InstallOptions{Command: "myapp hook"}) // merges into ~/.claude/settings.json
hooks.InstallCodex(hooks.InstallOptions{Command: "myapp hook"})  // ~/.codex/config.toml

ev, err := hooks.ParsePayload(os.Stdin)  // in your hook command
hooks.Summarize(ev)                      // one-line human description
hooks.SlashCommand(ev)                   // "/compact" etc., "" when not one
hooks.LastAssistantText(ev.TranscriptPath)
hooks.TurnText(ev.TranscriptPath)
```

Installation is a merge, not a write: other settings survive, and re-running
with the same command does not create duplicate hook groups. Default events are
`PreToolUse`, `PostToolUse`, `Notification`, `Stop`, `UserPromptSubmit`.

## Delegating over MCP

`cmd/agentexec-mcp` is an MCP server built on this library: it lets one agent
hand a whole task to a different agent CLI. It is a separate Go module, so
depending on the library does not drag the MCP SDK in with it.

```sh
go install github.com/liliang-cn/agentexec/cmd/agentexec-mcp@latest
agentexec-mcp -workspace /path/to/repo
```

It dispatches rather than blocks. `agent_start` returns a run id at once and the
delegate runs in the background under a bounded worker pool, so a twenty-minute
task cannot meet the client's tool timeout and several delegations can run at
the same time. Progress arrives as `resources/updated` on
`agentexec://runs/{id}`; a subscriber re-reads that resource for the state, the
usage, and everything the delegate has said so far. `agent_result` collects the
outcome, `agent_status` lists the runs, `agent_cancel` stops one.

Two things are decided for you, because getting them wrong is expensive:
`Request.NoMCP` is always on — a delegate that inherited your MCP config would
start this server again, and so would its delegate — and the workspace comes
from `-workspace`, never from the calling model, because the delegate runs with
permissions bypassed and can write.

## Testing

```sh
go test ./...
```

Provider argv is covered by golden tests — the argv a provider builds is the
contract this library sells, so it changes visibly or not at all.

## License

MIT
