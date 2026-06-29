# agent-as-a-service Migration Implementation Plan (Phase 3, Plan D)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Migrate `github.com/liliang-cn/agent-as-a-service` (repo root `/Users/liliang/Things/AI/projects/Agent`) off its forked `internal/provider` + `internal/runtime/pty.go` onto `github.com/liliang-cn/agentcli` (`cliagent` + `cliagent/pty`), functionally unchanged. The SaaS system prompt moves to a worker-set `Request.SystemPrompt`.

**Architecture:** Same shape as the completed `anywhered` migration. `agentcli` is unchanged. Agent keeps a thin app-local `internal/provider` (echo, opencode, a `LoadFromConfig` adapter, and a `saasprompt` helper). `internal/runtime` keeps `server.go`; only `pty.go` is deleted. The `internal/sandbox` `Strategy.Wrap` interface switches `provider.CommandSpec` → `cliagent.CommandSpec`.

**MONOREPO CONSTRAINT (critical):** `/Users/liliang/Things/AI/projects/Agent` is one git repo containing the Go backend AND an Expo React Native app (`apps/mobile/`), a `web/` frontend, screenshots, `cortexdb.db*`, etc. **Every commit must stage explicit Go paths only** — NEVER `git add -A`/`git add .`; never touch `apps/mobile/`, `web/`, images, or `cortexdb.db*`. After every `git add`, run `git status --short` and confirm only intended Go files are staged.

**Working dir:** `/Users/liliang/Things/AI/projects/Agent`. Branch: `phase3-agentcli-migration`. `agentcli` is at `../agentcli` (Agent's root is one level above the agentcli checkout).

## agentcli API recap
`cliagent.{Provider,Session,Request,CommandSpec,Event,Result,Usage,Capabilities,PluginRef,Registry,NewRegistry,ErrUnsupportedMode,EventTerminalOutput,PermissionBypass}`; `Request` has `{RunID,Mode,Prompt,SystemPrompt,WorkspacePath,Model,Env,Plugins,ExtraMCPServers,ResumeSessionID,Continue,PermissionMode,Sandbox,ExtraArgs}`; constructors `NewClaude/NewCodex/NewGemini` + `WithName/WithBinary/WithBaseEnv/WithModelEnv/WithMCPConfig/WithAllowedModes`; `&cliagent.LineBuffer{}`; `pty.Run(ctx, pty.Command{Argv,Env,WorkDir}, onChunk) (pty.Result{ExitCode,Output}, error)`. claude emits `--append-system-prompt <SystemPrompt>` when set; codex prepends `SystemPrompt + "\n\n" + Prompt`.

---

## File Structure (after migration)

| Path | Action |
|---|---|
| `internal/provider/echo.go` | Rewrite against cliagent |
| `internal/provider/opencode.go` | Rewrite against cliagent |
| `internal/provider/registry.go` | Create (`LoadFromConfig` → `*cliagent.Registry`) |
| `internal/provider/saasprompt.go` | Create (moved SaaS prompt helpers) |
| `internal/provider/{claude,codex,gemini,plugin,linebuf,provider}.go` + their `_test.go` | Delete |
| `internal/runtime/pty.go` | Delete (keep `server.go`, `server_test.go`) |
| `internal/sandbox/{sandbox,host,docker,docker_test}.go` | Edit: `provider.CommandSpec`→`cliagent.CommandSpec` |
| `internal/worker/worker.go` | Edit: cliagent types, Request fields, AAS bridge, SystemPrompt, `pty.Run` |
| `go.mod`/`go.sum` | require + local replace; tidy |

---

## Task 1: Add agentcli dependency

**Files:** `go.mod`

- [ ] **Step 1: require + local replace (no tidy yet)**
```bash
go mod edit -require=github.com/liliang-cn/agentcli@v0.0.0
go mod edit -replace=github.com/liliang-cn/agentcli=../agentcli
go build ./... 2>&1 | tail   # still green; agentcli unused so far
```
- [ ] **Step 2: Commit (go.mod only)**
```bash
git add go.mod
git status --short   # ONLY go.mod
git commit -m "agent: add agentcli dependency via local replace"
```

---

## Task 2: Cut over to agentcli

One coherent change (provider + runtime + sandbox + worker move together). Make every edit, then build/test/tidy green, then commit (explicit Go paths only).

### Step 1 — Rewrite `internal/provider/echo.go`
```go
package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agentcli/cliagent"
)

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

### Step 2 — Rewrite `internal/provider/opencode.go`
```go
package provider

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/liliang-cn/agentcli/cliagent"
	"github.com/liliang-cn/agent-as-a-service/internal/config"
)

