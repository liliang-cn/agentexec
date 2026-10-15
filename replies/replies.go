// Package replies holds answers that arrive for a terminal-run agent session
// while it is finishing a turn, so they can be injected without tmux.
//
// Controlling a terminal agent used to require tmux: the app typed into the
// pane. An agent started in a plain terminal reported events but could not be
// answered. Claude Code's Stop hook closes that gap — a Stop hook may block the
// turn and hand the agent new instructions — but only during the brief moment
// the hook is running. This queue bridges the two: replies from any source (the
// app, a chat channel) are parked here, and the Stop hook drains them.
package replies

import (
	"context"
	"sync"
	"time"
)

// Queue routes reply text to whoever is waiting for a given session.
type Queue struct {
	mu      sync.Mutex
	pending map[string][]string      // session id -> replies nobody has taken yet
	waiters map[string][]chan string // session id -> hooks currently blocked
}

// New creates an empty queue.
func New() *Queue {
	return &Queue{
		pending: make(map[string][]string),
		waiters: make(map[string][]chan string),
	}
}

// Push delivers text to a waiting hook, or parks it for the next one. It never
// blocks. Empty session ids and empty text are ignored.
func (q *Queue) Push(sessionID, text string) {
	if sessionID == "" || text == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.waiters[sessionID]) > 0 {
		ch := q.waiters[sessionID][0]
		q.waiters[sessionID] = q.waiters[sessionID][1:]
		select {
		case ch <- text:
			return // handed to a live waiter
		default:
			// Waiter already timed out; drop it and try the next one.
		}
	}
	q.pending[sessionID] = append(q.pending[sessionID], text)
}

// TryTake returns an already-parked reply for the session without blocking, or
// "" if there is none. Every Stop hook drains this, so a reply sent while the
// agent was busy is applied at the end of the turn without anyone waiting.
func (q *Queue) TryTake(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.takeLocked(sessionID)
}

// takeLocked pops the oldest parked reply. Callers must hold q.mu.
func (q *Queue) takeLocked(sessionID string) string {
	queued := q.pending[sessionID]
	if len(queued) == 0 {
		return ""
	}
	text := queued[0]
	if len(queued) == 1 {
		delete(q.pending, sessionID)
	} else {
		q.pending[sessionID] = queued[1:]
	}
	return text
}

// Wait returns a reply for the session: an already-parked one immediately, or
// the next one to arrive within timeout. It returns "" if none shows up, the
// context is cancelled, or timeout is not positive.
func (q *Queue) Wait(ctx context.Context, sessionID string, timeout time.Duration) string {
	if sessionID == "" || timeout <= 0 {
		return ""
	}

	q.mu.Lock()
	if text := q.takeLocked(sessionID); text != "" {
		q.mu.Unlock()
		return text
	}
	ch := make(chan string, 1)
	q.waiters[sessionID] = append(q.waiters[sessionID], ch)
	q.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case text := <-ch:
		return text
	case <-timer.C:
	case <-ctx.Done():
	}

	// Timed out: stop holding a slot. A racing Push may still have filled the
	// channel, in which case return that rather than losing the reply.
	q.mu.Lock()
	for i, w := range q.waiters[sessionID] {
		if w == ch {
			q.waiters[sessionID] = append(q.waiters[sessionID][:i], q.waiters[sessionID][i+1:]...)
			break
		}
	}
	if len(q.waiters[sessionID]) == 0 {
		delete(q.waiters, sessionID)
	}
	q.mu.Unlock()

	select {
	case text := <-ch:
		return text
	default:
		return ""
	}
}

// Waiting reports whether a hook is currently blocked on this session. Used to
// tell a caller whether a terminal session can be answered right now.
func (q *Queue) Waiting(sessionID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.waiters[sessionID]) > 0
}
