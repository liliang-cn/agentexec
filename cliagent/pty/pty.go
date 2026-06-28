// Package pty runs a command under a pseudo-terminal, streaming its combined
// output to a callback and capturing it for the caller. It is the transport
// layer beneath cliagent: it knows nothing about providers, events, or usage.
package pty

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// Command describes a process to run under a PTY. It maps directly from a
// cliagent.CommandSpec.
type Command struct {
	Argv    []string
	Env     []string // "KEY=VALUE" overrides, applied on top of os.Environ()
	WorkDir string
	Stdin   []byte // written to the PTY once started; the program must self-terminate
}

// Result is the outcome of a PTY run.
type Result struct {
	ExitCode int
	Output   []byte
}

// Run starts cmd under a PTY, forwarding every output chunk to onChunk (if
// non-nil) and accumulating the full output into Result.Output. It returns when
// the process exits, the PTY closes, or ctx is cancelled. On cancellation the
// process is killed and ctx.Err() is returned.
func Run(ctx context.Context, cmd Command, onChunk func([]byte)) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("pty: empty argv")
	}

	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.WorkDir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}

	f, err := pty.Start(c)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	if len(cmd.Stdin) > 0 {
		go func() { _, _ = f.Write(cmd.Stdin) }()
	}

	var buf bytes.Buffer
	readBuf := make([]byte, 4096)
	for {
		n, readErr := f.Read(readBuf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, readBuf[:n])
			buf.Write(chunk)
			if onChunk != nil {
				onChunk(chunk)
			}
		}
		if readErr != nil {
			// EOF on clean exit; EIO when the child closes the PTY.
			break
		}
	}

	waitErr := c.Wait()
	res := Result{Output: buf.Bytes()}

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