var opencodeModes = []string{"headless-code", "terminal-task"}

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

### Step 3 — Create `internal/provider/registry.go`
```go
package provider

import (
	"github.com/liliang-cn/agentcli/cliagent"
	"github.com/liliang-cn/agent-as-a-service/internal/config"
)

var (
	claudeModes = []string{"headless-code", "terminal-task", "browser-task", "desktop-task", "computer-task"}
	agentModes  = []string{"headless-code", "terminal-task"}
)

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

### Step 4 — Create `internal/provider/saasprompt.go`

Copy `saasSystemPrompt` and `codexSaasPrompt` from the current `claude.go`/`codex.go` VERBATIM, with ONE change: in `codexSaasPrompt`, the final `b.WriteString("--- END SYSTEM ---\n\n")` becomes `b.WriteString("--- END SYSTEM ---")` (drop the trailing `\n\n` — agentcli's codex prepend adds the `\n\n` separator, reproducing the original bytes). Add `saasPromptFor`:

```go
package provider

import "strings"

// saasSystemPrompt — claude SaaS guidance. withTools includes artifact.save /
// human.await / checkpoint guidance (true iff the per-run tool server is active).
func saasSystemPrompt(withTools bool) string {
	var b strings.Builder
	b.WriteString("You are running inside Agent-as-a-Service (AaaS), a SaaS platform. ")
	b.WriteString("A remote user submitted this request through a web UI. They cannot see your terminal, your chat, or files left in the workspace — they only see run events and artifacts you explicitly publish. ")
	b.WriteString("Work autonomously to completion; do not ask the user follow-up questions in chat (there is no chat). Respond in the language of the request.\n\n")
	if withTools {
		b.WriteString("DELIVERABLES: For every result the user should receive (a document, spreadsheet, image, video, archive), write the file into your workspace and then call the `artifact.save` tool with its workspace-relative `path`. That is the ONLY way the user can download what you produced. Save the finished output, not intermediate scratch files.\n\n")
		b.WriteString("ALWAYS WRITE answer.md: Whenever your result is (or includes) a textual answer — research findings, analysis, recommendations, an explanation, a Q&A response — you MUST also write that complete answer as Markdown to `answer.md` in your workspace and call `artifact.save` with path `answer.md`. Do this even when you have already explained the answer in your messages, and even when you also produced other files. This is mandatory for any question-answering or research task so the user can download the result. Write it as the final step, in the language of the request.\n\n")
		b.WriteString("CHECKPOINTS: When you genuinely need the operator to approve a direction or choose between options before continuing, call the `human.await` tool with a short stage id and a markdown summary, and wait for their answer. Use it sparingly, only at meaningful decision points.\n\n")
	}
	b.WriteString("SKILLS: First check whether a skill you already have fits the request (use the Skill tool). ")
	b.WriteString("Pre-installed skills are listed in your system/init event under `skills`; prefer them when relevant.\n\n")
	b.WriteString("SKILL DISCOVERY (MANDATORY for these triggers — not optional):\n")
	b.WriteString("- If the task touches Office files (.docx, .xlsx, .pptx, .pdf) for read/write/convert, run `npx -y skills find office` BEFORE you write any code. Pick one with high install count and `npx -y skills add` it.\n")
	b.WriteString("- If the task involves slides / presentations / decks, run `npx -y skills find slides` first.\n")
	b.WriteString("- If the task involves video / animation / motion / short-form reel, run `npx -y skills find video` first.\n")
	b.WriteString("- If the task involves charts, diagrams or data viz, run `npx -y skills find chart` or `diagram` first.\n")
	b.WriteString("- If the task names a specific framework (Remotion, Manim, Three.js, Excalidraw, Mermaid, …), search for it by name.\n")
	b.WriteString("- For trivial tasks (single-file edits, plain text replies, simple shell commands, fact look-ups), do NOT search — skip straight to doing.\n\n")
	b.WriteString("DISCOVERY USAGE: Prefer skills with high install counts from reputable sources (anthropics, vercel-labs, openai). `npx -y skills find <kw>` is a cheap network query (no LLM cost) — calling it when a trigger fires is far preferable to writing a brittle ad-hoc script. After picking one, install with `npx -y skills add <owner/repo@skill>` and use it via the Skill tool.")
	return b.String()
}

