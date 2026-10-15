// Package tmux gives a caller thin access to the local tmux server: type into
// a pane, list sessions and windows, read and answer an agent's selection menu,
// and build an attach-or-create command for a persistent terminal session. tmux
// sessions outlive the process that drives them, so a terminal session backed
// by tmux survives its restarts — a client just reattaches.
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Available reports whether the tmux binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// SendText types literal text into a target pane (no trailing newline). Use
// SendKey for named keys / chords.
func SendText(pane, text string) error {
	if pane == "" {
		return fmt.Errorf("no pane")
	}
	return exec.Command("tmux", "send-keys", "-t", pane, "-l", text).Run()
}

// SendKey sends one tmux key name (e.g. "Enter", "Escape", "C-c", "BTab",
// "Up", "Down", "Left", "Right", "Tab") to a target pane.
func SendKey(pane, key string) error {
	if pane == "" {
		return fmt.Errorf("no pane")
	}
	return exec.Command("tmux", "send-keys", "-t", pane, key).Run()
}

// SessionForPane resolves the tmux session name that owns an opaque pane id
// (e.g. "%3", as reported by $TMUX_PANE in a hook). Empty pane, no tmux, or a
// pane that no longer exists → ("", error). The name it returns is exactly what
// the client attaches with (`tmux new-session -A -s <name>`), so every view of
// that session can share one PTY.
func SessionForPane(pane string) (string, error) {
	if pane == "" {
		return "", fmt.Errorf("no pane")
	}
	if !Available() {
		return "", fmt.Errorf("tmux not installed")
	}
	out, err := exec.Command("tmux", "display-message", "-t", pane, "-p", "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Kill terminates a tmux session by name.
func Kill(name string) error {
	if !validName(name) {
		return fmt.Errorf("invalid tmux session name %q", name)
	}
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}

// Session is one tmux session.
type Session struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Attached bool   `json:"attached"`
}

// List returns the local tmux server's sessions. When no server is running
// (nothing started yet) it returns an empty slice and no error.
func List() ([]Session, error) {
	if !Available() {
		return nil, fmt.Errorf("tmux not installed")
	}
	// Use an explicit delimiter, not "\t": under some launchd runtimes tmux's
	// "\t" in the -F format doesn't render as a real tab, breaking parsing.
	const sep = ":::"
	out, err := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}"+sep+"#{session_windows}"+sep+"#{session_attached}").Output()
	if err != nil {
		// No server running yet → tmux exits non-zero. Treat as empty, not error.
		return []Session{}, nil
	}
	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, sep)
		if len(parts) < 3 {
			continue
		}
		windows, _ := strconv.Atoi(parts[1])
		sessions = append(sessions, Session{
			Name:     parts[0],
			Windows:  windows,
			Attached: parts[2] == "1",
		})
	}
	return sessions, nil
}

// Window is one tmux window within a session (a client may map these 1:1 to tabs).
type Window struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ListWindows returns the windows of a tmux session, ordered by index.
func ListWindows(session string) ([]Window, error) {
	if !Available() {
		return nil, fmt.Errorf("tmux not installed")
	}
	if !validName(session) {
		return nil, fmt.Errorf("invalid tmux session name %q", session)
	}
	const sep = ":::"
	out, err := exec.Command("tmux", "list-windows", "-t", session, "-F",
		"#{window_index}"+sep+"#{window_name}"+sep+"#{window_active}").Output()
	if err != nil {
		return []Window{}, nil // no such session / no server → empty, not an error
	}
	var ws []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, sep)
		if len(parts) < 3 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		ws = append(ws, Window{Index: idx, Name: parts[1], Active: parts[2] == "1"})
	}
	return ws, nil
}

// SelectWindow makes window `index` the active one in the session (the attached
// client's PTY then repaints to it).
func SelectWindow(session string, index int) error {
	if !validName(session) {
		return fmt.Errorf("invalid tmux session name %q", session)
	}
	return exec.Command("tmux", "select-window", "-t", fmt.Sprintf("%s:%d", session, index)).Run()
}

// NewWindow creates a new window in the session and switches to it.
func NewWindow(session string) error {
	if !validName(session) {
		return fmt.Errorf("invalid tmux session name %q", session)
	}
	return exec.Command("tmux", "new-window", "-t", session).Run()
}

// KillWindow removes window `index` from the session.
func KillWindow(session string, index int) error {
	if !validName(session) {
		return fmt.Errorf("invalid tmux session name %q", session)
	}
	return exec.Command("tmux", "kill-window", "-t", fmt.Sprintf("%s:%d", session, index)).Run()
}

// validName limits session names to a safe charset so they can be embedded in
// a shell command without injection risk.
func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// AttachCmd returns the shell command that attaches to the named tmux session,
// creating it if absent (-A). Run inside a PTY it gives a persistent terminal.
//
// We do NOT pass `-d` (don't detach the desktop): with two clients repeatedly
// reattaching, `-d` makes them fight (each kicks the other) and the phone never
// gets a stable frame. tmux's default `window-size latest` sizes the window to
// the most-recently-active client, so the phone renders cleanly when it's in
// use (the PTY already starts at the phone's geometry, so no reflow ghosting).
func AttachCmd(name string) (string, error) {
	if !Available() {
		return "", fmt.Errorf("tmux not installed")
	}
	if !validName(name) {
		return "", fmt.Errorf("invalid tmux session name %q", name)
	}
	// `window-size smallest`: pin the window to the smallest attached client, so
	// the phone always renders at its own (small) geometry. With `latest`, any
	// leftover desktop/zombie client at a larger size would resize the window
	// bigger and garble the phone on scroll. Smallest = phone is always exact.
	// `-u` forces tmux to treat output as UTF-8 even when the caller's launchd
	// environment has no UTF-8 locale — otherwise CJK is mangled into tofu.
	return fmt.Sprintf("tmux -u set -g window-size smallest 2>/dev/null; exec tmux -u new-session -A -s %s", name), nil
}
