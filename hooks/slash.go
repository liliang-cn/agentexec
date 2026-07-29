package hooks

import "strings"

// Slash commands are only partly visible through hooks, and the parts that are
// visible arrive through four different events. A command that starts an agent
// turn (a custom command in ~/.claude/commands) shows up as an ordinary prompt,
// while the built-in local ones — /compact, /clear — never reach the model at
// all and so never end a turn. Anything that wants to observe "the user ran a
// command" has to know all four shapes; this is that knowledge, kept here so
// each consumer does not rediscover it.
//
// Verified against Claude Code 2.1.220.

// SlashCommand returns the slash command a hook payload represents, or "" when
// it is not one. eventName may be empty, in which case ev.EventName is used.
//
// Recognised:
//
//	UserPromptSubmit  a prompt the human typed that begins with /
//	PreCompact        trigger "manual" — /compact (auto-compaction is not a command)
//	SessionEnd        reason "clear"   — /clear   (quit and logout are not)
//	PostToolUse       tool_name "SlashCommand" — the agent ran one itself
//
// PostToolUse rather than PreToolUse, so a caller reporting this can say the
// command ran rather than that it is about to.
func SlashCommand(ev HookEvent) string {
	switch ev.EventName {
	case "UserPromptSubmit":
		return CommandToken(str(ev.Raw, "prompt"))
	case "PreCompact":
		if str(ev.Raw, "trigger") == "manual" {
			return "/compact"
		}
	case "SessionEnd":
		if str(ev.Raw, "reason") == "clear" {
			return "/clear"
		}
	case "PostToolUse":
		if ev.ToolName == "SlashCommand" {
			return CommandToken(str(ev.ToolInput, "command"))
		}
	}
	return ""
}

// CommandToken pulls a leading "/name" off a prompt, or returns "" when there
// isn't one.
//
// A pasted absolute path ("/Users/liliang/...") also begins with a slash, so the
// name must contain nothing but the characters a command name can hold. Claude
// Code namespaces custom commands with a colon (/cortexdb:cortexdb) and plugin
// commands look like /mcp__server__tool, so ':' and '_' are part of the alphabet.
func CommandToken(prompt string) string {
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	name := fields[0][1:]
	if name == "" {
		return ""
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == ':', r == '.':
		default:
			return ""
		}
	}
	return fields[0]
}

// SessionStartSource reports why a session began: "startup", "resume", "clear"
// or "compact". Only the first two are a new agent appearing; the others are
// bookkeeping around a session that was already there.
func SessionStartSource(ev HookEvent) string { return str(ev.Raw, "source") }