// codexSaasPrompt — codex SaaS guidance. Trailing "\n\n" intentionally omitted:
// agentcli prepends SystemPrompt + "\n\n" + Prompt, which restores the original
// "--- END SYSTEM ---\n\n" + prompt bytes.
func codexSaasPrompt() string {
	var b strings.Builder
	b.WriteString("--- SYSTEM (read before doing the task; do not echo back to the user) ---\n")
	b.WriteString("You are running inside Agent-as-a-Service (AaaS), a self-hosted run platform. ")
	b.WriteString("A remote user submitted this request via a web UI; they cannot see your terminal or chat — only the run events and files in /workspace. ")
	b.WriteString("Work autonomously to completion; do not ask follow-up questions. Respond in the language of the request.\n\n")
	b.WriteString("DELIVERABLES: write every result file (.docx, .xlsx, .pptx, .pdf, images, video, archives, code) into /workspace. The platform collects every file under /workspace and serves them as downloadable artifacts after the run terminates — no explicit save call needed.\n\n")
	b.WriteString("ALWAYS WRITE answer.md: whenever your result is (or includes) text — research findings, analysis, recommendations, an explanation, a Q&A response — also write the complete answer as Markdown to /workspace/answer.md as the final step, in the language of the request. This is mandatory for any question-answering task.\n\n")
	b.WriteString("SKILL DISCOVERY (MANDATORY for these triggers — not optional):\n")
	b.WriteString("- If the task touches Office files (.docx, .xlsx, .pptx, .pdf) for read/write/convert, run `npx -y skills find office` BEFORE you write any code, then `npx -y skills add <owner/repo@skill>` if a high-install-count match exists. Use the installed SKILL.md as your method guide.\n")
	b.WriteString("- If the task involves slides / presentations / decks, run `npx -y skills find slides` first.\n")
	b.WriteString("- If the task involves video / animation / motion / short-form reel, run `npx -y skills find video` first.\n")
	b.WriteString("- If the task involves charts, diagrams or data viz, run `npx -y skills find chart` or `diagram` first.\n")
	b.WriteString("- If the task names a specific framework (Remotion, Manim, Three.js, Excalidraw, Mermaid, …), search by name.\n")
	b.WriteString("- For trivial tasks (single-file edits, plain text replies, simple shell commands, fact look-ups), do NOT search — skip straight to doing.\n\n")
	b.WriteString("DISCOVERY USAGE: prefer skills with high install counts from reputable sources (anthropics, vercel-labs, openai). `npx -y skills find <kw>` is a cheap network query (no LLM cost). After picking one, install with `npx -y skills add <owner/repo@skill>` and follow its SKILL.md.\n")
	b.WriteString("--- END SYSTEM ---")
	return b.String()
}

// SaaSPromptFor returns the SaaS system prompt for a provider (empty for
// providers that take none). The worker sets it as Request.SystemPrompt.
// Exported: the worker package calls it as provider.SaaSPromptFor.
func SaaSPromptFor(providerName string, withTools bool) string {
	switch providerName {
	case "claude-code":
		return saasSystemPrompt(withTools)
	case "codex":
		return codexSaasPrompt()
	default:
		return ""
	}
}
```

IMPORTANT: copy the prompt strings from the actual current `claude.go`/`codex.go` to guarantee they are verbatim; the text above is transcribed but you must confirm it matches the source exactly (diff the string literals).

### Step 5 — Delete migrated files (NOT server.go)
```bash
git rm internal/provider/claude.go internal/provider/claude_test.go \
       internal/provider/codex.go internal/provider/codex_test.go \
       internal/provider/gemini.go internal/provider/gemini_test.go \
       internal/provider/plugin.go internal/provider/plugin_test.go \
       internal/provider/linebuf.go internal/provider/linebuf_test.go \
       internal/provider/provider.go \
       internal/runtime/pty.go
