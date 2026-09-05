package agentexec

import (
	"context"
	"os"
	"slices"
	"testing"
)

func TestHermesGoldenArgvAndUsageFile(t *testing.T) {
	session := NewHermes().NewSession()
	spec, err := session.BuildCommand(context.Background(), Request{
		Prompt: "do it", WorkspacePath: "/work", Model: "anthropic/claude-sonnet-4",
		ResumeSessionID: "20260905_1", SystemPrompt: "be brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Argv) < 3 || spec.Argv[1] != "--usage-file" {
		t.Fatalf("argv=%q", spec.Argv)
	}
	usagePath := spec.Argv[2]
	if _, err := os.Stat(usagePath); err != nil {
		t.Fatalf("usage file must exist before the run: %v", err)
	}
	want := []string{
		"hermes", "--usage-file", usagePath, "--model", "anthropic/claude-sonnet-4",
		"--resume", "20260905_1", "--oneshot", "be brief\n\ndo it",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}

	// The schema hermes_cli/oneshot.py writes, including the verdict.
	report := `{"estimated_cost_usd":0.02,"input_tokens":5000,"output_tokens":40,"cache_read_tokens":100,"cache_write_tokens":0,"reasoning_tokens":10,"total_tokens":5150,"api_calls":2,"model":"anthropic/claude-sonnet-4","provider":"anthropic","session_id":"20260905_101729_af5f73","completed":true,"failed":false}`
	if err := os.WriteFile(usagePath, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	res, tail, err := session.Finalize(context.Background(), []byte("PROBE-OK\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "PROBE-OK" || res.Failed || session.SessionID() != "20260905_101729_af5f73" {
		t.Fatalf("res=%+v id=%q", res, session.SessionID())
	}
	if res.Usage.InputTokens != 5000 || res.Usage.OutputTokens != 50 || res.Usage.CacheTokens != 100 ||
		res.Usage.EstimatedCostUSD != 0.02 || res.Usage.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("usage=%+v", res.Usage)
	}
	if msg := findEvent(tail, EventAgentMessage); msg == nil || msg.Payload["text"] != "PROBE-OK" {
		t.Fatalf("tail=%+v", tail)
	}
	if _, err := os.Stat(usagePath); !os.IsNotExist(err) {
		t.Fatalf("usage file must be removed after Finalize: %v", err)
	}
}

func TestHermesFailedFlagIsTheVerdict(t *testing.T) {
	session := NewHermes().NewSession()
	spec, err := session.BuildCommand(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Written "even when the run fails, so pipelines can always account for
	// spend" — and the answer text is the error, which the exit code may or
	// may not reflect.
	if err := os.WriteFile(spec.Argv[2], []byte(`{"failed":true,"session_id":"s1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _, err := session.Finalize(context.Background(), []byte("API call failed after 3 retries\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || session.SessionID() != "s1" {
		t.Fatalf("res=%+v", res)
	}
}

func TestHermesMissingUsageFileLeavesTheTextResult(t *testing.T) {
	session := NewHermes().NewSession()
	spec, err := session.BuildCommand(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(spec.Argv[2])
	res, _, err := session.Finalize(context.Background(), []byte("hello\n"), 0)
	if err != nil || res.Summary != "hello" || res.Failed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
