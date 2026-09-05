package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestPiGoldenArgv(t *testing.T) {
	spec, err := NewPi().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "cpa/gemini-3.8-flash",
		PermissionMode:  PermissionBypass, // pi has no approval prompts; must add nothing
		ResumeSessionID: "01a0",
		SystemPrompt:    "be brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pi", "--print", "--mode", "json", "--model", "cpa/gemini-3.8-flash",
		"--session", "01a0", "--append-system-prompt", "be brief", "do it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
}

func TestPiParsesAgentEvents(t *testing.T) {
	// Recorded from pi 0.85.0; tool frames from its agent-core event shape.
	frames := `{"type":"session","version":3,"id":"01a06f5a","timestamp":"2026-09-05T02:17:01.164Z","cwd":"/tmp/probe"}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly PROBE-OK."}],"timestamp":1}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly PROBE-OK."}],"timestamp":1}}
{"type":"tool_execution_start","toolCallId":"tc-1","toolName":"bash","args":{"command":"echo hi"}}
{"type":"tool_execution_end","toolCallId":"tc-1","toolName":"bash","result":{"content":[{"type":"text","text":"hi"}]},"isError":false}
{"type":"message_start","message":{"role":"assistant","content":[{"type":"text","text":"PROBE-OK"}],"model":"gemini-3.8-flash","usage":{"input":0,"output":0},"stopReason":"pending"}}
{"type":"message_update","usage":{"input":0},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"PROBE-OK"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"PROBE-OK"}],"api":"openai-completions","provider":"cpa","model":"gemini-3.8-flash","usage":{"input":2044,"output":4,"cacheRead":0,"cacheWrite":0,"totalTokens":2048,"cost":{"input":0,"output":0,"total":0.01}},"stopReason":"stop"}}
{"type":"turn_end","message":{"role":"assistant","content":[{"type":"text","text":"PROBE-OK"}]},"toolResults":[]}
{"type":"agent_end","messages":[]}
{"type":"agent_settled"}
`
	session := NewPi().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID() != "01a06f5a" {
		t.Fatalf("session id=%q", session.SessionID())
	}
	call := findEvent(events, EventToolCall)
	if call == nil || call.Payload["name"] != "bash" || call.Payload["id"] != "tc-1" {
		t.Fatalf("tool call=%+v", call)
	}
	if result := findEvent(events, EventToolResult); result == nil || result.Payload["is_error"] != false {
		t.Fatalf("tool result=%+v", result)
	}
	var texts []string
	for _, e := range events {
		if e.Type == EventAgentMessage && e.Payload["role"] == "assistant" && e.Payload["delta"] != true {
			texts = append(texts, e.Payload["text"].(string))
		}
	}
	// One assistant message, from message_end — message_start must not
	// produce a second copy.
	if !slices.Equal(texts, []string{"PROBE-OK"}) {
		t.Fatalf("assistant texts=%q", texts)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Failed || res.Usage.InputTokens != 2044 ||
		res.Usage.OutputTokens != 4 || res.Usage.Model != "gemini-3.8-flash" || res.Usage.EstimatedCostUSD != 0.01 {
		t.Fatalf("result=%+v", res)
	}
}

func TestPiStopReasonErrorIsTheVerdict(t *testing.T) {
	// Recorded: a 429 from the provider. The process exits zero.
	frames := `{"type":"session","version":3,"id":"01a06f5d","cwd":"/tmp"}
{"type":"message_end","message":{"role":"assistant","content":[],"model":"gemini-3.8-flash","usage":{"input":0,"output":0},"stopReason":"error","errorMessage":"429: quota exceeded"}}
`
	session := NewPi().NewSession()
	res, tail, err := session.Finalize(context.Background(), []byte(frames), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || res.ExitCode != 0 {
		t.Fatalf("result=%+v", res)
	}
	var found bool
	for _, e := range tail {
		if e.Type == EventAgentMessage && e.Payload["role"] == "error" && e.Payload["text"] == "429: quota exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tail=%+v", tail)
	}
}
