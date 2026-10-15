// Package iterm types into a terminal tab from outside, the same way you would
// at the keyboard.
//
// It exists because an agent running in a plain terminal is otherwise
// unreachable. A Stop hook can only hand instructions back at the instant a
// turn ends — useless while the agent sits idle, waits at a permission prompt,
// or has crashed — and holding that hook open to listen freezes the terminal
// for the person sitting at it. Writing to the tab has none of those problems:
// it works whatever the agent is doing, because it is indistinguishable from
// typing.
//
// A session is addressed by its tty (e.g. /dev/ttys003), which the hook reports
// and iTerm2 exposes per session. tmux gets the same treatment through
// internal/tmux; this is the equivalent for people who do not use tmux.
package iterm

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// scriptTimeout bounds an AppleScript call. iTerm2 normally answers instantly;
// a hang means it is busy or blocked on a dialog, and waiting longer would not
// help.
const scriptTimeout = 10 * time.Second

// Available reports whether iTerm2 is running and scriptable. Sending to a
// stopped iTerm2 would launch it, which is never what the caller wants.
func Available(ctx context.Context) bool {
	out, err := run(ctx, `tell application "System Events" to return (exists (processes where name is "iTerm2"))`)
	return err == nil && strings.TrimSpace(out) == "true"
}

// HasSession reports whether a tab with this tty exists.
func HasSession(ctx context.Context, tty string) bool {
	if tty == "" || !Available(ctx) {
		return false
	}
	out, err := run(ctx, findScript(tty, `return "yes"`))
	return err == nil && strings.TrimSpace(out) == "yes"
}

// SendText types text into the tab owning tty, then presses Return — exactly
// what a person would do. The agent picks it up as ordinary input, whatever
// state it is in.
func SendText(ctx context.Context, tty, text string) error {
	if tty == "" {
		return fmt.Errorf("iterm: no tty for this session")
	}
	if !Available(ctx) {
		return fmt.Errorf("iterm: iTerm2 is not running")
	}
	// `write text` submits the line, which is why no separate Return is needed.
	body := fmt.Sprintf(`write text %s
			return "sent"`, quote(text))
	out, err := run(ctx, findScript(tty, body))
	if err != nil {
		return fmt.Errorf("iterm: %w", err)
	}
	if strings.TrimSpace(out) != "sent" {
		return fmt.Errorf("iterm: no tab is attached to %s", tty)
	}
	return nil
}

// Screen returns what is currently visible in the tab owning tty.
//
// Some of what a person needs to know never reaches a hook: Claude Code prints
// "Waiting for API response · will retry in 2m 35s · check your network" on
// screen and fires no event at all, and a twenty-minute build looks, from the
// event feed, exactly like a crash. The screen is where that difference is
// written, and a tab addressed by tty can simply be read.
func Screen(ctx context.Context, tty string) (string, error) {
	if tty == "" {
		return "", fmt.Errorf("iterm: no tty for this session")
	}
	if !Available(ctx) {
		return "", fmt.Errorf("iterm: iTerm2 is not running")
	}
	out, err := run(ctx, findScript(tty, "return contents"))
	if err != nil {
		return "", fmt.Errorf("iterm: %w", err)
	}
	return out, nil
}

// Open starts a new tab in dir and runs command in it, returning the tty of the
// new session so it can be addressed afterwards.
//
// No `activate`: creating the tab does not need iTerm2 in front, and stealing
// focus from whoever is at the keyboard to serve a request made from a phone is
// the wrong trade.
func Open(ctx context.Context, dir, command string) (string, error) {
	if !Available(ctx) {
		return "", fmt.Errorf("iterm: iTerm2 is not running")
	}
	// A brand-new iTerm2 can have no window at all, and `current window` is then
	// missing rather than empty.
	script := fmt.Sprintf(`tell application "iTerm2"
	if (count of windows) is 0 then
		set s to (current session of (create window with default profile))
	else
		tell current window
			set s to (current session of (create tab with default profile))
		end tell
	end if
	tell s
		write text %s
		write text %s
	end tell
	return tty of s
end tell`, quote("cd "+ShellQuote(dir)), quote(command))
	out, err := run(ctx, script)
	if err != nil {
		return "", fmt.Errorf("iterm: %w", err)
	}
	tty := strings.TrimSpace(out)
	if !strings.HasPrefix(tty, "/dev/") {
		return "", fmt.Errorf("iterm: opened a tab but got no tty back (%q)", tty)
	}
	return tty, nil
}

// ShellQuote wraps s as a single shell argument. Everything sent to a terminal
// this way originates in a chat message, so it must reach the command as one
// literal argument and never as syntax.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SendKeystroke delivers a raw key sequence (e.g. "\r" for Return, "\x1b" for
// Escape) without submitting a line, for answering prompts that read single
// keys rather than lines.
func SendKeystroke(ctx context.Context, tty, keys string) error {
	if tty == "" {
		return fmt.Errorf("iterm: no tty for this session")
	}
	if !Available(ctx) {
		return fmt.Errorf("iterm: iTerm2 is not running")
	}
	body := fmt.Sprintf(`write text %s newline NO
			return "sent"`, quote(keys))
	out, err := run(ctx, findScript(tty, body))
	if err != nil {
		return fmt.Errorf("iterm: %w", err)
	}
	if strings.TrimSpace(out) != "sent" {
		return fmt.Errorf("iterm: no tab is attached to %s", tty)
	}
	return nil
}

// findScript wraps body in a walk over every iTerm2 session, running it against
// the one whose tty matches. Returns "" when there is no such tab.
func findScript(tty, body string) string {
	return fmt.Sprintf(`tell application "iTerm2"
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if (tty of s) is %s then
					tell s
						%s
					end tell
				end if
			end repeat
		end repeat
	end repeat
	return ""
end tell`, quote(tty), body)
}

func run(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// quote renders a Go string as an AppleScript literal. AppleScript has no
// escape for a newline inside a literal, so text is split and rejoined with
// `linefeed`, and backslashes/quotes are escaped.
func quote(s string) string {
	if s == "" {
		return `""`
	}
	parts := strings.Split(s, "\n")
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ReplaceAll(p, `\`, `\\`)
		p = strings.ReplaceAll(p, `"`, `\"`)
		quoted = append(quoted, `"`+p+`"`)
	}
	return strings.Join(quoted, " & linefeed & ")
}
