package cliagent

import "testing"

func TestMapJSONLinesDispatchesObjects(t *testing.T) {
	mapper := func(o map[string]any) []Event {
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": o["role"]}}}
	}
	ev := mapJSONLines([]string{`{"role":"assistant"}`}, mapper)
	if len(ev) != 1 || ev[0].Payload["role"] != "assistant" {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesNonJSONUsesLineKey(t *testing.T) {
	ev := mapJSONLines([]string{"plain log"}, func(map[string]any) []Event { return nil })
	if len(ev) != 1 || ev[0].Type != EventTerminalOutput || ev[0].Payload["line"] != "plain log" {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesSkipsBlank(t *testing.T) {
	if ev := mapJSONLines([]string{""}, func(map[string]any) []Event { return nil }); len(ev) != 0 {
		t.Fatalf("ev = %v", ev)
	}
}

func TestMapJSONLinesArrayLeadingTreatedAsLine(t *testing.T) {
	// A '['-leading line is not an object; emit as terminal output.
	ev := mapJSONLines([]string{`[1,2]`}, func(map[string]any) []Event { return nil })
	if len(ev) != 1 || ev[0].Type != EventTerminalOutput || ev[0].Payload["line"] != "[1,2]" {
		t.Fatalf("ev = %v", ev)
	}
}
