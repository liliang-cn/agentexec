// Package agentexec provides an app-agnostic core for building, invoking, and
// parsing the Claude Code and Codex (and Gemini) CLIs. It contains command
// construction, stream-json / JSONL parsing, usage accounting, a line buffer,
// and plugin MCP config helpers — with no business logic, app config, or
// transport baked in. App-specific concerns are injected by the caller.
package agentexec

import "context"

// PermissionMode selects whether a provider bypasses its approval prompts.
type PermissionMode string

const (
	PermissionDefault PermissionMode = ""
	PermissionBypass  PermissionMode = "bypass"
)

// Request is the app-agnostic input to BuildCommand. App-specific policy text is
// passed via SystemPrompt; anything else via ExtraArgs/Env. No PolicyJSON/UserID/
// ProjectID/SaaS fields are baked in — those stay in the calling application.
type Request struct {
	RunID           string
	Mode            string // free-form, app-defined
	Prompt          string
	SystemPrompt    string // claude: --append-system-prompt; codex/gemini: prepended to prompt
	WorkspacePath   string
	Model           string // optional; provider maps to its model flag
	Env             map[string]string
	Plugins         []PluginRef    // claude --plugin-dir + .mcp.json merge
	ExtraMCPServers map[string]any // caller-injected MCP servers, merged before plugin servers
	// NoMCP runs with an empty MCP config instead of the user's own.
	//
	// An empty ExtraMCPServers map cannot express this: no servers means no
	// --mcp-config flag, which means the CLI loads everything the developer has
	// configured. That is right for an interactive session and wrong for using
	// the CLI as an inference backend — booting every server took longer than
	// the model spent thinking, and a call that can reach the operator's own
	// MCP servers is not reproducible in any sense.
	//
	// Ignored when ExtraMCPServers or Plugins supply servers: asking for both
	// none and some is a caller bug, and the explicit servers are the clearer
	// intent.
	NoMCP           bool
	PermissionMode  PermissionMode // PermissionDefault | PermissionBypass
	Sandbox         bool           // false (zero value) = headless: emit skip-sandbox/trust/git-check flags. true = run inside the CLI's own sandbox/approval flow.
	ResumeSessionID string         // claude --resume / codex resume <id>
	Continue        bool           // claude --continue
	ExtraArgs       []string       // escape hatch appended before the prompt
}

// CommandSpec is the fully resolved command to execute, produced by BuildCommand.
type CommandSpec struct {
	Argv    []string
	Env     []string
	WorkDir string
	Stdin   []byte
}

// Event is a single canonical, provider-normalized output event.
type Event struct {
	Type    string
	Payload map[string]any
}

// Canonical event types.
//
// EventAgentMessage carries more than the agent's prose. Provider lifecycle
// frames — Claude's `system` init and its hook events, the `result` summary —
// map here too, distinguished by Payload["role"]: "assistant" is what the model
// said, "system" and "result" are the CLI talking about the session. One
// "say OK" call produced eleven agent.message events, ten of them hook
// lifecycle, so a caller collecting the answer wants:
//
//	if e.Type == EventAgentMessage && e.Payload["role"] == "assistant" { ... }
//
// Filtering on Payload["text"] being non-empty happens to work today, because
// lifecycle frames carry "raw" instead — but that is a coincidence of the
// current mapping, not a contract.
const (
	EventAgentMessage   = "agent.message"
	EventToolCall       = "agent.tool_call"
	EventToolResult     = "agent.tool_result"
	EventTerminalOutput = "terminal.output"
	EventRateLimit      = "provider.rate_limit"
)

// Usage accumulates token usage and cost across a session.
type Usage struct {
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheTokens      int64
	EstimatedCostUSD float64
}

// Result is the terminal outcome of a session.
type Result struct {
	ExitCode int
	Summary  string
	Usage    Usage
	// Failed is the provider's own verdict on the turn, which is not the same
	// as the exit code and not always visible in it.
	//
	// Only Claude reports one: its result frame carries is_error. Codex uses
	// its `error` item for warnings as well as failures — a truncated skill
	// description arrives as one — so treating that as a verdict would mark
	// healthy turns as failed, and inventing a signal is worse than not having
	// it. Gemini has none either. For those two this stays false and the caller
	// is no worse off than before.
	//
	// A `claude` whose OAuth token has been revoked writes "Failed to
	// authenticate" as an assistant message, sets is_error on the result frame,
	// and exits zero. A caller reading only the message and the exit code takes
	// an authentication failure for the model's answer — and if that answer is
	// being written into a file, the failure is laundered into an artefact with
	// nothing anywhere saying the model never ran.
	Failed bool
}

// Capabilities describes which app-agnostic features a provider supports.
type Capabilities struct {
	Streaming         bool
	Resume            bool
	Plugins           bool
	MCP               bool
	SupportsPTY       bool
	RequiresWorkspace bool
}

// PluginRef references a Claude Code plugin directory by name and path.
type PluginRef struct {
	Name string
	Path string
}

// Provider is a CLI agent backend (claude, codex, gemini).
type Provider interface {
	Name() string
	Capabilities() Capabilities
	NewSession() Session
}

// Session is a single invocation lifecycle: build the command, parse streamed
// chunks into events, and finalize into a Result.
type Session interface {
	BuildCommand(ctx context.Context, req Request) (CommandSpec, error)
	ParseChunk(chunk []byte) ([]Event, error)
	Finalize(ctx context.Context, fullOutput []byte, exitCode int) (Result, []Event, error)
	SessionID() string
}
