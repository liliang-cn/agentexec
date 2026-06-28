package cliagent

import "testing"

func TestMapJSONLinesDispatchesObjects(t *testing.T) {
	mapper := func(obj map[string]any) []Event {
		return []Event{{Type: EventAgentMessage, Payload: map[string]any{"role": obj["role"]}}}
	}
	events := mapJSONLines([]string{`{"role":"assistant"}`}, mapper)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventAgentMessage || events[0].Payload["role"] != "assistant" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestMapJSONLinesNonJSONBecomesTerminalOutput(t *testing.T) {
	mapper := func(map[string]any) []Event { return nil }
	events := mapJSONLines([]string{"plain log line"}, mapper)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTerminalOutput {
		t.Fatalf("type = %q, want %q", events[0].Type, EventTerminalOutput)
	}
	if events[0].Payload["text"] != "plain log line" {
		t.Fatalf("text = %v, want 'plain log line'", events[0].Payload["text"])
	}
}

func TestMapJSONLinesSkipsBlankLines(t *testing.T) {
	mapper := func(map[string]any) []Event { return nil }
	events := mapJSONLines([]string{"", "   "}, mapper)
	if len(events) != 0 {
		t.Fatalf("blank lines should produce no events, got %v", events)
	}
}

func TestMapJSONLinesJSONArrayIsTerminalOutput(t *testing.T) {
	// A JSON array is valid JSON but not an object; treat as terminal output.
	mapper := func(map[string]any) []Event { return nil }
	events := mapJSONLines([]string{`[1,2,3]`}, mapper)
	if len(events) != 1 || events[0].Type != EventTerminalOutput {
		t.Fatalf("array line should be terminal output, got %v", events)
	}
}

func TestMapJSONLinesPreservesOrder(t *testing.T) {
	mapper := func(obj map[string]any) []Event {
		return []Event{{Type: EventAgentMessage, Payload: obj}}
	}
	events := mapJSONLines([]string{`{"n":1}`, "log", `{"n":2}`}, mapper)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Type != EventAgentMessage || events[1].Type != EventTerminalOutput || events[2].Type != EventAgentMessage {
		t.Fatalf("order not preserved: %+v", events)
	}
}
