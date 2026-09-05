package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestQwenGoldenArgv(t *testing.T) {
	spec, err := NewQwen(WithModelEnv("QWEN_MODEL")).NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "ses-1",
		SystemPrompt:    "be brief",
		Env:             map[string]string{"QWEN_MODEL": "qwen3-coder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positional prompt (-p is deprecated), --yolo for bypass, and the system
	// prompt has a real flag here unlike cursor.
	want := []string{
		"qwen", "--output-format", "stream-json", "--model", "qwen3-coder",
		"--resume", "ses-1", "--yolo", "--append-system-prompt", "be brief", "do it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	if !slices.Contains(spec.Env, "QWEN_CODE_SUPPRESS_YOLO_WARNING=1") {
		t.Fatalf("bypass must silence the yolo warning: %v", spec.Env)
	}
}

func TestQwenParsesTheClaudeDialect(t *testing.T) {
	// Recorded from qwen 0.23.0 against an OpenAI-compatible endpoint.
	frames := `{"type":"system","subtype":"init","uuid":"32d1","session_id":"32d1a118","cwd":"/w","tools":["run_shell_command"],"model":"gemini-3.8-flash","permission_mode":"yolo","qwen_code_version":"0.23.0"}
{"type":"stream_event","uuid":"b4e7","session_id":"32d1a118","event":{"type":"goal_state"}}
{"type":"assistant","uuid":"0d40","session_id":"32d1a118","message":{"role":"assistant","model":"gemini-3.8-flash","content":[{"type":"tool_use","id":"run_shell_command-1","name":"run_shell_command","input":{"command":"echo hi-from-tool"}}],"stop_reason":"tool_use","usage":{"input_tokens":24232,"output_tokens":44,"cache_read_input_tokens":0}}}
{"type":"user","uuid":"e2c3","session_id":"32d1a118","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"run_shell_command-1","is_error":false,"content":"hi-from-tool"}]}}
{"type":"assistant","uuid":"77aa","session_id":"32d1a118","message":{"role":"assistant","model":"gemini-3.8-flash","content":[{"type":"text","text":"PROBE-OK"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":4}}}
{"type":"result","subtype":"success","uuid":"c2b4","session_id":"32d1a118","is_error":false,"duration_ms":10,"num_turns":2,"result":"PROBE-OK","usage":{"input_tokens":24332,"output_tokens":48}}
`
	session := NewQwen().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID() != "32d1a118" {
		t.Fatalf("session id=%q", session.SessionID())
	}
	if findEvent(events, EventToolCall) == nil || findEvent(events, EventToolResult) == nil {
		t.Fatalf("expected tool call and result events: %+v", events)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Failed || res.Usage.InputTokens != 24332 {
		t.Fatalf("result=%+v", res)
	}
}

func TestQwenReadsIsErrorFromTheResultFrame(t *testing.T) {
	// Recorded: the account's OAuth tier was discontinued; exit code 1, and the
	// verdict is on the frame.
	frame := `{"type":"result","subtype":"error_during_execution","session_id":"ece0","is_error":true,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0},"error":{"message":"Qwen OAuth free tier was discontinued"}}` + "\n"
	session := NewQwen().NewSession()
	res, _, err := session.Finalize(context.Background(), []byte(frame), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || res.ExitCode != 1 {
		t.Fatalf("result=%+v", res)
	}
}
