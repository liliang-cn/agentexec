package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agentexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeProvider stands in for an agent CLI: a real process under a real PTY,
// with pretend argv and frame parsing, so these tests need no agent installed.
type fakeProvider struct {
	argv    []string
	summary string
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Capabilities() agentexec.Capabilities {
	return agentexec.Capabilities{Streaming: true, Resume: true}
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
	return agentexec.Result{ExitCode: exitCode, Summary: s.p.summary}, nil, nil
}

func (s *fakeSession) SessionID() string { return "fake-session" }

// connect stands up the server and an in-memory client, exercising the real
// protocol rather than calling the handlers directly.
func connect(t *testing.T, p *fakeProvider, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	reg := agentexec.NewRegistry()
	reg.Register(p)

	srv := New(Config{Registry: reg, Workspace: t.TempDir(), MaxConcurrent: 2, Version: "test"})
	t.Cleanup(srv.Close)

	clientT, serverT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, opts).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool[T any](t *testing.T, cs *mcp.ClientSession, name string, args any) T {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned an error result: %+v", name, res.Content)
	}
	var out T
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s output: %v", name, err)
	}
	return out
}

// waitForRun polls agent_result until the run leaves the live states.
func waitForRun(t *testing.T, cs *mcp.ClientSession, id string) RunView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		view := callTool[RunView](t, cs, "agent_result", runIDInput{RunID: id})
		if view.State != "queued" && view.State != "running" {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stuck in %q", id, view.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestToolsAreAdvertised(t *testing.T) {
	cs := connect(t, &fakeProvider{argv: []string{"printf", "x"}}, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"agent_start", "agent_result", "agent_status", "agent_cancel", "list_providers"} {
		if !got[want] {
			t.Errorf("tool %q was not advertised", want)
		}
	}
}

func TestStartReturnsWithoutWaitingThenResultCollects(t *testing.T) {
	p := &fakeProvider{argv: []string{"sh", "-c", "printf thinking; sleep 0.3"}, summary: "the answer"}
	cs := connect(t, p, nil)

	begin := time.Now()
	start := callTool[StartOutput](t, cs, "agent_start", map[string]any{
		"provider": "fake", "prompt": "do a thing",
	})
	if elapsed := time.Since(begin); elapsed > 250*time.Millisecond {
		t.Fatalf("agent_start took %v; it must dispatch, not wait for a 0.3s delegate", elapsed)
	}
	if start.RunID == "" {
		t.Fatal("no run id")
	}
	if start.Resource != RunURI(start.RunID) {
		t.Fatalf("Resource = %q, want %q", start.Resource, RunURI(start.RunID))
	}

	view := waitForRun(t, cs, start.RunID)
	if view.State != "done" || view.Answer != "the answer" {
		t.Fatalf("final view = %+v", view)
	}
	if view.SessionID != "fake-session" {
		t.Fatalf("SessionID = %q", view.SessionID)
	}
}

// Asking for a result too early has to be cheap and legible, not an error and
// not a block.
func TestResultOnAnUnfinishedRunReportsState(t *testing.T) {
	p := &fakeProvider{argv: []string{"sleep", "5"}, summary: "never"}
	cs := connect(t, p, nil)

	start := callTool[StartOutput](t, cs, "agent_start", map[string]any{"provider": "fake", "prompt": "p"})
	view := callTool[RunView](t, cs, "agent_result", runIDInput{RunID: start.RunID})

	if view.State != "queued" && view.State != "running" {
		t.Fatalf("State = %q, want it to still be live", view.State)
	}
	if view.Note == "" {
		t.Fatal("an unfinished run came back with no note explaining why the answer is empty")
	}
	callTool[RunView](t, cs, "agent_cancel", runIDInput{RunID: start.RunID})
}

