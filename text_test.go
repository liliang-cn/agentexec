package agentexec

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestTextProviderNeedsAnArgvTemplate(t *testing.T) {
	_, err := NewText(WithBinary("/usr/bin/some-agent")).NewSession().BuildCommand(context.Background(), Request{Prompt: "x"})
	if !errors.Is(err, ErrNoArgv) {
		t.Fatalf("err=%v", err)
	}
}

func TestTextProviderSubstitutesThePrompt(t *testing.T) {
	p := NewText(WithName("mine"), WithBinary("/opt/agent"), WithArgv("--run", "{prompt}", "--quiet"), WithModelFlag("-m"))
	spec, err := p.NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", SystemPrompt: "be brief", Model: "m1", WorkspacePath: "/work", ExtraArgs: []string{"--x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/agent", "--run", "be brief\n\ndo it", "--quiet", "-m", "m1", "--x"}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	if p.Name() != "mine" || spec.WorkDir != "/work" {
		t.Fatalf("name=%q workdir=%q", p.Name(), spec.WorkDir)
	}

	// No {prompt} token: the prompt goes last.
	spec, _ = NewText(WithBinary("a"), WithArgv("--once")).NewSession().BuildCommand(context.Background(), Request{Prompt: "p"})
	if !slices.Equal(spec.Argv, []string{"a", "--once", "p"}) {
		t.Fatalf("argv=%q", spec.Argv)
	}
}

func TestTextSessionTurnsOutputIntoOneAnswer(t *testing.T) {
	session := NewText(WithBinary("a"), WithArgv("{prompt}")).NewSession()
	events, err := session.ParseChunk([]byte("thinking...\n\nPROBE"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != EventTerminalOutput || events[0].Payload["line"] != "thinking..." {
		t.Fatalf("events=%+v", events)
	}
	res, tail, err := session.Finalize(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "thinking...\n\nPROBE" {
		t.Fatalf("summary=%q", res.Summary)
	}
	if msg := findEvent(tail, EventAgentMessage); msg == nil || msg.Payload["role"] != "assistant" || msg.Payload["text"] != res.Summary {
		t.Fatalf("tail=%+v", tail)
	}
	if line := findEvent(tail, EventTerminalOutput); line == nil || line.Payload["line"] != "PROBE" {
		t.Fatalf("partial last line must be flushed: %+v", tail)
	}
}

func TestTextSessionCollectThenFinalize(t *testing.T) {
	session := NewText(WithBinary("a"), WithArgv("{prompt}")).NewSession()
	res, tail, err := session.Finalize(context.Background(), []byte("all at once\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "all at once" || len(tail) != 2 {
		t.Fatalf("res=%+v tail=%+v", res, tail)
	}
}

func TestAiderGoldenArgv(t *testing.T) {
	spec, err := NewAider().NewSession().BuildCommand(context.Background(), Request{
		Prompt: "do it", Model: "gpt-4o", ExtraArgs: []string{"--no-auto-commits"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"aider", "--message", "do it", "--yes-always", "--no-pretty", "--no-stream",
		"--no-check-update", "--no-fancy-input", "--model", "gpt-4o", "--no-auto-commits",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv=%q\nwant=%q", spec.Argv, want)
	}
	if c := NewAider().Capabilities(); c.Resume || !c.Streaming {
		t.Fatalf("caps=%+v", c)
	}
}
