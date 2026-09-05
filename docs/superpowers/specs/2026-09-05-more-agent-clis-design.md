# More agent CLIs: qwen, kimi, opencode, copilot, goose, pi, agy, hermes, aider

Date: 2026-09-05

## Why

`Discover` only knew four CLIs. A machine with `kimi`, `opencode`, `copilot`,
`goose`, `pi`, `agy` and `hermes` on PATH listed none of them, because the
library had no parser for what they print. Consumers (agent-go's
`cli_agent_run`, agentexec-mcp) could not offer them at all.

## What each CLI actually does headless

Every row below was established by running the binary in an empty, non-git
scratch directory with stdin on `/dev/null`, or by reading its source when a
live turn was not possible. "Verified" means a real turn produced the frames
the parser is written against.

| CLI | version | headless argv | stream | session id | bypass | verified |
|---|---|---|---|---|---|---|
| qwen | 0.23.0 | `qwen --output-format stream-json [--yolo] <prompt>` | Claude dialect frame for frame (`system/init`, `assistant`, `user`, `result` + `is_error`) | `session_id` | `--yolo` | live |
| kimi | 1.3 | `kimi --print --output-format stream-json -p <prompt>` | one kosong `Message` per line: `{role, content, tool_calls?, tool_call_id?}`; `content` is a string when it is a single text part | none printed | implicit in `--print` | live |
| opencode | 1.18.21 | `opencode run --format json [--auto] <prompt>` | `{type: text\|tool_use\|step_start\|step_finish\|reasoning\|error, sessionID, part}` | `sessionID` on every frame | `--auto` | live |
| copilot | 1.0.34 | `copilot -p <prompt> --output-format json --allow-all-tools` | `{type: "assistant.message"\|"tool.execution_start"\|..., data}` then a final `{type:"result", sessionId, exitCode, usage}` | `session.start` / `result` | `--yolo` | live |
| goose | 1.44.0 | `goose run -t <prompt> --output-format stream-json` | text banner, then `{type:"message", message:{role, content:[text\|toolRequest\|toolResponse]}}`, `{type:"complete", *_tokens}` | banner line only | `GOOSE_MODE=auto` | live |
| pi | 0.85.0 | `pi -p --mode json <prompt>` | `session`, `message_start/update/end`, `tool_execution_start/end`, `turn_end`, `agent_end` | `session.id` | none needed | live |
| agy | 1.1.26 | `agy --output-format stream-json [--dangerously-skip-permissions] -p <prompt>` | `{event: init\|step_update\|result, <event>: {...}}` | `conversation_id` | `--dangerously-skip-permissions` | `result` frame only (no login on this machine) |
| hermes | 0.20.1 | `hermes -z <prompt> --usage-file <tmp>` | plain text; usage/session/failed in the JSON usage file | usage file | implicit in `-z` | argv + usage schema from source |
| aider | 0.86 | `aider --message <prompt> --yes-always --no-pretty --no-stream ...` | plain text | none | `--yes-always` | flags from `--help` only |

Two traps found on the way, recorded so nobody pays for them twice:

- **agy's `--print` eats the next argument as its prompt.** `--print
  --output-format stream-json "task"` runs with the prompt `--output-format`.
  The prompt flag goes last, immediately followed by the prompt.
- **kimi prints its own errors as plain text on stdout with exit 0 in
  `--print` mode** (`LLM not set`). They arrive as `terminal.output` lines;
  there is no verdict to read.

## Design

Two mechanisms, chosen per CLI by what it prints:

1. **A dialect provider** for each CLI that streams JSON. Same shape as
   `codex.go`: a `Provider`, a `Session` with `BuildCommand` / `ParseChunk` /
   `Finalize` / `SessionID`, and a private `map<Name>Event` that turns one frame
   into canonical events. qwen borrows the claude session the way cursor does,
   since its stream is the Claude dialect.
2. **A text provider** (`NewText`) for CLIs that print prose. Every line is a
   `terminal.output` event; `Finalize` emits one `agent.message` with role
   `assistant` carrying the whole output so consumers keep their one filter.
   `WithArgv` gives it an argv template with a `{prompt}` token, `WithModelFlag`
   the flag to put the model behind. `NewAider` is a text provider with aider's
   flags filled in. hermes is a text provider plus a usage file: `BuildCommand`
   creates a temp file, `Finalize` reads it for tokens, session id and the
   `failed` verdict, then removes it.

Verdicts (`Result.Failed`) are only read where the CLI states one: qwen
`is_error`, copilot `result.exitCode`, opencode `error` frames, pi
`stopReason == "error"`, agy `result.status == "ERROR"` / `result.error`,
hermes `failed` in the usage file. kimi, goose and aider stay `false`.

`Discover` gains the nine names in `builtins`, with substring keys so an alias
such as `qwen-work` still lands on the qwen dialect. `RegistryFrom` builds each.

## Not done

- agy's `init` and `step_update` frames are mapped from its binary's struct
  tags (`step_type`, `tool_name`, `text_delta`), not from a recorded run. The
  `result` frame is recorded.
- aider's argv is from `--help` on 0.86; no turn was run.
