package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLastAssistantTextReturnsMostRecent(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first reply"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"more"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second reply"}]}}
`
	got, err := LastAssistantText(writeTranscript(t, jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if got != "second reply" {
		t.Fatalf("LastAssistantText = %q, want 'second reply'", got)
	}
}

func TestLastAssistantTextConcatenatesBlocks(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"part one "},{"type":"text","text":"part two"}]}}
`
	got, _ := LastAssistantText(writeTranscript(t, jsonl))
	if got != "part one part two" {
		t.Fatalf("LastAssistantText = %q, want concatenated", got)
	}
}

func TestLastAssistantTextNoAssistant(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"only user"}]}}
`
	got, err := LastAssistantText(writeTranscript(t, jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("LastAssistantText = %q, want empty", got)
	}
}

func TestLastAssistantTextMissingFile(t *testing.T) {
	if _, err := LastAssistantText("/no/such/file.jsonl"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLastAssistantTextIgnoresBlankLines(t *testing.T) {
	jsonl := "\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n\n"
	got, _ := LastAssistantText(writeTranscript(t, jsonl))
	if got != "hello" {
		t.Fatalf("LastAssistantText = %q, want 'hello'", got)
	}
}

func TestTurnTextJoinsTheWholeTurn(t *testing.T) {
	// What a person who was not watching needs is the running commentary, not the
	// closing sentence — and the previous turn must not bleed into it.
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"first question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}`,
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"looking"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TurnText(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "looking\n\ndone" {
		t.Fatalf("TurnText = %q, want %q", got, "looking\n\ndone")
	}
}

func TestTurnTextEmptyPath(t *testing.T) {
	if got, err := TurnText(""); got != "" || err != nil {
		t.Fatalf("TurnText(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
}
