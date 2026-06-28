package cliagent

import (
	"encoding/json"
	"strings"
)

// mapJSONLines routes each line to mapper if it is a JSON object, otherwise
// emits it as a terminal-output event. Blank/whitespace-only lines are skipped.
func mapJSONLines(lines []string, mapper func(map[string]any) []Event) []Event {
	var events []Event
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil && obj != nil {
			events = append(events, mapper(obj)...)
			continue
		}
		events = append(events, Event{
			Type:    EventTerminalOutput,
			Payload: map[string]any{"text": line},
		})
	}
	return events
}
