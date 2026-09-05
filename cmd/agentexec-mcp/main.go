// Command agentexec-mcp lets one agent hand a whole task to another agent CLI
// over MCP. It dispatches and reports: agent_start returns a run id at once,
// progress arrives as notifications, and agent_result collects the outcome.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/liliang-cn/agentexec"
	"github.com/liliang-cn/agentexec/cmd/agentexec-mcp/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridable at build time with -ldflags.
var version = "dev"

// binaryOverrides collects repeated -provider name=path flags.
type binaryOverrides map[string]string

func (b binaryOverrides) String() string { return fmt.Sprint(map[string]string(b)) }

func (b binaryOverrides) Set(v string) error {
	name, path, ok := strings.Cut(v, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("want name=path, got %q", v)
	}
	b[name] = path
	return nil
}

func main() {
	overrides := binaryOverrides{}
	workspace := flag.String("workspace", "", "directory the delegated agent runs in (required)")
	maxConcurrent := flag.Int("max-concurrent", 2, "how many delegates may run at once")
	flag.Var(overrides, "provider", "override a provider's binary, as name=path; repeatable")
	flag.Parse()

	if err := run(*workspace, *maxConcurrent, overrides); err != nil {
		fmt.Fprintln(os.Stderr, "agentexec-mcp:", err)
		os.Exit(1)
	}
}

func run(workspace string, maxConcurrent int, overrides binaryOverrides) error {
	// Required on purpose: there is no sensible default for where somebody
	// else's agent may write. The delegate runs with permissions bypassed and
	// outside the CLI's own sandbox, so this is an operator's decision.
	if workspace == "" {
		return fmt.Errorf("-workspace is required")
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return fmt.Errorf("workspace %s: %w", workspace, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", workspace)
	}

	reg, registered := buildRegistry(overrides)
	if len(registered) == 0 {
		return fmt.Errorf("no agent CLI found on PATH (looked for claude, codex, gemini); use -provider name=path")
	}
	// stdout is the MCP transport, so anything for a human goes to stderr.
	fmt.Fprintf(os.Stderr, "agentexec-mcp %s: workspace=%s providers=%s max-concurrent=%d\n",
		version, workspace, strings.Join(registered, ","), maxConcurrent)

	srv := mcpserver.New(mcpserver.Config{
		Registry:      reg,
		Workspace:     workspace,
		MaxConcurrent: maxConcurrent,
		Version:       version,
	})

	// A signal must stop the delegates too, not just this process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return srv.Run(ctx, &mcp.StdioTransport{})
}

// buildRegistry registers the agent CLIs that are actually available, so
// list_providers describes what can really be delegated to rather than what
// this program has heard of. An explicit -provider override is always
// registered: the operator naming a path is a stronger signal than PATH.
func buildRegistry(overrides binaryOverrides) (*agentexec.Registry, []string) {
	type candidate struct {
		name string
		ctor func(...agentexec.Option) agentexec.Provider
	}
	candidates := []candidate{
		{"claude", agentexec.NewClaude},
		{"codex", agentexec.NewCodex},
		{"gemini", agentexec.NewGemini},
	}

	reg := agentexec.NewRegistry()
	var registered []string
	for _, c := range candidates {
		binary, overridden := overrides[c.name]
		if !overridden {
			found, err := exec.LookPath(c.name)
			if err != nil {
				continue
			}
			binary = found
		}
		reg.Register(c.ctor(agentexec.WithName(c.name), agentexec.WithBinary(binary)))
		registered = append(registered, c.name)
	}
	return reg, registered
}
