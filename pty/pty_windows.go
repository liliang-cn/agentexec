//go:build windows

// Package pty runs a command under a pseudo-terminal, streaming its combined
// output to a callback and capturing it. It knows nothing about providers.
//
// Windows has no pseudo-terminal this package can open, so there a command
// runs on plain pipes: the same streamed and captured output, the same exit
// code and errors, without the terminal a CLI would draw colour and prompts
// for. Cancelling kills the process.
package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Command describes a process to run. It maps from agentexec.CommandSpec.
// Rows and Cols only mean something under a terminal and are ignored here.
type Command struct {
	Argv    []string
	Env     []string // "KEY=VALUE" overrides applied on top of os.Environ()
	WorkDir string
	Stdin   []byte // written to the process once started (optional)
	Rows    uint16
	Cols    uint16
}

// Result is the outcome of a run.
type Result struct {
	ExitCode int
	Output   []byte
}

// Run starts cmd with stdout and stderr on one pipe, forwarding output chunks
// to onChunk and accumulating Result.Output. On ctx cancellation the process
// is killed and ctx.Err() is returned.
func Run(ctx context.Context, cmd Command, onChunk func([]byte)) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("pty: empty argv")
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Env = append(os.Environ(), cmd.Env...)
	c.Dir = cmd.WorkDir
	if len(cmd.Stdin) > 0 {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw

	var (
		fullBuf bytes.Buffer
		bufMu   sync.Mutex
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := pr.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				bufMu.Lock()
				fullBuf.Write(chunk)
				bufMu.Unlock()
				if onChunk != nil {
					onChunk(chunk)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	if err := c.Start(); err != nil {
		_ = pw.Close()
		wg.Wait()
		return Result{}, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Process.Kill()
		case <-done:
		}
	}()

	waitErr := c.Wait()
	close(done)
	// Closing the writer is what ends the reader; whatever is buffered is
	// read first.
	_ = pw.Close()
	wg.Wait()

	res := Result{Output: fullBuf.Bytes()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		res.ExitCode = -1
		return res, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if waitErr != nil {
		res.ExitCode = -1
		return res, waitErr
	}
	return res, nil
}
