// Package reach decides whether a terminal-run agent session can still be
// typed into, and through which terminal.
package reach

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Sighting is one report of a session and the terminal it was attached to, as
// a hook saw it. Only the order of sightings matters, not their times.
type Sighting struct {
	SessionID string
	TTY       string
}

// Whether a terminal-run agent can still be reached is not a question the event
// feed answers on its own. A session reports the terminal it is attached to, but
// that report is a fact about the past: the tab can be closed, the agent can
// exit and leave a shell behind, and macOS hands the freed tty number straight
// to the next tab that opens.
//
// Typing into a stale tty is worse than not delivering at all — the text lands
// in whatever now occupies that tab. So delivery is gated on two checks that
// both have to pass, and when they don't the caller refuses rather than parking
// the text in a queue nothing will ever read.

// CurrentTTY returns the terminal a session owns right now.
//
// known is false when the feed says nothing about this session's terminal (an
// app-launched run, or a session too old to still be in the window) — that is
// "no evidence", not "gone". known is true with an empty tty when the session
// did report a terminal and another session has since reported the same one:
// the tab it was in belongs to somebody else now.
//
// sightings must be oldest→newest.
func CurrentTTY(sightings []Sighting, sessionID string) (tty string, known bool) {
	owner := map[string]string{} // tty -> the session that last reported it
	for _, ev := range sightings {
		if ev.TTY == "" {
			continue
		}
		owner[ev.TTY] = ev.SessionID
		if ev.SessionID == sessionID {
			tty = ev.TTY
		}
	}
	if tty == "" {
		return "", false
	}
	if owner[tty] != sessionID {
		return "", true
	}
	return tty, true
}

// agentCommands are the process names that count as a coding agent sitting at a
// terminal, ready to read what we type. A tab showing a shell prompt is not one:
// text sent there would be run as a shell command.
var agentCommands = map[string]bool{
	"claude": true,
	"codex":  true,
	"gemini": true,
	"qwen":   true,
}

// AgentTTYs returns the terminals that currently have a coding agent attached.
// One ps call answers for every session at once, which matters because the
// session list is rebuilt on every poll.
func AgentTTYs() map[string]bool {
	out, err := exec.Command("ps", "-eo", "tty=,comm=").Output()
	if err != nil {
		return nil
	}
	live := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, comm := fields[0], filepath.Base(fields[len(fields)-1])
		if name == "??" || name == "-" {
			continue // a daemon, not something sitting in a tab
		}
		if agentCommands[comm] {
			live["/dev/"+name] = true
		}
	}
	return live
}

// Reach describes how (and whether) an instruction can get to a session.
type Reach struct {
	TTY    string // terminal to type into; empty when there is none to type into
	Gone   bool   // proven unreachable: don't queue, tell the user
	Reason string // why, in words meant for the person who asked
}

// For decides how to deliver to a session, given the recent event feed and the
// set of terminals that currently hold an agent.
func For(sightings []Sighting, live map[string]bool, sessionID string) Reach {
	tty, known := CurrentTTY(sightings, sessionID)
	switch {
	case !known:
		// Nothing on record. Not proof of anything, so fall through to the queue.
		return Reach{}
	case tty == "":
		return Reach{Gone: true, Reason: "its terminal tab is gone — another session has taken over the one it was in"}
	case live == nil:
		// The process list was unreadable; trust the tty rather than refuse.
		return Reach{TTY: tty}
	case !live[tty]:
		return Reach{Gone: true, Reason: "nothing is running in its terminal any more — the tab was closed, or the agent exited"}
	}
	return Reach{TTY: tty}
}
