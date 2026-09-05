// Package runner queues delegated runs and executes them under a bounded
// worker pool. It knows about agentexec and about the store, and nothing about
// MCP: what it has to say goes to a Sink the caller supplies.
package runner

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/liliang-cn/agentexec"
	"github.com/liliang-cn/agentexec/cmd/agentexec-mcp/internal/runstore"
	"github.com/liliang-cn/agentexec/pty"
)

// Sink receives everything worth telling a client about. Both methods may be
// called from several workers at once and must not block for long.
type Sink interface {
	// Progress reports one thing a delegate did, as it happens.
	Progress(runID, kind, text string)
	// Updated reports that a run's snapshot has changed.
	Updated(runID string)
}

// NopSink discards everything. Useful for tests and for a server with no
// client attached yet.
type NopSink struct{}

func (NopSink) Progress(string, string, string) {}
func (NopSink) Updated(string)                  {}

// Config is what a Runner needs to exist.
type Config struct {
	Registry  *agentexec.Registry
	Store     *runstore.Store
	Sink      Sink
	Workspace string
	// MaxConcurrent bounds how many delegates run at once. Delegation is cheap
	// to ask for and expensive to serve. Defaults to 2.
	MaxConcurrent int
	// QueueDepth bounds how many runs may wait. Defaults to 64.
	QueueDepth int
}

// Request is one delegation.
type Request struct {
	Provider        string `json:"provider" jsonschema:"which agent CLI to delegate to; see list_providers"`
	Prompt          string `json:"prompt" jsonschema:"the task to hand over"`
	Model           string `json:"model,omitempty" jsonschema:"optional model override"`
	SystemPrompt    string `json:"system_prompt,omitempty" jsonschema:"optional instructions prepended to the delegate's own"`
	ResumeSessionID string `json:"resume_session_id,omitempty" jsonschema:"continue a previous run's session, from its session_id"`
}

// ErrQueueFull is returned when the queue cannot take another run.
var ErrQueueFull = errors.New("runner: queue is full")

type job struct {
	id  string
	ctx context.Context
	req Request
}

// Runner owns the queue and the workers.
type Runner struct {
	cfg    Config
	queue  chan job
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
}

// New starts a Runner's workers. Close stops them.
func New(cfg Config) *Runner {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 64
	}
	if cfg.Sink == nil {
		cfg.Sink = NopSink{}
	}
	r := &Runner{
		cfg:    cfg,
		queue:  make(chan job, cfg.QueueDepth),
		closed: make(chan struct{}),
	}
	for range cfg.MaxConcurrent {
		r.wg.Add(1)
		go r.worker()
	}
	return r
}

// Start queues req and returns its run id straight away. It does not wait for
// the delegate: that is the whole point of the shape.
func (r *Runner) Start(req Request) (string, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("runner: prompt is empty")
	}
	if _, err := r.cfg.Registry.Get(req.Provider); err != nil {
		return "", err
	}

	// The run's context is cancelled by the store, which is what Cancel and
	// shutdown both go through.
	ctx, cancel := context.WithCancel(context.Background())
	id := r.cfg.Store.Create(req.Provider, req.Prompt, cancel)

	select {
	case r.queue <- job{id: id, ctx: ctx, req: req}:
		r.cfg.Sink.Updated(id)
		return id, nil
	default:
		cancel()
		r.cfg.Store.Finish(id, runstore.Outcome{Err: ErrQueueFull.Error()})
		r.cfg.Sink.Updated(id)
		return "", ErrQueueFull
	}
}

// Close cancels every live run and waits for the workers to stop.
func (r *Runner) Close() {
	r.once.Do(func() {
		close(r.closed)
		r.cfg.Store.CancelAll()
		close(r.queue)
	})
	r.wg.Wait()
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for j := range r.queue {
		r.execute(j)
	}
}

func (r *Runner) execute(j job) {
	started, err := r.cfg.Store.Start(j.id)
	if err != nil || !started {
		// Cancelled off the queue before anyone picked it up.
		r.cfg.Sink.Updated(j.id)
		return
	}
	r.cfg.Sink.Updated(j.id)

	out := r.run(j)
	r.cfg.Store.Finish(j.id, out)
	r.cfg.Sink.Updated(j.id)
}

