package runstore

import (
	"errors"
	"sync"
	"testing"
)

func TestCreateQueuesAndIssuesDistinctIDs(t *testing.T) {
	s := New()
	a := s.Create("codex", "one", nil)
	b := s.Create("claude", "two", nil)
	if a == b {
		t.Fatalf("ids collided: %q", a)
	}
	snap, err := s.Get(a)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != Queued || snap.Provider != "codex" || snap.Prompt != "one" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.QueuedAt.IsZero() {
		t.Fatal("QueuedAt not set")
	}
}

func TestLifecycleQueuedRunningDone(t *testing.T) {
	s := New()
	id := s.Create("codex", "p", nil)

	started, err := s.Start(id)
	if err != nil || !started {
		t.Fatalf("Start = %v, %v; want true, nil", started, err)
	}
	if snap, _ := s.Get(id); snap.State != Running || snap.StartedAt.IsZero() {
		t.Fatalf("after Start: %+v", snap)
	}

	s.Finish(id, Outcome{Answer: "done", SessionID: "sess", Usage: Usage{InputTokens: 5}})
	snap, _ := s.Get(id)
	if snap.State != Done {
		t.Fatalf("State = %q, want done", snap.State)
	}
	if snap.Answer != "done" || snap.SessionID != "sess" || snap.Usage.InputTokens != 5 {
		t.Fatalf("outcome not recorded: %+v", snap)
	}
	if snap.EndedAt.IsZero() {
		t.Fatal("EndedAt not set")
	}
}

// The provider's verdict and the exit code are separate reasons to fail, and
// each has to be enough on its own: a claude with a revoked token exits zero.
func TestFinishFailsOnVerdictExitCodeOrError(t *testing.T) {
	for name, out := range map[string]Outcome{
		"provider verdict": {Failed: true},
		"non-zero exit":    {ExitCode: 3},
		"could not run":    {Err: "binary not found"},
	} {
		t.Run(name, func(t *testing.T) {
			s := New()
			id := s.Create("codex", "p", nil)
			if _, err := s.Start(id); err != nil {
				t.Fatal(err)
			}
			s.Finish(id, out)
			if snap, _ := s.Get(id); snap.State != Failed {
				t.Fatalf("State = %q, want failed", snap.State)
			}
		})
	}
}

func TestCancelQueuedRunStopsItStartingAndCallsCancel(t *testing.T) {
	s := New()
	called := false
	id := s.Create("codex", "p", func() { called = true })

	state, err := s.Cancel(id)
	if err != nil || state != Cancelled {
		t.Fatalf("Cancel = %q, %v", state, err)
	}
	if !called {
		t.Fatal("cancel func was not called")
	}
	started, err := s.Start(id)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("Start returned true for a cancelled run; the worker would have run it anyway")
	}
}

// A cancelled run stays cancelled. Whatever the delegate managed to emit on its
// way out does not turn it into a normal finish.
func TestFinishDoesNotOverwriteCancelled(t *testing.T) {
	s := New()
	id := s.Create("codex", "p", nil)
	if _, err := s.Start(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	s.Finish(id, Outcome{Answer: "trailing"})

	snap, _ := s.Get(id)
	if snap.State != Cancelled {
		t.Fatalf("State = %q, want cancelled", snap.State)
	}
	if snap.Answer != "trailing" {
		t.Fatalf("Answer = %q; what it managed to say is still worth keeping", snap.Answer)
	}
}

func TestCancelFinishedRunReportsExistingState(t *testing.T) {
	s := New()
	id := s.Create("codex", "p", nil)
	if _, err := s.Start(id); err != nil {
		t.Fatal(err)
	}
	s.Finish(id, Outcome{Answer: "ok"})

	state, err := s.Cancel(id)
	if err != nil {
		t.Fatalf("cancelling a finished run should not error: %v", err)
	}
	if state != Done {
		t.Fatalf("state = %q, want done", state)
	}
}

func TestAppendIgnoredAfterTerminal(t *testing.T) {
	s := New()
	id := s.Create("codex", "p", nil)
	if _, err := s.Start(id); err != nil {
		t.Fatal(err)
	}
	s.Append(id, "message", "during")
	s.Finish(id, Outcome{Answer: "ok"})
	s.Append(id, "message", "after")

	snap, _ := s.Get(id)
	if len(snap.Events) != 1 || snap.Events[0].Text != "during" {
		t.Fatalf("Events = %+v, want only the one from before the finish", snap.Events)
	}
}

func TestSnapshotEventsAreCopied(t *testing.T) {
	s := New()
	id := s.Create("codex", "p", nil)
	s.Append(id, "message", "original")

	snap, _ := s.Get(id)
	snap.Events[0].Text = "mutated"

	again, _ := s.Get(id)
	if again.Events[0].Text != "original" {
		t.Fatalf("store was mutated through a snapshot: %q", again.Events[0].Text)
	}
}

func TestUnknownIDIsAnError(t *testing.T) {
	s := New()
	if _, err := s.Get("nope"); !errors.As(err, &ErrNoSuchRun{}) {
		t.Fatalf("Get error = %v, want ErrNoSuchRun", err)
	}
	if _, err := s.Cancel("nope"); !errors.As(err, &ErrNoSuchRun{}) {
		t.Fatalf("Cancel error = %v, want ErrNoSuchRun", err)
	}
}

func TestListIsOldestFirst(t *testing.T) {
	s := New()
	a := s.Create("codex", "one", nil)
	b := s.Create("codex", "two", nil)
	got := s.List()
	if len(got) != 2 || got[0].ID != a || got[1].ID != b {
		t.Fatalf("List order = %+v", got)
	}
}

func TestCancelAllStopsOnlyLiveRuns(t *testing.T) {
	s := New()
	finished := s.Create("codex", "done", nil)
	if _, err := s.Start(finished); err != nil {
		t.Fatal(err)
	}
	s.Finish(finished, Outcome{Answer: "ok"})
	live := s.Create("codex", "live", nil)

	stopped := s.CancelAll()
	if len(stopped) != 1 || stopped[0] != live {
		t.Fatalf("CancelAll = %v, want just %q", stopped, live)
	}
	if snap, _ := s.Get(finished); snap.State != Done {
		t.Fatalf("finished run became %q", snap.State)
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s := New()
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = s.Create("codex", "p", func() {})
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(3)
		go func() { defer wg.Done(); _, _ = s.Start(id) }()
		go func() { defer wg.Done(); s.Append(id, "message", "x") }()
		go func() { defer wg.Done(); _, _ = s.Get(id) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.List() }()
	wg.Wait()
}
