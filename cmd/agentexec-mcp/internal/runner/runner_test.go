package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liliang-cn/agentexec"
	"github.com/liliang-cn/agentexec/cmd/agentexec-mcp/internal/runstore"
)

// fakeProvider stands in for an agent CLI. Its command is a real process run
// under a real PTY — only the argv and the frame parsing are pretend, so these
// tests need no claude, codex or gemini on the box.
type fakeProvider struct {
	name    string
	argv    []string
	summary string
	failed  bool
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Capabilities() agentexec.Capabilities {
	return agentexec.Capabilities{Streaming: true}
}
func (p *fakeProvider) NewSession() agentexec.Session { return &fakeSession{p: p} }

type fakeSession struct{ p *fakeProvider }

func (s *fakeSession) BuildCommand(context.Context, agentexec.Request) (agentexec.CommandSpec, error) {
	return agentexec.CommandSpec{Argv: s.p.argv}, nil
}

func (s *fakeSession) ParseChunk(chunk []byte) ([]agentexec.Event, error) {
	text := strings.TrimSpace(string(chunk))
	if text == "" {
		return nil, nil
	}
	return []agentexec.Event{{
		Type:    agentexec.EventAgentMessage,
		Payload: map[string]any{"role": "assistant", "text": text},
	}}, nil
}

func (s *fakeSession) Finalize(_ context.Context, _ []byte, exitCode int) (agentexec.Result, []agentexec.Event, error) {
	return agentexec.Result{
		ExitCode: exitCode,
		Summary:  s.p.summary,
		Failed:   s.p.failed,
		Usage:    agentexec.Usage{Model: "fake-1", InputTokens: 11, OutputTokens: 22},
	}, nil, nil
}

func (s *fakeSession) SessionID() string { return "fake-session" }

// recordingSink captures what the runner would have pushed to a client.
type recordingSink struct {
	mu       sync.Mutex
	progress []string
	updated  []string
}

func (r *recordingSink) Progress(id, kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, id+" "+kind+" "+text)
}

func (r *recordingSink) Updated(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updated = append(r.updated, id)
}

func (r *recordingSink) snapshot() (progress, updated []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.progress...), append([]string(nil), r.updated...)
}

func newHarness(t *testing.T, p *fakeProvider, maxConcurrent int) (*Runner, *runstore.Store, *recordingSink) {
	t.Helper()
	reg := agentexec.NewRegistry()
	reg.Register(p)
	store := runstore.New()
	sink := &recordingSink{}
	r := New(Config{
		Registry:      reg,
		Store:         store,
		Sink:          sink,
		Workspace:     t.TempDir(),
		MaxConcurrent: maxConcurrent,
	})
	t.Cleanup(r.Close)
	return r, store, sink
}

