package pty

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitZero(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Argv: []string{"printf", "hello-pty"},
	}, nil)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Output), "hello-pty") {
		t.Fatalf("Output = %q, want to contain hello-pty", res.Output)
	}
}

func TestRunInvokesOnChunk(t *testing.T) {
	var got bytes.Buffer
	_, err := Run(context.Background(), Command{
		Argv: []string{"printf", "streamed"},
	}, func(chunk []byte) { got.Write(chunk) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "streamed") {
		t.Fatalf("onChunk received %q, want to contain streamed", got.String())
	}
}

func TestRunNonZeroExit(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Argv: []string{"sh", "-c", "exit 3"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestRunFeedsStdin(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Argv:  []string{"head", "-n1"},
		Stdin: []byte("from-stdin\n"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "from-stdin") {
		t.Fatalf("Output = %q, want to contain from-stdin", res.Output)
	}
}

func TestRunRespectsWorkDir(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), Command{
		Argv:    []string{"pwd"},
		WorkDir: dir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// macOS /tmp symlinks to /private/tmp; match on the temp dir's base name.
	if !strings.Contains(string(res.Output), lastPathSegment(dir)) {
		t.Fatalf("pwd Output = %q, want to contain %q", res.Output, dir)
	}
}

func TestRunPassesEnv(t *testing.T) {
	res, err := Run(context.Background(), Command{
		Argv: []string{"sh", "-c", "echo $CLIAGENT_TEST_VAR"},
		Env:  []string{"CLIAGENT_TEST_VAR=present"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "present") {
		t.Fatalf("Output = %q, want to contain present", res.Output)
	}
}

func TestRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, Command{Argv: []string{"sleep", "10"}}, nil)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("context cancellation did not kill process promptly")
	}
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

func TestRunReportsSizeViaStty(t *testing.T) {
	// The PTY is sized 40x120; `stty size` prints "rows cols".
	res, err := Run(context.Background(), Command{Argv: []string{"sh", "-c", "stty size"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "40 120") {
		t.Fatalf("stty size = %q, want '40 120'", res.Output)
	}
}

func lastPathSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