```
(Keep `internal/runtime/server.go` and `internal/runtime/server_test.go`.)

### Step 6 — `internal/sandbox`: swap `provider.CommandSpec` → `cliagent.CommandSpec`

In `internal/sandbox/sandbox.go`, `internal/sandbox/host.go`, `internal/sandbox/docker.go`, and `internal/sandbox/docker_test.go`: replace the import `"github.com/liliang-cn/agent-as-a-service/internal/provider"` with `"github.com/liliang-cn/agentcli/cliagent"`, and replace every `provider.CommandSpec` with `cliagent.CommandSpec`. These files use ONLY `provider.CommandSpec` (the `Strategy.Wrap(spec provider.CommandSpec, ...) (provider.CommandSpec, error)` interface at sandbox.go:46, its impls at host.go:15 and docker.go:91/94/153/194, and the test literals at docker_test.go:17/82/96/198). Do not change any other logic (docker.go's mcp-config rewrite stays).

### Step 7 — `internal/worker/worker.go`

(a) Imports: keep `"github.com/liliang-cn/agent-as-a-service/internal/provider"` (still used for `LoadFromConfig`, `SaaSPromptFor`, and as the package that holds the app-local providers). REMOVE `"github.com/liliang-cn/agent-as-a-service/internal/runtime"`. ADD `"github.com/liliang-cn/agentcli/cliagent"` and `"github.com/liliang-cn/agentcli/cliagent/pty"`. Ensure `"os"` is imported (needed for `os.Executable()`).

(b) Type references:
- `reg *provider.Registry` (worker struct, ~line 39) → `reg *cliagent.Registry`.
- `provider.LoadFromConfig(cfg.Providers)` (~line 66) — UNCHANGED (app-local adapter now returns `*cliagent.Registry`).
- `Providers() *provider.Registry` (~line 142) → `*cliagent.Registry`.
- `resolvePlugins(...) ([]provider.PluginRef, error)` (~line 821) → `([]cliagent.PluginRef, error)`; the slice element type (~line 836) and the literal `provider.PluginRef{Name: name, Path: ...}` (~line 845) → `cliagent.PluginRef`.

(c) Request construction (lines ~393-406). Replace:
```go
	req := provider.Request{
		RunID:           run.ID,
		UserID:          run.UserID,
		ProjectID:       run.ProjectID,
		Mode:            run.Mode,
		Prompt:          run.Prompt,
		WorkspacePath:   layout.WorkspaceDir,
		PolicyJSON:      run.PolicyJSON,
		Env:             map[string]string{},
		Plugins:         pluginRefs,
		ResumeSessionID: resumeSessionID,
	}
	if tools != nil {
		req.Env["AAS_TOOL_SOCKET"] = tools.SocketPath()
	}
```
with:
```go
	req := cliagent.Request{
		RunID:           run.ID,
		Mode:            run.Mode,
		Prompt:          run.Prompt,
		WorkspacePath:   layout.WorkspaceDir,
		Env:             map[string]string{},
		Plugins:         pluginRefs,
		ResumeSessionID: resumeSessionID,
		PermissionMode:  cliagent.PermissionBypass,
		Sandbox:         false,
	}
	toolsEnabled := tools != nil
	if toolsEnabled {
		sock := tools.SocketPath()
		req.Env["AAS_TOOL_SOCKET"] = sock
		// agentcli no longer sniffs AAS_TOOL_SOCKET; inject the human.await MCP
		// bridge explicitly (byte-identical to the old claude.go).
		if exe, err := os.Executable(); err == nil {
			req.ExtraMCPServers = map[string]any{
				"aas": map[string]any{
					"command": exe,
					"args":    []string{"mcp-bridge", "--socket", sock},
				},
			}
		}
	}
	req.SystemPrompt = provider.SaaSPromptFor(run.Provider, toolsEnabled)
```
NOTE: `UserID`/`ProjectID`/`PolicyJSON` are dropped from the request. The worker must keep reading `run.UserID`/`run.PolicyJSON` directly wherever it already does (cost cap, timeout, `store.InsertProviderUsage` uses `run.UserID`). Grep `req.UserID`, `req.ProjectID`, `req.PolicyJSON` — there must be NO reads of those (they were write-only into the provider, which ignored them). If any read exists, repoint it to `run.*`.

(d) PTY call (line ~492). Replace:
```go
	res, runErr := runtime.Run(runCtx, spec.Argv, spec.Env, spec.WorkDir, onChunk)
```
with:
```go
	res, runErr := pty.Run(runCtx, pty.Command{Argv: spec.Argv, Env: spec.Env, WorkDir: spec.WorkDir}, onChunk)
