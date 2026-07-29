package hooks

import (
	"encoding/json"
	"os"
	"strings"
)

// LastAssistantText reverse-scans a Claude Code JSONL transcript and returns the
// concatenated text of the most recent assistant message. Returns "" if none.
func LastAssistantText(transcriptPath string) (string, error) {
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		msg, ok := rec["message"].(map[string]any)
		if !ok {
			continue
		}
		if rec["type"] != "assistant" && msg["role"] != "assistant" {
			continue
		}
		if text := assistantText(msg); text != "" {
			return text, nil
		}
	}
	return "", nil
}

// TurnText returns EVERY assistant text block since the last prompt the human
// actually typed, joined in order — the model's whole running commentary for
// this turn, not just its closing sentence.
//
// LastAssistantText is the wrong thing to show a person who was not watching:
// an agent that narrates as it works ends on "done", and the reasoning that
// makes "done" meaningful is in the messages before it. User lines that only
// carry tool_result blocks do not count as prompts — they are the transcript's
// plumbing, not something anyone typed.
func TurnText(transcriptPath string) (string, error) {
	if transcriptPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	for i := len(lines) - 1; i >= 0; i-- {
		var rec map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &rec) != nil {
			continue
		}
		if rec["type"] != "user" {
			continue
		}
		if msg, _ := rec["message"].(map[string]any); typedByHuman(msg) {
			start = i + 1
			break
		}
	}
	var parts []string
	for i := start; i < len(lines); i++ {
		var rec map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &rec) != nil {
			continue
		}
		msg, ok := rec["message"].(map[string]any)
		if !ok || (rec["type"] != "assistant" && msg["role"] != "assistant") {
			continue
		}
		if t := assistantText(msg); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// typedByHuman is true for a user line with real content, false for one that
// only carries tool results.
func typedByHuman(msg map[string]any) bool {
	if msg == nil {
		return false
	}
	switch c := msg["content"].(type) {
	case string:
		return strings.TrimSpace(c) != ""
	case []any:
		for _, item := range c {
			if b, ok := item.(map[string]any); ok && b["type"] == "text" {
				return true
			}
		}
	}
	return false
}

func assistantText(msg map[string]any) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		if t, ok := block["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}
