package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestOpencodeGoldenArgv(t *testing.T) {
	spec, err := NewOpencode().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "opencode/big-pickle",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "ses_1",
		SystemPrompt:    "be brief",
		ExtraArgs:       []string{"--agent", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"opencode", "run", "--format", "json", "--model", "opencode/big-pickle",
		"--session", "ses_1", "--auto", "--agent", "build", "be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
}

func TestOpencodeParsesRunJSON(t *testing.T) {
	// Recorded from opencode 1.18.21 with a free model.
	frames := `{"type":"step_start","timestamp":1,"sessionID":"ses_f909","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_f909","type":"step-start"}}
{"type":"tool_use","timestamp":2,"sessionID":"ses_f909","part":{"type":"tool","tool":"bash","callID":"call_907","state":{"status":"completed","input":{"command":"echo hi-from-tool"},"output":"hi-from-tool\n","metadata":{"exit":0},"title":"echo hi-from-tool"},"id":"prt_2","sessionID":"ses_f909","messageID":"msg_1"}}
{"type":"step_finish","timestamp":3,"sessionID":"ses_f909","part":{"id":"prt_3","reason":"tool-calls","messageID":"msg_1","sessionID":"ses_f909","type":"step-finish","tokens":{"total":24444,"input":22586,"output":66,"reasoning":0,"cache":{"write":0,"read":1792}},"cost":0}}
{"type":"step_start","timestamp":4,"sessionID":"ses_f909","part":{"id":"prt_4","messageID":"msg_2","sessionID":"ses_f909","type":"step-start"}}
{"type":"text","timestamp":5,"sessionID":"ses_f909","part":{"id":"prt_5","messageID":"msg_2","sessionID":"ses_f909","type":"text","text":"PROBE-OK","time":{"start":1,"end":2}}}
{"type":"step_finish","timestamp":6,"sessionID":"ses_f909","part":{"id":"prt_6","reason":"stop","messageID":"msg_2","sessionID":"ses_f909","type":"step-finish","tokens":{"total":24466,"input":140,"output":6,"reasoning":0,"cache":{"write":0,"read":24320}},"cost":0.5}}
`
	session := NewOpencode().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID() != "ses_f909" {
		t.Fatalf("session id=%q", session.SessionID())
	}
	call := findEvent(events, EventToolCall)
	if call == nil || call.Payload["name"] != "bash" || call.Payload["id"] != "call_907" {
		t.Fatalf("tool call=%+v", call)
	}
	result := findEvent(events, EventToolResult)
	if result == nil || result.Payload["output"] != "hi-from-tool\n" || result.Payload["status"] != "completed" {
		t.Fatalf("tool result=%+v", result)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Usage is summed across the two steps.
	if res.Summary != "PROBE-OK" || res.Failed ||
		res.Usage.InputTokens != 22726 || res.Usage.OutputTokens != 72 ||
		res.Usage.CacheTokens != 26112 || res.Usage.EstimatedCostUSD != 0.5 {
		t.Fatalf("result=%+v", res)
	}
}

func TestOpencodeErrorFrameIsAVerdict(t *testing.T) {
	frame := `{"type":"error","timestamp":1,"sessionID":"ses_x","error":{"name":"ProviderAuthError","data":{"message":"no api key"}}}` + "\n"
	session := NewOpencode().NewSession()
	res, tail, err := session.Finalize(context.Background(), []byte(frame), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed {
		t.Fatalf("result=%+v", res)
	}
	if e := findEvent(tail, EventAgentMessage); e == nil || e.Payload["role"] != "error" || e.Payload["text"] != "no api key" {
		t.Fatalf("tail=%+v", tail)
	}
}
