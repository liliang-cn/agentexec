package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestAgyGoldenArgv(t *testing.T) {
	spec, err := NewAgy().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "gemini-3-pro",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "conv-1",
		SystemPrompt:    "be brief",
		ExtraArgs:       []string{"--effort", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// --print swallows the next argument as its prompt whatever it looks
	// like, so it is last and immediately followed by the prompt.
	want := []string{
		"agy", "--output-format", "stream-json", "--model", "gemini-3-pro",
		"--conversation", "conv-1", "--dangerously-skip-permissions",
		"--effort", "high", "--print", "be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
}

func TestAgyReadsTheResultFrame(t *testing.T) {
	// Recorded from agy 1.1.26 without a login: status ERROR, exit 1.
	frame := `{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"authentication failed or timed out","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0}}}` + "\n"
	session := NewAgy().NewSession()
	res, _, err := session.Finalize(context.Background(), []byte(frame), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || res.Summary != "" {
		t.Fatalf("result=%+v", res)
	}

	// The shape of a successful result, from the same struct.
	frame = `{"event":"result","result":{"conversation_id":"conv-9","status":"SUCCESS","response":"PROBE-OK","error":"","duration_seconds":3,"num_turns":1,"usage":{"input_tokens":100,"output_tokens":5,"thinking_tokens":2,"cache_read_tokens":40,"total_tokens":147}}}` + "\n"
	session = NewAgy().NewSession()
	res, _, err = session.Finalize(context.Background(), []byte(frame), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed || res.Summary != "PROBE-OK" || session.SessionID() != "conv-9" ||
		res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 7 || res.Usage.CacheTokens != 40 {
		t.Fatalf("result=%+v id=%q", res, session.SessionID())
	}
}

func TestAgyStepUpdatesMapByContent(t *testing.T) {
	frames := `{"event":"init","init":{"conversation_id":"conv-9","model":"gemini-3-pro","tools":["run_command"]}}
{"event":"step_update","step_update":{"step_index":0,"step_type":"STEP_TYPE_RUN_COMMAND","tool_name":"run_command","tool_info":{"command":"echo hi"}}}
{"event":"step_update","step_update":{"step_index":1,"step_type":"STEP_TYPE_TEXT","text_delta":"PROBE"}}
{"event":"step_update","step_update":{"step_index":1,"step_type":"STEP_TYPE_CHECKPOINT"}}
`
	session := NewAgy().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID() != "conv-9" {
		t.Fatalf("session id=%q", session.SessionID())
	}
	if call := findEvent(events, EventToolCall); call == nil || call.Payload["name"] != "run_command" {
		t.Fatalf("tool call=%+v", call)
	}
	var roles []string
	for _, e := range events {
		if e.Type == EventAgentMessage {
			roles = append(roles, e.Payload["role"].(string))
		}
	}
	if !slices.Equal(roles, []string{"system", "assistant", "step"}) {
		t.Fatalf("roles=%q", roles)
	}
}
