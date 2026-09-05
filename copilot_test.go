package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestCopilotGoldenArgv(t *testing.T) {
	spec, err := NewCopilot().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "gpt-4.1",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "5411",
		SystemPrompt:    "be brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	// --resume's value is attached with "=" because the flag's value is
	// optional; --yolo already covers --allow-all-tools.
	want := []string{
		"copilot", "--output-format", "json", "--model", "gpt-4.1", "--resume=5411",
		"--yolo", "--no-ask-user", "--prompt", "be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
}

func TestCopilotHeadlessWithoutBypassStillAllowsTools(t *testing.T) {
	spec, err := NewCopilot().NewSession().BuildCommand(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// The CLI's own help: --allow-all-tools is required for non-interactive
	// mode. Sandbox: false is the headless posture, so it goes in.
	if !slices.Contains(spec.Argv, "--allow-all-tools") || slices.Contains(spec.Argv, "--yolo") {
		t.Fatalf("argv=%q", spec.Argv)
	}
	spec, _ = NewCopilot().NewSession().BuildCommand(context.Background(), Request{Prompt: "x", Sandbox: true})
	if slices.Contains(spec.Argv, "--allow-all-tools") || slices.Contains(spec.Argv, "--no-ask-user") {
		t.Fatalf("Sandbox: true must leave copilot's own permission flow alone: %q", spec.Argv)
	}
}

func TestCopilotParsesSessionEvents(t *testing.T) {
	// Recorded from copilot 1.0.34 with gpt-4.1. Ephemeral session.* frames
	// trimmed to one.
	frames := `{"type":"session.tools_updated","data":{"model":"gpt-4.1"},"id":"6a4c","ephemeral":true}
{"type":"user.message","data":{"content":"Run echo and reply PROBE-OK.","attachments":[]},"id":"4059"}
{"type":"assistant.turn_start","data":{"turnId":"0"},"id":"0a7c"}
{"type":"assistant.message","data":{"messageId":"ceaa","content":"","toolRequests":[{"toolCallId":"call_4xw","name":"bash","arguments":{"command":"echo hi-from-tool"},"type":"function"}],"outputTokens":57},"id":"0556"}
{"type":"tool.execution_start","data":{"toolCallId":"call_4xw","toolName":"bash","arguments":{"command":"echo hi-from-tool","description":"Echo test message"}},"id":"8e8e"}
{"type":"tool.execution_complete","data":{"toolCallId":"call_4xw","model":"gpt-4.1","success":true,"result":{"content":"hi-from-tool\n<exited with exit code 0>"}},"id":"ce6c"}
{"type":"assistant.turn_end","data":{"turnId":"0"},"id":"38b1"}
{"type":"assistant.message_delta","data":{"messageId":"7f18","deltaContent":"PRO"},"id":"5256","ephemeral":true}
{"type":"assistant.message","data":{"messageId":"7f18","content":"PROBE-OK","toolRequests":[],"outputTokens":6},"id":"e5b4"}
{"type":"result","timestamp":"2026-09-05T02:26:42.832Z","sessionId":"54117941","exitCode":0,"usage":{"premiumRequests":0,"totalApiDurationMs":8322}}
`
	session := NewCopilot().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID() != "54117941" {
		t.Fatalf("session id=%q", session.SessionID())
	}
	call := findEvent(events, EventToolCall)
	if call == nil || call.Payload["name"] != "bash" || call.Payload["id"] != "call_4xw" {
		t.Fatalf("tool call=%+v", call)
	}
	result := findEvent(events, EventToolResult)
	if result == nil || result.Payload["success"] != true {
		t.Fatalf("tool result=%+v", result)
	}
	var texts []string
	for _, e := range events {
		if e.Type == EventAgentMessage && e.Payload["role"] == "assistant" {
			if text, _ := e.Payload["text"].(string); text != "" && e.Payload["delta"] != true {
				texts = append(texts, text)
			}
		}
	}
	if !slices.Equal(texts, []string{"PROBE-OK"}) {
		t.Fatalf("assistant texts=%q", texts)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Failed || res.Usage.OutputTokens != 63 {
		t.Fatalf("result=%+v", res)
	}
}

func TestCopilotResultExitCodeIsTheVerdict(t *testing.T) {
	// Recorded: unsupported model. The CLI's own exit code is on the frame.
	frames := `{"type":"session.error","data":{"errorType":"query","message":"Execution failed: CAPIError: 400 The requested model is not supported.","statusCode":400},"id":"fb17"}
{"type":"result","timestamp":"2026-09-05T02:17:26.796Z","sessionId":"3904","exitCode":1,"usage":{"premiumRequests":0}}
`
	session := NewCopilot().NewSession()
	res, tail, err := session.Finalize(context.Background(), []byte(frames), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || session.SessionID() != "3904" {
		t.Fatalf("result=%+v id=%q", res, session.SessionID())
	}
	if e := findEvent(tail, EventAgentMessage); e == nil || e.Payload["role"] != "error" {
		t.Fatalf("tail=%+v", tail)
	}
}
