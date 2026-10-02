package agentexec

import (
	"maps"
	"regexp"
	"sort"
	"strings"
)

// mergeEnv combines the provider base env and the request env into a sorted
// "KEY=VALUE" slice. Empty base values are dropped; Request.Env overrides base.
func mergeEnv(base, reqEnv map[string]string) []string {
	merged := make(map[string]string, len(base)+len(reqEnv))
	for k, v := range base {
		if v != "" {
			merged[k] = v
		}
	}
	maps.Copy(merged, reqEnv)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// JSON map accessors — tolerant of missing/mistyped fields.

func mapString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func mapInt(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// resolveModel picks the model for a request: the explicit field first, then
// the env key the provider was told to read.
func resolveModel(cfg providerConfig, req Request) string {
	if req.Model != "" {
		return req.Model
	}
	if cfg.modelEnv != "" {
		return req.Env[cfg.modelEnv]
	}
	return ""
}

// promptWithSystem is what a CLI with no system-prompt flag gets: the policy
// text in front of the prompt, the way codex and gemini already do it.
func promptWithSystem(req Request) string {
	if req.SystemPrompt == "" {
		return req.Prompt
	}
	return req.SystemPrompt + "\n\n" + req.Prompt
}

// joinTextParts concatenates the "text" fields of a content-part list of the
// shape every one of these CLIs has converged on: [{"type":"text","text":...}].
// A bare string is returned as itself, for the CLIs that collapse a single
// text part into one.
func joinTextParts(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	parts, _ := content.([]any)
	var text string
	for _, p := range parts {
		item, _ := p.(map[string]any)
		if item == nil || item["type"] != "text" {
			continue
		}
		if t, ok := item["text"].(string); ok && t != "" {
			if text != "" {
				text += "\n"
			}
			text += t
		}
	}
	return text
}

// finishOutput turns whatever is left into events.
//
// A caller that streamed through ParseChunk gets only the buffered tail. A
// caller that collected the output and handed it to Finalize gets it parsed
// here — because the parameter is called fullOutput, and a parameter that is
// accepted and discarded invites exactly one mistake: pass everything, get an
// empty Result and no error, which is the least debuggable outcome available.
func finishOutput(lb *LineBuffer, fullOutput []byte, mapper func(map[string]any) []Event) []Event {
	if !lb.Fed() && len(fullOutput) > 0 {
		events := mapJSONLines(lb.Feed(fullOutput), mapper)
		return append(events, mapJSONLines(lb.Flush(), mapper)...)
	}
	return mapJSONLines(lb.Flush(), mapper)
}

// lastPlainLine returns the last non-empty line of output that is not a JSON
// frame.
//
// For a CLI whose errors are bare text on stdout, that line is the only
// account of why a turn failed, and a caller told only "failed" with no reason
// has to go and rerun it by hand to find out.
func lastPlainLine(output []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(stripANSI(lines[i]))
		if line == "" || strings.HasPrefix(line, "{") {
			continue
		}
		return line
	}
	return ""
}

// ansiEscape matches CSI sequences, which a CLI writing to a pty sprinkles
// through its plain-text errors (cursor-agent ends with one that re-shows the
// cursor).
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// appendPrompt puts the prompt last in argv. A prompt that starts with a dash
// goes after "--": unseparated, "--deny this" was read as an unknown flag and
// the CLI exited before doing anything.
func appendPrompt(argv []string, prompt string) []string {
	if strings.HasPrefix(prompt, "-") {
		argv = append(argv, "--")
	}
	return append(argv, prompt)
}