// waitForState polls until the run settles, rather than sleeping a guessed
// interval and hoping.
func waitForState(t *testing.T, store *runstore.Store, id string, want runstore.State) runstore.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if snap.State == want {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %q is %q after 10s, want %q", id, snap.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartReturnsImmediatelyAndRunCompletes(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"printf", "working on it"}, summary: "the answer"}
	r, store, sink := newHarness(t, p, 2)

	begin := time.Now()
	id, err := r.Start(Request{Provider: "fake", Prompt: "do a thing"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("Start took %v; it is supposed to dispatch, not wait", elapsed)
	}

	snap := waitForState(t, store, id, runstore.Done)
	if snap.Answer != "the answer" {
		t.Fatalf("Answer = %q", snap.Answer)
	}
	if snap.SessionID != "fake-session" || snap.Usage.InputTokens != 11 {
		t.Fatalf("outcome not carried over: %+v", snap)
	}

	progress, updated := sink.snapshot()
	if len(progress) == 0 {
		t.Fatal("nothing was pushed as progress")
	}
	if !strings.Contains(strings.Join(progress, "\n"), "working on it") {
		t.Fatalf("progress = %v, want the delegate's output", progress)
	}
	if len(updated) < 2 {
		t.Fatalf("updated = %v, want at least queued and finished", updated)
	}
}

func TestRunsProceedInParallel(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sh", "-c", "printf a; sleep 0.4"}, summary: "ok"}
	r, store, _ := newHarness(t, p, 3)

	begin := time.Now()
	var ids []string
	for range 3 {
		id, err := r.Start(Request{Provider: "fake", Prompt: "p"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		waitForState(t, store, id, runstore.Done)
	}
	// Serialized, three 0.4s runs cannot finish in under a second.
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("three concurrent runs took %v; they were serialized", elapsed)
	}
}

func TestNonZeroExitIsAFailedRun(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sh", "-c", "exit 4"}, summary: ""}
	r, store, _ := newHarness(t, p, 1)

	id, err := r.Start(Request{Provider: "fake", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	snap := waitForState(t, store, id, runstore.Failed)
	if snap.ExitCode != 4 {
		t.Fatalf("ExitCode = %d, want 4", snap.ExitCode)
	}
}

// The provider's own verdict has to fail the run even when the process exited
// zero, which is exactly the case the library added Result.Failed for.
func TestProviderVerdictFailsRunDespiteZeroExit(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"printf", "hi"}, summary: "Failed to authenticate", failed: true}
	r, store, _ := newHarness(t, p, 1)

	id, err := r.Start(Request{Provider: "fake", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	snap := waitForState(t, store, id, runstore.Failed)
	if snap.ExitCode != 0 {
		t.Fatalf("ExitCode = %d; the point of this case is that it is zero", snap.ExitCode)
	}
}

func TestCancelStopsARunningDelegate(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sleep", "30"}, summary: "never"}
	r, store, _ := newHarness(t, p, 1)

	id, err := r.Start(Request{Provider: "fake", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, store, id, runstore.Running)

	state, err := store.Cancel(id)
	if err != nil || state != runstore.Cancelled {
		t.Fatalf("Cancel = %q, %v", state, err)
	}
	// The worker must actually come back, which it only does if the process died.
	waitForState(t, store, id, runstore.Cancelled)
}

// A run cancelled while still queued must never start.
func TestCancelledQueuedRunNeverStarts(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sleep", "30"}, summary: "x"}
	r, store, _ := newHarness(t, p, 1)

	blocking, err := r.Start(Request{Provider: "fake", Prompt: "occupies the only worker"})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, store, blocking, runstore.Running)

	queued, err := r.Start(Request{Provider: "fake", Prompt: "waits"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(queued); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(blocking); err != nil {
		t.Fatal(err)
	}

	snap := waitForState(t, store, queued, runstore.Cancelled)
	if !snap.StartedAt.IsZero() {
		t.Fatal("a run cancelled off the queue was started anyway")
	}
}

func TestStartRejectsUnknownProviderAndEmptyPrompt(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"printf", "x"}}
	r, store, _ := newHarness(t, p, 1)

	if _, err := r.Start(Request{Provider: "nope", Prompt: "p"}); err == nil {
		t.Fatal("unknown provider was accepted")
	}
	if _, err := r.Start(Request{Provider: "fake", Prompt: "   "}); err == nil {
		t.Fatal("empty prompt was accepted")
	}
	if runs := store.List(); len(runs) != 0 {
		t.Fatalf("rejected requests left %d runs behind", len(runs))
	}
}

func TestCloseCancelsLiveRuns(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sleep", "30"}, summary: "x"}
	reg := agentexec.NewRegistry()
	reg.Register(p)
	store := runstore.New()
	r := New(Config{Registry: reg, Store: store, Workspace: t.TempDir(), MaxConcurrent: 1})

	id, err := r.Start(Request{Provider: "fake", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, store, id, runstore.Running)

	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return; a delegate was left running")
	}
	if snap, _ := store.Get(id); snap.State != runstore.Cancelled {
		t.Fatalf("State = %q after Close, want cancelled", snap.State)
	}
}

func TestQueueFullIsReported(t *testing.T) {
	p := &fakeProvider{name: "fake", argv: []string{"sleep", "30"}, summary: "x"}
	reg := agentexec.NewRegistry()
	reg.Register(p)
	store := runstore.New()
	r := New(Config{Registry: reg, Store: store, Workspace: t.TempDir(), MaxConcurrent: 1, QueueDepth: 1})
	t.Cleanup(r.Close)

	// One worker plus a depth of one accepts two runs at most; whether the
	// second sits in the queue or has just been picked up is a race, so push
	// until the queue says no rather than asserting on a particular count.
	var queueFull bool
	for range 8 {
		if _, err := r.Start(Request{Provider: "fake", Prompt: "p"}); errors.Is(err, ErrQueueFull) {
			queueFull = true
			break
		}
	}
	if !queueFull {
		t.Fatal("the queue never reported itself full")
	}

	// A rejection is recorded rather than vanishing, so an operator can see it.
	var sawRejection bool
	for _, snap := range store.List() {
		if snap.State == runstore.Failed && strings.Contains(snap.Err, "queue is full") {
			sawRejection = true
		}
	}
	if !sawRejection {
		t.Fatal("the rejected run left no trace in the store")
	}
}

func TestSummarizeIgnoresLifecycleFrames(t *testing.T) {
	for name, e := range map[string]agentexec.Event{
		"system init":     {Type: agentexec.EventAgentMessage, Payload: map[string]any{"role": "system", "raw": map[string]any{}}},
		"result frame":    {Type: agentexec.EventAgentMessage, Payload: map[string]any{"role": "result"}},
		"terminal output": {Type: agentexec.EventTerminalOutput, Payload: map[string]any{"line": "noise"}},
		"tool result":     {Type: agentexec.EventToolResult, Payload: map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, text := summarize(e); text != "" {
				t.Fatalf("summarize reported %q; lifecycle chatter is not progress", text)
			}
		})
	}
}

func TestSummarizeNamesToolCalls(t *testing.T) {
	got := map[string]string{}
	for name, payload := range map[string]map[string]any{
		"claude shape": {"type": "tool_use", "name": "Read"},
		"codex shape":  {"item": map[string]any{"type": "command_execution"}},
	} {
		kind, text := summarize(agentexec.Event{Type: agentexec.EventToolCall, Payload: payload})
		if kind != "tool_call" {
			t.Fatalf("%s: kind = %q", name, kind)
		}
		got[name] = text
	}
	if got["claude shape"] != "Read" {
		t.Fatalf("claude tool name = %q, want Read", got["claude shape"])
	}
	if got["codex shape"] != "command_execution" {
		t.Fatalf("codex tool name = %q", got["codex shape"])
	}
}
