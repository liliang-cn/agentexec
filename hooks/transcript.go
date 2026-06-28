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
