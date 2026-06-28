// Package hooks parses Claude Code / Codex hook payloads, reads Claude
// transcripts, and installs hook commands into the CLIs' config files. It is
// app-agnostic: transport (what the hook command actually does) is the caller's
// responsibility.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
)

// maxPayloadBytes bounds how much of a hook stdin payload is read.
const maxPayloadBytes = 8 << 20 // 8 MiB

// HookEvent is a parsed Claude Code / Codex hook payload.
type HookEvent struct {
	EventName      string // PreToolUse|PostToolUse|Notification|Stop|SubagentStop|UserPromptSubmit|SessionStart
	SessionID      string
	Cwd            string
	ToolName       string
	ToolInput      map[string]any
	Command        string // convenience: tool_input.command if present
	LastAssistant  string // last_assistant_message if present
	TranscriptPath string
	Raw            map[string]any
}

// ParsePayload reads a bounded JSON hook payload and normalizes it.
func ParsePayload(r io.Reader) (HookEvent, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPayloadBytes))
	if err != nil {
		return HookEvent{}, fmt.Errorf("hooks: reading payload: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return HookEvent{}, fmt.Errorf("hooks: parsing payload: %w", err)
	}

	ev := HookEvent{
		EventName:      firstString(raw, "hook_event_name", "event_name", "event"),
		SessionID:      str(raw, "session_id"),
		Cwd:            str(raw, "cwd"),
		ToolName:       str(raw, "tool_name"),
		LastAssistant:  str(raw, "last_assistant_message"),
		TranscriptPath: str(raw, "transcript_path"),
		Raw:            raw,
	}
	if ti, ok := raw["tool_input"].(map[string]any); ok {
		ev.ToolInput = ti
		ev.Command = str(ti, "command")
	}
	return ev, nil
}

// Summarize renders a short human-readable line for a hook event.
func Summarize(ev HookEvent) string {
	switch ev.EventName {
	case "PreToolUse":
		if ev.Command != "" {
			return fmt.Sprintf("→ %s: %s", ev.ToolName, ev.Command)
		}
		return fmt.Sprintf("→ %s", ev.ToolName)
	case "PostToolUse":
		return fmt.Sprintf("✓ %s", ev.ToolName)
	case "Notification":
		return fmt.Sprintf("🔔 %s", str(ev.Raw, "message"))
	case "UserPromptSubmit":
		return fmt.Sprintf("⌨ %s", str(ev.Raw, "prompt"))
	case "Stop", "SubagentStop":
		if ev.LastAssistant != "" {
			return fmt.Sprintf("■ %s", ev.LastAssistant)
		}
		return "■ " + ev.EventName
	case "SessionStart":
		return "● session start"
	default:
		return ev.EventName
	}
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := str(m, k); v != "" {
			return v
		}
	}
	return ""
}
