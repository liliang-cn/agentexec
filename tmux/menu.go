package tmux

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// MenuChoice is a parsed interactive selection menu (e.g. Claude Code's
// "Do you want to proceed?" permission prompt) read from a tmux pane.
type MenuChoice struct {
	Question string
	Options  []string
	Selected int // index of the ❯-marked option, or -1 if none detected
}

// statusRe matches Claude Code's working status line, e.g.
// "✽ Julienning… (28s · still thinking)" — the "(Ns …)" timer is the tell.
var statusRe = regexp.MustCompile(`\(\d+s[^)]*\)`)

// StatusLine extracts the agent's live status line from captured pane text
// (the spinner word + elapsed timer). Returns "" when the agent is idle.
func StatusLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(strings.Trim(lines[i], borders))
		if s == "" {
			continue
		}
		if statusRe.MatchString(s) {
			return s
		}
	}
	return ""
}

// CapturePane returns the visible text of a pane.
func CapturePane(pane string) (string, error) {
	if pane == "" {
		return "", fmt.Errorf("no pane")
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	return string(out), err
}

// A menu option line (after border stripping): optional ❯/> cursor, a number,
// a separator, then the label.
var optionRe = regexp.MustCompile(`^([❯>›])?\s*(\d+)[.)]\s+(.+\S)\s*$`)

// borders are the box-drawing / padding characters tmux panes wrap menus in.
const borders = " \t│┃|╭╮╰╯─┌┐└┘"

// ParseMenu extracts the numbered selection block (1, 2, 3, …) nearest the
// bottom of the captured text. Returns ok=false if no such menu is present.
func ParseMenu(text string) (MenuChoice, bool) {
	lines := strings.Split(text, "\n")
	clean := make([]string, len(lines))
	for i, ln := range lines {
		clean[i] = strings.Trim(ln, borders)
	}
	type opt struct {
		line   int
		marked bool
		num    int
		label  string
	}
	var opts []opt
	for i, ln := range clean {
		m := optionRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		opts = append(opts, opt{line: i, marked: m[1] != "", num: n, label: m[3]})
	}
	// Build the last contiguous 1..k run (handles a stray earlier number).
	start := -1
	for i, o := range opts {
		if o.num == 1 {
			start = i
		}
	}
	if start < 0 {
		return MenuChoice{}, false
	}
	block := []opt{opts[start]}
	expect := 2
	for i := start + 1; i < len(opts); i++ {
		if opts[i].num == expect {
			block = append(block, opts[i])
			expect++
		}
	}
	if len(block) < 2 {
		return MenuChoice{}, false
	}

	mc := MenuChoice{Selected: -1}
	for i, o := range block {
		mc.Options = append(mc.Options, o.label)
		if o.marked {
			mc.Selected = i
		}
	}
	// Question = nearest non-empty, non-option line above the block.
	for i := block[0].line - 1; i >= 0; i-- {
		s := clean[i]
		if s == "" || optionRe.MatchString(s) {
			continue
		}
		mc.Question = s
		break
	}
	if mc.Question == "" {
		mc.Question = "Choose an option"
	}
	return mc, true
}

// Choose selects the option at index (0-based) in the pane's live menu by
// re-capturing to find the current cursor, navigating with Up/Down, then Enter.
// Falls back to pressing the option number if the cursor can't be located.
func Choose(pane string, index int) error {
	if pane == "" {
		return fmt.Errorf("no pane")
	}
	text, err := CapturePane(pane)
	if err != nil {
		return err
	}
	mc, ok := ParseMenu(text)
	if !ok || index < 0 || index >= len(mc.Options) {
		_ = SendKey(pane, strconv.Itoa(index+1))
		return SendKey(pane, "Enter")
	}
	sel := max(mc.Selected, 0)
	delta := index - sel
	key := "Down"
	if delta < 0 {
		key = "Up"
		delta = -delta
	}
	for i := 0; i < delta; i++ {
		_ = SendKey(pane, key)
	}
	return SendKey(pane, "Enter")
}
