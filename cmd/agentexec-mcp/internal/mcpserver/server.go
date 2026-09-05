// Package mcpserver binds the runner to MCP: tools to start and inspect
// delegated runs, a resource per run, and the two notification channels that
// carry progress out to a client. It holds no policy of its own.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/agentexec"
	"github.com/liliang-cn/agentexec/cmd/agentexec-mcp/internal/runner"
	"github.com/liliang-cn/agentexec/cmd/agentexec-mcp/internal/runstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	runURIScheme   = "agentexec://runs/"
	runURITemplate = runURIScheme + "{run_id}"
)

// RunURI is where a client subscribes to follow one run.
func RunURI(id string) string { return runURIScheme + id }

// Config is what a Server needs to exist.
type Config struct {
	Registry      *agentexec.Registry
	Workspace     string
	MaxConcurrent int
	Version       string
}

// Server is the MCP surface plus the runner behind it.
type Server struct {
	mcp    *mcp.Server
	store  *runstore.Store
	runner *runner.Runner
}

// New wires everything together. The runner's notification sink needs the MCP
// server, and the tools need the runner, so the order here is not arbitrary.
func New(cfg Config) *Server {
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	store := runstore.New()

	// Subscriptions are the whole progress mechanism, and the SDK refuses
	// resources/subscribe outright unless both handlers are supplied. They
	// exist to vet the URI, not to gate access: anyone who can call the tools
	// can already see the runs.
	m := mcp.NewServer(&mcp.Implementation{Name: "agentexec", Version: version}, &mcp.ServerOptions{
		SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
			_, err := runIDFromURI(store, req.Params.URI)
			return err
		},
		UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
	})
	r := runner.New(runner.Config{
		Registry:      cfg.Registry,
		Store:         store,
		Sink:          &notifier{mcp: m},
		Workspace:     cfg.Workspace,
		MaxConcurrent: cfg.MaxConcurrent,
	})

	s := &Server{mcp: m, store: store, runner: r}
	s.registerTools(cfg.Registry)
	s.registerResources()
	return s
}

// Run serves until the client disconnects, then stops every live delegate.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	defer s.runner.Close()
	return s.mcp.Run(ctx, t)
}

// MCP exposes the underlying server, for tests that connect an in-memory client.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// Close stops the runner. Run does this too; this is for callers that never ran.
func (s *Server) Close() { s.runner.Close() }

// notifier tells subscribed clients that a run's resource has changed. Every
// line the delegate produces triggers one, which is what makes progress live.
//
// There is deliberately only one channel. MCP's logging notifications could
// have carried the text itself, but they are deprecated as of protocol version
// 2026-07-28 (SEP-2577), and they send nothing at all until a client happens to
// call logging/setLevel — a progress channel that is silent by default is worse
// than no progress channel. The resource carries strictly more: state, usage,
// and every event so far, in one read.
type notifier struct{ mcp *mcp.Server }

// Progress means the run's resource now says something new.
func (n *notifier) Progress(runID, _, _ string) { n.Updated(runID) }

func (n *notifier) Updated(runID string) {
	_ = n.mcp.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{
		URI: RunURI(runID),
	})
}

// --- tool payloads ---

// StartOutput is what a caller gets back the moment a run is queued.
type StartOutput struct {
	RunID string `json:"run_id" jsonschema:"pass this to agent_result, agent_status and agent_cancel"`
	State string `json:"state" jsonschema:"queued or running"`
	// Resource is what to subscribe to for updates, for a client that follows
	// resources rather than reading the log stream.
	Resource string `json:"resource" jsonschema:"MCP resource URI carrying this run's live snapshot"`
}

