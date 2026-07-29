package hooks

import "testing"

func TestCommandTokenAcceptsCommands(t *testing.T) {
	cases := map[string]string{
		"/compact":                    "/compact",
		"/commit 修一下 fd leak":         "/commit",
		"  /cortexdb:cortexdb 存!":     "/cortexdb:cortexdb",
		"/mcp__claude_design__design": "/mcp__claude_design__design",
		"/release\n":                  "/release",
	}
	for in, want := range cases {
		if got := CommandToken(in); got != want {
			t.Errorf("CommandToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandTokenRejectsNonCommands(t *testing.T) {
	// A pasted path also starts with a slash; treating it as a command would
	// report an ordinary prompt as one.
	for _, in := range []string{
		"/Users/liliang/Things/AI/base/roma look at this dir",
		"just a prompt",
		"/",
		"//",
		"/说中文", // command names are ASCII; a leading slash alone is not enough
		"",
	} {
		if got := CommandToken(in); got != "" {
			t.Errorf("CommandToken(%q) = %q, want \"\"", in, got)
		}
	}
}

func TestSlashCommandFromPrompt(t *testing.T) {
	ev := HookEvent{EventName: "UserPromptSubmit", Raw: map[string]any{"prompt": "/commit only my files"}}
	if got := SlashCommand(ev); got != "/commit" {
		t.Fatalf("SlashCommand = %q, want /commit", got)
	}
}

func TestSlashCommandOnlyManualCompact(t *testing.T) {
	// Auto-compaction fires the same hook and is not something the human ran.
	manual := HookEvent{EventName: "PreCompact", Raw: map[string]any{"trigger": "manual"}}
	if got := SlashCommand(manual); got != "/compact" {
		t.Fatalf("manual PreCompact = %q, want /compact", got)
	}
	auto := HookEvent{EventName: "PreCompact", Raw: map[string]any{"trigger": "auto"}}
	if got := SlashCommand(auto); got != "" {
		t.Fatalf("auto PreCompact = %q, want \"\"", got)
	}
}

func TestSlashCommandOnlyClearSessionEnd(t *testing.T) {
	// SessionEnd fires on every exit; only /clear is a command.
	clear := HookEvent{EventName: "SessionEnd", Raw: map[string]any{"reason": "clear"}}
	if got := SlashCommand(clear); got != "/clear" {
		t.Fatalf("SessionEnd(clear) = %q, want /clear", got)
	}
	for _, reason := range []string{"logout", "prompt_input_exit", "other"} {
		ev := HookEvent{EventName: "SessionEnd", Raw: map[string]any{"reason": reason}}
		if got := SlashCommand(ev); got != "" {
			t.Errorf("SessionEnd(%s) = %q, want \"\"", reason, got)
		}
	}
}

func TestSlashCommandFromTool(t *testing.T) {
	// The agent can run a command itself; report it after it ran, not before.
	ev := HookEvent{
		EventName: "PostToolUse",
		ToolName:  "SlashCommand",
		ToolInput: map[string]any{"command": "/review"},
	}
	if got := SlashCommand(ev); got != "/review" {
		t.Fatalf("PostToolUse = %q, want /review", got)
	}
	ev.EventName = "PreToolUse"
	if got := SlashCommand(ev); got != "" {
		t.Fatalf("PreToolUse = %q, want \"\" (reported on completion only)", got)
	}
	other := HookEvent{EventName: "PostToolUse", ToolName: "Bash", ToolInput: map[string]any{"command": "ls /tmp"}}
	if got := SlashCommand(other); got != "" {
		t.Fatalf("Bash PostToolUse = %q, want \"\"", got)
	}
}
