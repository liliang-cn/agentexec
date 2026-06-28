package cliagent

import (
	"encoding/json"
	"strings"
)

// mapJSONLines routes each line to mapper if it is a JSON object, otherwise
// emits it as a terminal-output event keyed "line". Blank lines are skipped.
// Lines that do not begin with '{' are treated as terminal output (matching the
// real CLI wrappers, which only attempt to parse object-leading lines).
func mapJSONLines(lines []string, mapper func(map[string]any) []Event) []Event {
	if len(lines) == 0 {
		return nil
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != '{' {
			out = append(out, Event{Type: EventTerminalOutput, Payload: map[string]any{"line": line}})
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
			out = append(out, Event{Type: EventTerminalOutput, Payload: map[string]any{"line": line}})
			continue
		}
		out = append(out, mapper(obj)...)
	}
	return out
}
