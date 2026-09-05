# agentexec-mcp — Delegating a task to another agent CLI over MCP

Date: 2026-09-05
Status: Approved (brainstorming)
Module: `github.com/liliang-cn/agentexec/cmd/agentexec-mcp` (own `go.mod`, nested under the library repo)

## Purpose

Let an agent hand a whole task to a different agent CLI — Claude Code delegating
to `codex` for a second opinion, or fanning several delegations out at once. The
library already knows how to build the argv, run it under a PTY, and normalize
what comes back; this is the MCP shell around that.

## Why not the obvious shape

A single blocking `agent_run` was the first proposal and was rejected. A
delegated coding task runs for minutes to tens of minutes: blocking holds a tool
call open the whole time, serializes delegations that ought to run in parallel,
and eventually meets the client's tool timeout, which truncates work that was
going fine.

So: **dispatch and report**. `agent_start` returns as soon as the run is
queued, and progress is pushed as it happens.

## What "pushed" can and cannot mean

MCP has no way to interrupt the calling model mid-turn. Both mechanisms below
reach the *client* — its UI, its logs, and any subscriber — but Claude Code does
not wake the model on a notification. The delegating model still collects the
outcome with one `agent_result` call.

This is a property of the protocol, not a shortcoming of the design, and it is
written down here so nobody re-litigates it later. What the shape does buy is
real: no tool call held open, delegations running in parallel, no timeout
truncating a long run, and a human watching the sub-agent work in real time.

Two channels, because they answer different questions:

- **`notifications/message`** (logging) — every assistant message and tool call
  as it happens. For a human and the client's log.
- **`agentexec://runs/{id}` as a resource, plus `resources/updated`** — for a
  client that wants the current state rather than a stream of lines. The
  protocol-correct way to follow evolving state.

## Tool surface

| Tool | Input | Returns |
|---|---|---|
| `agent_start` | `provider`, `prompt`, optional `model`, `system_prompt`, `resume_session_id` | `{run_id, state}` immediately |
| `agent_result` | `run_id` | `{answer, usage, session_id, failed, exit_code}`, or the current state if it is not finished |
| `agent_status` | optional `run_id` | one snapshot, or all runs when omitted |
| `agent_cancel` | `run_id` | the state it moved to |
| `list_providers` | — | registered names and their capabilities |

`agent_result` on an unfinished run reports the state rather than erroring or
blocking: a caller that guessed wrong should learn that cheaply.

## Architecture

Three units under `cmd/agentexec-mcp/internal/`, each testable without a live
CLI and without a live MCP client:

| Unit | Does | Depends on |
|---|---|---|
| `runstore` | The run state machine and its snapshots. The only place that locks. | nothing |
| `runner` | A bounded worker pool over a queue; builds the request, runs it under a PTY, feeds events to the store and to a sink. | `runstore`, `agentexec`, `agentexec/pty` |
| `mcpserver` | Binds tools and resources to the runner. No policy of its own. | `runner` |

`runner` takes its notification sink as an interface, so its tests assert on
what would have been pushed without an MCP session in the picture. `mcpserver`
supplies the implementation that turns those into `notifications/message` and
`ResourceUpdated`.

States: `queued → running → {done, failed, cancelled}`. `failed` covers both a
non-zero exit and `Result.Failed`, which the library reports separately for a
reason — a `claude` with a revoked token exits zero.

## Safety

- **`Request.NoMCP` is always true.** A delegated CLI that inherits the
  operator's MCP config would start this very server again. Not configurable.
- **The workspace is fixed by a server flag**, not chosen by the calling model.
  The delegate runs with `PermissionBypass` and outside the CLI's own sandbox —
  it can write. Where it writes is an operator decision.
- **Bounded concurrency** (`--max-concurrent`, default 2). Delegation is easy to
  ask for and expensive to serve.
- **Shutdown cancels every live run.** No orphaned agent processes.

## Configuration

```
agentexec-mcp --workspace <dir> [--max-concurrent N] [--provider name=binary ...]
```

`--workspace` is required: there is no sensible default for where someone else's
agent may write.

## Testing

- `runstore`: table-driven state transitions; concurrent access under `-race`.
- `runner`: a fake provider registered into the registry, so a run can be driven
  end to end with no `claude`/`codex` on the box; assert the events reaching the
  sink and the final snapshot. Cancellation asserted by state, not by timing.
- `mcpserver`: an in-memory client/server transport pair from the SDK; call each
  tool, assert the JSON that comes back and the notifications that were sent.
- No test may require a real agent CLI, a network, or an MCP client process.

## Non-goals

- Exposing the `hooks` package. Different job, no demand yet.
- Streaming the delegate's output as it arrives *into the calling model*. The
  protocol cannot do it; see above.
- Persisting runs across a restart. A run belongs to the session that asked
  for it.
- HTTP transport. stdio is what a delegating agent uses.

## Dependencies

`github.com/modelcontextprotocol/go-sdk v1.7.0` and the library itself. Both
live in the nested module so the root keeps its single dependency on
`creack/pty` — a library that costs nothing to import is worth preserving.