// Progress has to reach a subscriber while the delegate is still working, not
// only once it finishes. The notification says "this changed"; the resource
// says what it changed to.
func TestProgressReachesASubscriberWhileRunning(t *testing.T) {
	updated := make(chan string, 64)
	opts := &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			select {
			case updated <- req.Params.URI:
			default:
			}
		},
	}
	// Speaks, then keeps working, so a notification must arrive before the end.
	p := &fakeProvider{argv: []string{"sh", "-c", "printf working-on-it; sleep 1"}, summary: "done"}
	cs := connect(t, p, opts)
	ctx := context.Background()

	// A real client subscribes with the URI agent_start hands back.
	start := callTool[StartOutput](t, cs, "agent_start", map[string]any{"provider": "fake", "prompt": "p"})
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: start.Resource}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Wait for a notification that arrives while the delegate is still running.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case uri := <-updated:
			if uri != start.Resource {
				continue
			}
			view := callTool[RunView](t, cs, "agent_result", runIDInput{RunID: start.RunID})
			if view.State != "running" {
				continue // a late notification; keep looking for a live one
			}
			for _, e := range view.Events {
				if strings.Contains(e.Text, "working-on-it") {
					callTool[RunView](t, cs, "agent_cancel", runIDInput{RunID: start.RunID})
					return
				}
			}
		case <-deadline:
			t.Fatal("no resource update carried the delegate's output while it was still running")
		}
	}
}

func TestRunIsReadableAsAResource(t *testing.T) {
	p := &fakeProvider{argv: []string{"printf", "x"}, summary: "resource answer"}
	cs := connect(t, p, nil)

	start := callTool[StartOutput](t, cs, "agent_start", map[string]any{"provider": "fake", "prompt": "p"})
	waitForRun(t, cs, start.RunID)

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: start.Resource})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("Contents = %d, want 1", len(res.Contents))
	}
	var view RunView
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &view); err != nil {
		t.Fatalf("resource body: %v", err)
	}
	if view.RunID != start.RunID || view.Answer != "resource answer" {
		t.Fatalf("resource view = %+v", view)
	}
}

func TestCancelStopsARun(t *testing.T) {
	p := &fakeProvider{argv: []string{"sleep", "30"}, summary: "never"}
	cs := connect(t, p, nil)

	start := callTool[StartOutput](t, cs, "agent_start", map[string]any{"provider": "fake", "prompt": "p"})
	view := callTool[RunView](t, cs, "agent_cancel", runIDInput{RunID: start.RunID})
	if view.State != "cancelled" {
		t.Fatalf("State = %q, want cancelled", view.State)
	}
	// Cancelling again reports the same state rather than failing.
	again := callTool[RunView](t, cs, "agent_cancel", runIDInput{RunID: start.RunID})
	if again.State != "cancelled" {
		t.Fatalf("second cancel = %q", again.State)
	}
}

func TestStatusListsEveryRunWithoutEventBodies(t *testing.T) {
	p := &fakeProvider{argv: []string{"printf", "chatty"}, summary: "ok"}
	cs := connect(t, p, nil)

	var ids []string
	for range 2 {
		s := callTool[StartOutput](t, cs, "agent_start", map[string]any{"provider": "fake", "prompt": "p"})
		ids = append(ids, s.RunID)
	}
	for _, id := range ids {
		waitForRun(t, cs, id)
	}

	all := callTool[StatusOutput](t, cs, "agent_status", map[string]any{})
	if len(all.Runs) != 2 {
		t.Fatalf("listed %d runs, want 2", len(all.Runs))
	}
	for _, r := range all.Runs {
		if len(r.Events) != 0 {
			t.Fatalf("a listing carried event bodies: %+v", r.Events)
		}
	}

	one := callTool[StatusOutput](t, cs, "agent_status", map[string]any{"run_id": ids[0]})
	if len(one.Runs) != 1 || one.Runs[0].RunID != ids[0] {
		t.Fatalf("single status = %+v", one.Runs)
	}
	if len(one.Runs[0].Events) == 0 {
		t.Fatal("asking for one run dropped its events; that is where they belong")
	}
}

func TestListProvidersReportsCapabilities(t *testing.T) {
	cs := connect(t, &fakeProvider{argv: []string{"printf", "x"}}, nil)
	out := callTool[ProvidersOutput](t, cs, "list_providers", struct{}{})
	if len(out.Providers) != 1 || out.Providers[0].Name != "fake" {
		t.Fatalf("providers = %+v", out.Providers)
	}
	if !out.Providers[0].Streaming || !out.Providers[0].Resume {
		t.Fatalf("capabilities not carried over: %+v", out.Providers[0])
	}
}

func TestUnknownRunIsAnError(t *testing.T) {
	cs := connect(t, &fakeProvider{argv: []string{"printf", "x"}}, nil)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "agent_result", Arguments: runIDInput{RunID: "nope"},
	})
	if err == nil && !res.IsError {
		t.Fatal("an unknown run id came back as success")
	}
}
