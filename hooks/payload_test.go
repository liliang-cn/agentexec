package hooks

import (
	"strings"
	"testing"
)

func TestParsePayloadPreToolUse(t *testing.T) {
	body := `{
		"hook_event_name": "PreToolUse",
		"session_id": "sess-1",
		"cwd": "/work",
		"transcript_path": "/tmp/t.jsonl",
		"tool_name": "Bash",
		"tool_input": {"command": "ls -la"}
	}`
	ev, err := ParsePayload(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.EventName != "PreToolUse" {
		t.Fatalf("EventName = %q", ev.EventName)
	}
	if ev.SessionID != "sess-1" || ev.Cwd != "/work" || ev.TranscriptPath != "/tmp/t.jsonl" {
		t.Fatalf("unexpected fields: %+v", ev)
	}
	if ev.ToolName != "Bash" {
		t.Fatalf("ToolName = %q, want Bash", ev.ToolName)
	}
	if ev.Command != "ls -la" {
		t.Fatalf("Command = %q, want 'ls -la'", ev.Command)
	}
	if ev.Raw == nil {
		t.Fatal("Raw should be populated")
	}
}

func TestParsePayloadLastAssistant(t *testing.T) {
	body := `{"hook_event_name":"Stop","last_assistant_message":"all done"}`
	ev, err := ParsePayload(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.LastAssistant != "all done" {
		t.Fatalf("LastAssistant = %q, want 'all done'", ev.LastAssistant)
	}
}

func TestParsePayloadCodexEventNameAlias(t *testing.T) {
	// Codex uses "event_name" rather than "hook_event_name".
	body := `{"event_name":"PostToolUse","tool_name":"shell"}`
	ev, err := ParsePayload(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.EventName != "PostToolUse" {
		t.Fatalf("EventName = %q, want PostToolUse", ev.EventName)
	}
}

func TestParsePayloadBadJSON(t *testing.T) {
	if _, err := ParsePayload(strings.NewReader("{not json")); err == nil {
		t.Fatal("expected error on bad JSON")
	}
}

func TestSummarizeIncludesToolAndCommand(t *testing.T) {
	ev := HookEvent{EventName: "PreToolUse", ToolName: "Bash", Command: "rm -rf /tmp/x"}
	s := Summarize(ev)
	if !strings.Contains(s, "Bash") || !strings.Contains(s, "rm -rf /tmp/x") {
		t.Fatalf("Summarize = %q, want tool + command", s)
	}
}

func TestSummarizeStopUsesLastAssistant(t *testing.T) {
	ev := HookEvent{EventName: "Stop", LastAssistant: "final words"}
	if !strings.Contains(Summarize(ev), "final words") {
		t.Fatalf("Summarize = %q, want last assistant text", Summarize(ev))
	}
}

func TestSummarizeUnknownEvent(t *testing.T) {
	ev := HookEvent{EventName: "Mystery"}
	if !strings.Contains(Summarize(ev), "Mystery") {
		t.Fatalf("Summarize = %q, want event name", Summarize(ev))
	}
}
