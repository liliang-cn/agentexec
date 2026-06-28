package hooks

import (
	"os"
	"path/filepath"
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