```
The `onChunk` closure, `recordRateLimit`, `sess.Finalize`, and `store.InsertProviderUsage(... finalRes.Usage ...)` are UNCHANGED.

### Step 8 — Build, tidy, test
```bash
go build ./... 2>&1 | tail -20
go mod tidy 2>&1 | tail
go build ./... 2>&1 | tail -20
go test ./... 2>&1 | tail -30
gofmt -l . | grep -vE '^(apps/mobile|web)/' ; go vet ./... 2>&1 | tail
```
Fix any leftover `provider.<deleted-symbol>` / `runtime.Run` references by pointing them at `cliagent.*` / `pty.*`. `provider.LoadFromConfig`, `provider.saasPromptFor`, `provider.NewEcho`, `provider.NewOpenCode` stay (app-local). All Go packages must build & test green; `gofmt -l` must show no GO files (ignore any pre-existing non-Go listings); vet clean.

### Step 9 — Commit (EXPLICIT Go paths only)
```bash
git add internal/provider internal/runtime internal/sandbox internal/worker go.mod go.sum
git status --short   # MUST show only those Go paths — NO apps/mobile, web, images, cortexdb.db
git commit -m "agent: migrate provider+runtime onto agentcli; SaaS prompt via Request.SystemPrompt"
```
If `git status` shows anything under `apps/mobile/`, `web/`, or any image/`cortexdb.db*` staged, unstage it (`git restore --staged <p>`) before committing. NEVER `git add -A`.

---

## Task 3: Verify parity

- [ ] `go build ./...`, `go test ./...`, `go test -race ./...`, `go vet ./...` green; `gofmt -l .` shows no Go files.
- [ ] Grep confirms no `internal/runtime` import remains in worker.go and no `provider.{Request,Registry,Session,Provider,Event,Result,Capabilities,CommandSpec}` references remain outside the app-local provider package: `grep -rn 'provider\.\(Request\|Registry\|Session\|Provider\|Event\|Result\|Capabilities\|CommandSpec\)' internal/ ` should only (if anything) hit the app-local provider package's own files (which now use `cliagent` types, so likely zero).
- [ ] SaaS-prompt byte parity: confirm `saasprompt.go`'s `saasSystemPrompt`/`codexSaasPrompt` string literals are character-identical to the deleted sources except the codex trailing-`\n\n` trim. The claude prompt is emitted via `--append-system-prompt`; codex via `SystemPrompt + "\n\n" + Prompt` ⇒ original bytes.
- [ ] DB persistence: `store.InsertProviderUsage` still receives `finalRes.Usage` (Model/InputTokens/OutputTokens/CacheTokens/EstimatedCostUSD).
- [ ] Build the binary (`go build -o /tmp/aas ./cmd/...` for the worker/server entrypoint) to confirm `main` compiles; confirm the `mcp-bridge` subcommand still exists (the AAS bridge invokes it).
- [ ] Report build/test/-race/vet/fmt, the commit stat (proving no RN/web/binary files committed), and any residual references.

---

## Self-Review (plan author)
**Spec coverage:** B.1 echo/opencode/registry/saasprompt → Task 2 Steps 1-4; B.2 delete runtime/pty.go (keep server.go) → Step 5; B.3 saasprompt + codex trim → Step 4; B.4 worker Request/SystemPrompt/AAS bridge/pty.Run → Step 7; B.5 plugins via Request.Plugins → Step 7(b); B.6 go.mod → Task 1. Plus discovered scope: sandbox `CommandSpec` swap → Step 6.
**Type consistency:** `mergeEnv`/`linesToTerminalOutput`/`shellQuote` live once in the app-local package (opencode.go defines mergeEnv; echo.go defines linesToTerminalOutput + shellQuote) — shared within package, no dup. `claudeModes`/`agentModes`/`opencodeModes` distinct names. `LoadFromConfig` returns `*cliagent.Registry` consumed by worker. `saasPromptFor` exported within package (lowercase, same package as worker? NO — worker is a DIFFERENT package). **FIX:** `saasPromptFor` must be EXPORTED (`SaaSPromptFor`) since worker calls `provider.saasPromptFor` across packages. Use `provider.SaaSPromptFor` in worker and name the func `SaaSPromptFor` in saasprompt.go.
**Placeholder scan:** none.

## Risks
- **Monorepo commit scoping** — biggest operational risk; explicit-path `git add` + `git status` check each commit.
- **Cross-package export:** `SaaSPromptFor` must be exported (worker is package `worker`, helper is package `provider`).
- **SaaS prompt byte parity** — verify string literals against source (Step 4 + Task 3).
- **Local replace** — Agent builds only with `agentcli` at `../agentcli`.