// run does the actual delegation and reports what came of it.
func (r *Runner) run(j job) runstore.Outcome {
	provider, err := r.cfg.Registry.Get(j.req.Provider)
	if err != nil {
		return runstore.Outcome{Err: err.Error()}
	}
	sess := provider.NewSession()

	spec, err := sess.BuildCommand(j.ctx, agentexec.Request{
		RunID:           j.id,
		Prompt:          j.req.Prompt,
		SystemPrompt:    j.req.SystemPrompt,
		Model:           j.req.Model,
		ResumeSessionID: j.req.ResumeSessionID,
		WorkspacePath:   r.cfg.Workspace,
		PermissionMode:  agentexec.PermissionBypass,
		// Never negotiable: a delegate that inherited the operator's MCP
		// config would start this very server again, and so would its own
		// delegate, without limit.
		NoMCP: true,
	})
	if err != nil {
		return runstore.Outcome{Err: err.Error()}
	}

	res, runErr := pty.Run(j.ctx, pty.Command{
		Argv:    spec.Argv,
		Env:     spec.Env,
		WorkDir: spec.WorkDir,
		Stdin:   spec.Stdin,
	}, func(chunk []byte) {
		events, _ := sess.ParseChunk(chunk)
		r.report(j.id, events)
	})

	// Finalize even after an error: a cancelled run still has usage and a
	// partial answer worth keeping, and the library parses whatever it is
	// handed here if nothing streamed through ParseChunk.
	result, tail, finErr := sess.Finalize(j.ctx, res.Output, res.ExitCode)
	r.report(j.id, tail)

	out := runstore.Outcome{
		Answer:    result.Summary,
		SessionID: sess.SessionID(),
		ExitCode:  result.ExitCode,
		Failed:    result.Failed,
		Usage: runstore.Usage{
			Model:            result.Usage.Model,
			InputTokens:      result.Usage.InputTokens,
			OutputTokens:     result.Usage.OutputTokens,
			CacheTokens:      result.Usage.CacheTokens,
			EstimatedCostUSD: result.Usage.EstimatedCostUSD,
		},
	}
	switch {
	case runErr != nil:
		out.Err = runErr.Error()
	case finErr != nil:
		out.Err = finErr.Error()
	}
	return out
}

// report turns provider events into store entries and progress notifications.
// Lifecycle chatter is dropped: a caller wants to know what the delegate said
// and did, not that its hooks fired.
func (r *Runner) report(id string, events []agentexec.Event) {
	for _, e := range events {
		kind, text := summarize(e)
		if text == "" {
			continue
		}
		r.cfg.Store.Append(id, kind, text)
		r.cfg.Sink.Progress(id, kind, text)
	}
}

// summarize reduces one event to a line, or to "" when it is not worth
// reporting.
func summarize(e agentexec.Event) (kind, text string) {
	switch e.Type {
	case agentexec.EventAgentMessage:
		// Provider lifecycle frames land here too; the role is what separates
		// them from what the model actually said.
		if e.Payload["role"] != "assistant" {
			return "", ""
		}
		s, _ := e.Payload["text"].(string)
		return "message", strings.TrimSpace(s)
	case agentexec.EventToolCall:
		return "tool_call", toolName(e.Payload)
	case agentexec.EventRateLimit:
		status, _ := e.Payload["status"].(string)
		if status == "" {
			return "", ""
		}
		return "rate_limit", status
	default:
		// terminal.output and tool results are noise at this level.
		return "", ""
	}
}

// toolName digs the tool's name out of whichever shape the provider used.
func toolName(payload map[string]any) string {
	for _, key := range []string{"name", "tool_name", "type"} {
		if s, ok := payload[key].(string); ok && s != "" {
			return s
		}
	}
	if item, ok := payload["item"].(map[string]any); ok {
		return toolName(item)
	}
	return "tool"
}
