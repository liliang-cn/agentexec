package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestKimiGoldenArgv(t *testing.T) {
	spec, err := NewKimi().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "kimi-k2",
		PermissionMode:  PermissionBypass, // implied by --print; must add nothing
		ResumeSessionID: "sess-9",
		SystemPrompt:    "be brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kimi", "--print", "--output-format", "stream-json", "--model", "kimi-k2",
		"--session", "sess-9", "--prompt", "be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	if spec.WorkDir != "/work" {
		t.Fatalf("workdir=%q", spec.WorkDir)
	}
}

func TestKimiParsesKosongMessages(t *testing.T) {
	// Recorded from kimi 1.3. Note the assistant's final content is a bare
	// string, and the tool message is role "tool" with a tool_call_id.
	frames := `{"role":"assistant","content":[],"tool_calls":[{"type":"function","id":"Shell-963","function":{"name":"Shell","arguments":"{\"command\": \"echo hi-from-tool\"}"}}]}
{"role":"tool","content":[{"type":"text","text":"<system>Command executed successfully.</system>"},{"type":"text","text":"hi-from-tool\n"}],"tool_call_id":"Shell-963"}
{"role":"assistant","content":"PROBE-OK"}
`
	session := NewKimi().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	call := findEvent(events, EventToolCall)
	if call == nil || call.Payload["name"] != "Shell" || call.Payload["id"] != "Shell-963" {
		t.Fatalf("tool call=%+v", call)
	}
	result := findEvent(events, EventToolResult)
	if result == nil || result.Payload["id"] != "Shell-963" {
		t.Fatalf("tool result=%+v", result)
	}
	var answer string
	for _, e := range events {
		if e.Type == EventAgentMessage && e.Payload["role"] == "assistant" {
			answer, _ = e.Payload["text"].(string)
		}
	}
	if answer != "PROBE-OK" {
		t.Fatalf("answer=%q", answer)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Failed {
		t.Fatalf("result=%+v", res)
	}
}

func TestKimiErrorsArePlainTextLines(t *testing.T) {
	// Recorded: no model configured. Exit 0 and a bare line, no frame.
	session := NewKimi().NewSession()
	res, tail, err := session.Finalize(context.Background(), []byte("LLM not set\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	line := findEvent(tail, EventTerminalOutput)
	if line == nil || line.Payload["line"] != "LLM not set" {
		t.Fatalf("tail=%+v", tail)
	}
	if res.Summary != "" || res.Failed {
		t.Fatalf("no verdict exists to read, got %+v", res)
	}
}
