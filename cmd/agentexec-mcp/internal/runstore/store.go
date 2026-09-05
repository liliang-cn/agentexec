// Package runstore holds the state of every delegated run and hands out
// snapshots of it. It is the only place in the server that locks, and it knows
// nothing about MCP or about how a run is actually executed.
package runstore

import (
	"fmt"
	"sync"
	"time"
)

// State is where a run has got to. queued and running are live; done, failed
// and cancelled are terminal and never change again.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Terminal reports whether s is an end state.
func (s State) Terminal() bool {
	return s == Done || s == Failed || s == Cancelled
}

// Event is one thing the delegate did, kept so a caller arriving late can still
// see how the run got where it did.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// Usage is the token and cost accounting for a finished run. It mirrors the
// library's Usage rather than importing it, so this package stays dependency
// free.
type Usage struct {
	Model            string  `json:"model,omitempty"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheTokens      int64   `json:"cache_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// Outcome is what a run finished with.
type Outcome struct {
	Answer    string
	SessionID string
	Usage     Usage
	ExitCode  int
	// Failed is the provider's own verdict, which is not the exit code: a
	// claude whose token was revoked says so and exits zero.
	Failed bool
	// Err is set when the run could not be carried out at all, as opposed to
	// the delegate running and reporting a failure.
	Err string
}

// Snapshot is an immutable view of a run. Every accessor returns one of these
// rather than a pointer into the store.
type Snapshot struct {
	ID        string    `json:"run_id"`
	Provider  string    `json:"provider"`
	Prompt    string    `json:"prompt"`
	State     State     `json:"state"`
	QueuedAt  time.Time `json:"queued_at"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Events    []Event   `json:"events,omitempty"`
	Answer    string    `json:"answer,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Usage     Usage     `json:"usage"`
	ExitCode  int       `json:"exit_code"`
	Failed    bool      `json:"failed"`
	Err       string    `json:"error,omitempty"`
}

type run struct {
	Snapshot
	cancel func()
}

// Store keeps every run of this server's lifetime, in creation order.
type Store struct {
	mu    sync.Mutex
	runs  map[string]*run
	order []string
	seq   int
	now   func() time.Time
}

// New returns an empty Store.
func New() *Store {
	return &Store{runs: make(map[string]*run), now: time.Now}
}

// ErrNoSuchRun is returned for an id the store has never issued.
type ErrNoSuchRun struct{ ID string }

func (e ErrNoSuchRun) Error() string { return fmt.Sprintf("no such run %q", e.ID) }

// Create records a queued run and returns its id. cancel is called by Cancel to
// stop the work; it may be nil for a run that has nothing to stop yet.
func (s *Store) Create(provider, prompt string, cancel func()) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("r%d", s.seq)
	s.runs[id] = &run{
		Snapshot: Snapshot{
			ID:       id,
			Provider: provider,
			Prompt:   prompt,
			State:    Queued,
			QueuedAt: s.now(),
		},
		cancel: cancel,
	}
	s.order = append(s.order, id)
	return id
}

// Start moves a queued run to running. A run already cancelled off the queue
// stays cancelled and false is returned, which is how a worker learns not to
// bother starting it.
func (s *Store) Start(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return false, ErrNoSuchRun{id}
	}
	if r.State != Queued {
		return false, nil
	}
	r.State = Running
	r.StartedAt = s.now()
	return true, nil
}

// Append records something the delegate did. It is ignored once the run has
// finished, so a straggling event cannot reopen a closed run.
func (s *Store) Append(id, kind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok || r.State.Terminal() {
		return
	}
	r.Events = append(r.Events, Event{At: s.now(), Kind: kind, Text: text})
}

// Finish records the outcome and moves the run to done or failed. A cancelled
// run keeps that state: what the delegate managed to say on its way out does
// not change why it stopped.
func (s *Store) Finish(id string, out Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return
	}
	r.Answer, r.SessionID, r.Usage = out.Answer, out.SessionID, out.Usage
	r.ExitCode, r.Failed, r.Err = out.ExitCode, out.Failed, out.Err
	if r.State == Cancelled {
		return
	}
	r.EndedAt = s.now()
	if out.Err != "" || out.Failed || out.ExitCode != 0 {
		r.State = Failed
		return
	}
	r.State = Done
}

// Cancel stops a live run and reports the state it settled in. Cancelling a
// finished run is not an error; it reports what the run already was.
func (s *Store) Cancel(id string) (State, error) {
	s.mu.Lock()
	r, ok := s.runs[id]
	if !ok {
		s.mu.Unlock()
		return "", ErrNoSuchRun{id}
	}
	if r.State.Terminal() {
		state := r.State
		s.mu.Unlock()
		return state, nil
	}
	r.State = Cancelled
	r.EndedAt = s.now()
	cancel := r.cancel
	s.mu.Unlock()

	// Outside the lock: cancelling kills a process, and nothing about that
	// needs to hold up a reader asking for a snapshot.
	if cancel != nil {
		cancel()
	}
	return Cancelled, nil
}

// Get returns a snapshot of one run.
func (s *Store) Get(id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Snapshot{}, ErrNoSuchRun{id}
	}
	return r.snapshot(), nil
}

// List returns every run, oldest first.
func (s *Store) List() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Snapshot, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.runs[id].snapshot())
	}
	return out
}

// CancelAll stops every live run, for shutdown. It returns the ids it stopped.
func (s *Store) CancelAll() []string {
	s.mu.Lock()
	live := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if !s.runs[id].State.Terminal() {
			live = append(live, id)
		}
	}
	s.mu.Unlock()

	stopped := make([]string, 0, len(live))
	for _, id := range live {
		if _, err := s.Cancel(id); err == nil {
			stopped = append(stopped, id)
		}
	}
	return stopped
}

// snapshot copies the run's events so a caller cannot mutate the store's slice
// through the returned Snapshot. Callers must hold s.mu.
func (r *run) snapshot() Snapshot {
	s := r.Snapshot
	s.Events = append([]Event(nil), r.Events...)
	return s
}
