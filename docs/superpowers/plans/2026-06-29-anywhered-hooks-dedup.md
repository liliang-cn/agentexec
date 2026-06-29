# anywhered Hooks Dedup Implementation Plan (Phase 2, Plan C)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Replace `anywhered`'s inline hook-payload parsing and transcript reading in `cmd/anywhered/hooks.go` with `github.com/liliang-cn/agentcli/hooks.{ParsePayload, LastAssistantText}`, with no behavior change. The daemon transport (inbox POST, install commands) and the app-specific `hookSummary` text stay app-side.

**Scope decision:** `agentcli/hooks.Summarize` uses different, generic summary strings than anywhered's inbox UX (`turn complete`, `you: …`, `needs your attention`). Summary text is app-specific presentation, so `hookSummary` stays in anywhered (rewritten to consume `hooks.HookEvent`). Only the pure parsing/transcript helpers are shared. `installHooks`/`installCodexHooks`/`runHook` POST transport are unchanged.

**Working dir:** `/Users/liliang/Things/AI/projects/anywhere/anywhered` (already depends on `agentcli` via local replace). Branch: `phase2-hooks-dedup`.

---

## Task 1: Swap hooks.go onto agentcli/hooks

**Files:** `cmd/anywhered/hooks.go`

- [ ] **Step 1: Rewrite `runHook` payload handling**

Replace the body that does `io.ReadAll(io.LimitReader(os.Stdin, 1<<20))` + `json.Unmarshal(raw, &p)` + the `str := func(k string)...` closure + the `inbox.Event{...}` construction + the Stop transcript block, with:

```go
	hev, _ := hooks.ParsePayload(os.Stdin)
	if eventName == "" {
		eventName = hev.EventName
	}

	ev := inbox.Event{
		Agent:     agent,
		Type:      eventName,
		SessionID: hev.SessionID,
		Cwd:       hev.Cwd,
		Tool:      hev.ToolName,
		Summary:   hookSummary(eventName, hev),
		TmuxPane:  os.Getenv("TMUX_PANE"),
	}
	// Hooks carry no assistant text; on turn end, read the transcript so the
	// app can show what the model actually replied.
	if eventName == "Stop" || eventName == "SubagentStop" {
		txt := hev.LastAssistant // codex provides this inline
		if txt == "" {
			txt, _ = hooks.LastAssistantText(hev.TranscriptPath) // claude: read transcript
		}
		if txt != "" {
			ev.Text = txt
			ev.Summary = truncate(txt, 100)
		}
	}
```

(Keep the surrounding `runHook` parts unchanged: the `ANYWHERE_APP_RUN` early return, `agent` default, the `json.Marshal(ev)` + `http.Client` POST to `hookURL()`.)

- [ ] **Step 2: Rewrite `hookSummary` to take a `hooks.HookEvent`**

Replace `func hookSummary(event string, p map[string]any) string { ... }` (and delete the `bashCommand` helper) with:

```go
func hookSummary(event string, ev hooks.HookEvent) string {
	switch event {
	case "Notification":
		if m, _ := ev.Raw["message"].(string); m != "" {
			return m
		}
		return "needs your attention"
	case "PreToolUse":
		if ev.Command != "" {
			return "→ " + ev.ToolName + ": " + truncate(ev.Command, 80)
		}
		return "→ " + ev.ToolName
	case "PostToolUse":
		return "✓ " + ev.ToolName
	case "Stop", "SubagentStop":
		return "turn complete"
	case "UserPromptSubmit":
		if pr, _ := ev.Raw["prompt"].(string); pr != "" {
			return "you: " + truncate(pr, 80)
		}
		return "prompt submitted"
	default:
		return event
	}
}
```

These strings are byte-identical to the current `hookSummary`. `ev.Command` is the parsed `tool_input.command`; truncating to 80 matches the old `bashCommand` behavior.

- [ ] **Step 3: Delete the now-dead local helpers**

Delete `func lastAssistantText(path string) string { ... }` (replaced by `hooks.LastAssistantText`) and `func bashCommand(p map[string]any) string { ... }` (replaced by `ev.Command`). Keep `truncate`, `hookURL`, `installHooks`, `installCodexHooks`, `stripAnywhereHooks`.

- [ ] **Step 4: Fix imports**

Add `"github.com/liliang-cn/agentcli/hooks"`. Remove `"io"` if it is now unused (it was only used by the removed `io.ReadAll`/`io.LimitReader`). Keep `"encoding/json"` (still used for `json.Marshal(ev)`), `"bytes"`, `"net/http"`, `"os"`, `"strings"`, `"time"`, `"fmt"`, etc. — let `go build` / `gofmt` tell you what's unused.

- [ ] **Step 5: Build + verify + commit**

```bash
go build ./... 2>&1 | tail
go test ./... 2>&1 | grep -E 'FAIL|^ok ' | tail
go vet ./... ; gofmt -l .
git add cmd/anywhered/hooks.go
git commit -m "anywhered: dedup hook parsing + transcript onto agentcli/hooks"
```
Expected: build clean, tests pass, vet/fmt clean. Commit ONLY `cmd/anywhered/hooks.go` (the workspace has untracked scratch files `.aas-mcp.json`/`answer.md` that must not be committed).

---

## Verification

- `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt -l .` green/clean.
- Behavior parity (by inspection): inbox `Summary` strings unchanged; Stop/SubagentStop still prefers `last_assistant_message` then falls back to the transcript's last assistant text; bad/empty stdin still produces a best-effort POST (errors ignored, agent never blocked).
- Build the daemon binary and confirm `anywhered hook PreToolUse` with a piped JSON payload still posts (best-effort) — or, if no daemon is listening, that it exits 0 without blocking.

## Risk

- Behavior parity rests on the `hookSummary` strings being copied verbatim and `truncate` semantics matching — covered by Step 2. `ParsePayload`'s 8 MiB read limit (vs the old 1 MiB) is a superset; bad JSON yields a zero `HookEvent` (error ignored) preserving the "never block the agent" contract.
