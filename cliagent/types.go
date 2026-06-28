// Package cliagent provides an app-agnostic core for building, invoking, and
// parsing the Claude Code and Codex (and Gemini) CLIs. It contains command
// construction, stream-json / JSONL parsing, usage accounting, a line buffer,
// and plugin MCP config helpers — with no business logic, app config, or
// transport baked in. App-specific concerns are injected by the caller.
package cliagent

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
	PermissionMode  PermissionMode // PermissionDefault | PermissionBypass
	Sandbox         bool           // false (zero value) = headless: emit skip-sandbox/trust/git-check flags. true = run inside the CLI's own sandbox/approval flow.
	MCPConfigPath   string         // optional precomputed --mcp-config path
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