// RunView is one run, as reported to a client.
type RunView struct {
	RunID     string           `json:"run_id"`
	Provider  string           `json:"provider"`
	State     string           `json:"state"`
	Prompt    string           `json:"prompt,omitempty"`
	Answer    string           `json:"answer,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	Usage     runstore.Usage   `json:"usage"`
	ExitCode  int              `json:"exit_code"`
	Failed    bool             `json:"failed"`
	Error     string           `json:"error,omitempty"`
	Events    []runstore.Event `json:"events,omitempty"`
	QueuedAt  time.Time        `json:"queued_at"`
	EndedAt   time.Time        `json:"ended_at,omitzero"`
	// Note explains a state a caller may not have expected, rather than
	// leaving them to infer it from an empty answer.
	Note string `json:"note,omitempty"`
}

type runIDInput struct {
	RunID string `json:"run_id" jsonschema:"the id returned by agent_start"`
}

type statusInput struct {
	RunID string `json:"run_id,omitempty" jsonschema:"one run; omit to list every run"`
}

// StatusOutput lists runs.
type StatusOutput struct {
	Runs []RunView `json:"runs"`
}

// ProviderView describes one registered agent CLI.
type ProviderView struct {
	Name      string `json:"name"`
	Streaming bool   `json:"streaming"`
	Resume    bool   `json:"resume"`
	MCP       bool   `json:"mcp"`
	Plugins   bool   `json:"plugins"`
}

// ProvidersOutput lists what can be delegated to.
type ProvidersOutput struct {
	Providers []ProviderView `json:"providers"`
}

func (s *Server) registerTools(reg *agentexec.Registry) {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "agent_start",
		Description: "Hand a task to another agent CLI. Returns a run id immediately — it does " +
			"not wait for the delegate. Progress arrives as log notifications and as updates to " +
			"the run's resource; collect the outcome with agent_result.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.Request) (*mcp.CallToolResult, StartOutput, error) {
		id, err := s.runner.Start(in)
		if err != nil {
			return nil, StartOutput{}, err
		}
		snap, err := s.store.Get(id)
		if err != nil {
			return nil, StartOutput{}, err
		}
		return nil, StartOutput{RunID: id, State: string(snap.State), Resource: RunURI(id)}, nil
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "agent_result",
		Description: "Collect what a delegated run produced. If it has not finished, this reports " +
			"the current state instead of waiting.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in runIDInput) (*mcp.CallToolResult, RunView, error) {
		snap, err := s.store.Get(in.RunID)
		if err != nil {
			return nil, RunView{}, err
		}
		return nil, viewOf(snap), nil
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "agent_status",
		Description: "Show one delegated run, or every run of this session when run_id is omitted.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, StatusOutput, error) {
		if in.RunID != "" {
			snap, err := s.store.Get(in.RunID)
			if err != nil {
				return nil, StatusOutput{}, err
			}
			return nil, StatusOutput{Runs: []RunView{viewOf(snap)}}, nil
		}
		snaps := s.store.List()
		out := StatusOutput{Runs: make([]RunView, 0, len(snaps))}
		for _, snap := range snaps {
			view := viewOf(snap)
			view.Events = nil // a listing is a listing
			out.Runs = append(out.Runs, view)
		}
		return nil, out, nil
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "agent_cancel",
		Description: "Stop a delegated run. Cancelling one that already finished is not an error.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in runIDInput) (*mcp.CallToolResult, RunView, error) {
		if _, err := s.store.Cancel(in.RunID); err != nil {
			return nil, RunView{}, err
		}
		snap, err := s.store.Get(in.RunID)
		if err != nil {
			return nil, RunView{}, err
		}
		return nil, viewOf(snap), nil
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_providers",
		Description: "Which agent CLIs this server can delegate to.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProvidersOutput, error) {
		out := ProvidersOutput{}
		for _, name := range reg.Names() {
			p, err := reg.Get(name)
			if err != nil {
				continue
			}
			c := p.Capabilities()
			out.Providers = append(out.Providers, ProviderView{
				Name: name, Streaming: c.Streaming, Resume: c.Resume, MCP: c.MCP, Plugins: c.Plugins,
			})
		}
		return nil, out, nil
	})
}

func (s *Server) registerResources() {
	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: runURITemplate,
		Name:        "delegated run",
		Description: "Live snapshot of one delegated run: state, everything the delegate has said so far, and its usage.",
		MIMEType:    "application/json",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id, err := runIDFromURI(s.store, req.Params.URI)
		if err != nil {
			return nil, err
		}
		snap, err := s.store.Get(id)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, viewOf(snap))
	})
}

func viewOf(snap runstore.Snapshot) RunView {
	v := RunView{
		RunID:     snap.ID,
		Provider:  snap.Provider,
		State:     string(snap.State),
		Prompt:    snap.Prompt,
		Answer:    snap.Answer,
		SessionID: snap.SessionID,
		Usage:     snap.Usage,
		ExitCode:  snap.ExitCode,
		Failed:    snap.Failed,
		Error:     snap.Err,
		Events:    snap.Events,
		QueuedAt:  snap.QueuedAt,
		EndedAt:   snap.EndedAt,
	}
	switch snap.State {
	case runstore.Queued:
		v.Note = "waiting for a free worker; nothing has run yet"
	case runstore.Running:
		v.Note = "still running; the answer is not final"
	case runstore.Cancelled:
		v.Note = "cancelled before it finished"
	}
	return v
}

// jsonResource renders v as the JSON body of a resource read.
func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(b),
		}},
	}, nil
}

// runIDFromURI pulls a run id out of a resource URI and checks it names a run
// this server actually issued.
func runIDFromURI(store *runstore.Store, uri string) (string, error) {
	id := strings.TrimPrefix(uri, runURIScheme)
	if id == uri || id == "" {
		return "", fmt.Errorf("not a run URI: %q", uri)
	}
	if _, err := store.Get(id); err != nil {
		return "", err
	}
	return id, nil
}
