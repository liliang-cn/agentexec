package agentexec

import (
	"context"
	"slices"
	"testing"
)

func TestGooseGoldenArgv(t *testing.T) {
	spec, err := NewGoose().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "gpt-5",
		PermissionMode:  PermissionBypass,
		ResumeSessionID: "20260905_4",
		SystemPrompt:    "be brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The system prompt has its own flag; model and approval mode are
	// environment variables, not flags.
	want := []string{
		"goose", "run", "--output-format", "stream-json",
		"--resume", "--session-id", "20260905_4", "--system", "be brief", "--text", "do it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	for _, kv := range []string{"GOOSE_MODEL=gpt-5", "GOOSE_MODE=auto"} {
		if !slices.Contains(spec.Env, kv) {
			t.Fatalf("env=%v missing %s", spec.Env, kv)
		}
	}
}

func TestGooseParsesStreamJSONAndSniffsTheBanner(t *testing.T) {
	// Recorded from goose 1.44.0, banner included.
	frames := "\n" +
		"    __( O)>  ● new session · openai gemini-3.7-flash-high\n" +
		"   \\____)    20260905_4 · /tmp/probe4\n" +
		"     L L     goose is ready\n" +
		`{"type":"message","message":{"id":"MHyb","role":"assistant","created":1,"content":[{"type":"toolRequest","id":"shell-954","toolCall":{"status":"success","value":{"name":"shell","arguments":{"command":"echo hi-from-tool"}}},"_meta":{"goose_extension":"developer"}}],"metadata":{"userVisible":true}}}` + "\n" +
		`{"type":"message","message":{"id":"msg_34","role":"user","created":2,"content":[{"type":"toolResponse","id":"shell-954","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"hi-from-tool"}],"structuredContent":{"stdout":"hi-from-tool","exit_code":0},"isError":false}}}],"metadata":{"userVisible":true}}}` + "\n" +
		`{"type":"message","message":{"id":"M3yb","role":"assistant","created":3,"content":[{"type":"text","text":"PROBE-OK"}],"metadata":{"userVisible":true}}}` + "\n" +
		`{"type":"complete","total_tokens":12463,"input_tokens":12296,"output_tokens":66,"cache_read_input_tokens":0,"cache_write_input_tokens":0}` + "\n"

	session := NewGoose().NewSession()
	events, err := session.ParseChunk([]byte(frames))
	if err != nil {
		t.Fatal(err)
	}
	// The first banner line also has " · " in it; the id line is the one
	// whose left side ends in the id and whose right side is the path.
	if session.SessionID() != "20260905_4" {
		t.Fatalf("banner sniff took the wrong line: %q", session.SessionID())
	}
	call := findEvent(events, EventToolCall)
	if call == nil || call.Payload["name"] != "shell" || call.Payload["id"] != "shell-954" {
		t.Fatalf("tool call=%+v", call)
	}
	result := findEvent(events, EventToolResult)
	if result == nil || result.Payload["output"] != "hi-from-tool" || result.Payload["is_error"] != false {
		t.Fatalf("tool result=%+v", result)
	}
	if line := findEvent(events, EventTerminalOutput); line == nil {
		t.Fatalf("banner lines must survive as terminal output: %+v", events)
	}
	res, _, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Usage.InputTokens != 12296 || res.Usage.OutputTokens != 66 {
		t.Fatalf("result=%+v", res)
	}
}
