// Package pty runs a command under a pseudo-terminal, streaming its combined
// output to a callback and capturing it. It knows nothing about providers.
package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Command describes a process to run under a PTY. It maps from cliagent.CommandSpec.
type Command struct {
	Argv    []string
	Env     []string // "KEY=VALUE" overrides applied on top of os.Environ()
	WorkDir string
	Stdin   []byte // written to the PTY once started (optional)
	Rows    uint16 // defaults to 40
	Cols    uint16 // defaults to 120
}

// Result is the outcome of a PTY run.
type Result struct {
	ExitCode int
	Output   []byte
}

// Run starts cmd under a PTY in its own process group, forwarding output chunks
// to onChunk and accumulating Result.Output. On ctx cancellation the child gets
// SIGINT, then SIGKILL after 2s, and ctx.Err() is returned.
func Run(ctx context.Context, cmd Command, onChunk func([]byte)) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("pty: empty argv")
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Env = append(os.Environ(), cmd.Env...)
	c.Dir = cmd.WorkDir
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	ptmx, err := pty.Start(c)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = ptmx.Close() }()

	rows, cols := cmd.Rows, cmd.Cols
	if rows == 0 {
		rows = 40
	}
	if cols == 0 {
		cols = 120
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})

	if len(cmd.Stdin) > 0 {
		go func() { _, _ = ptmx.Write(cmd.Stdin) }()
	}

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
			n, rerr := ptmx.Read(buf)
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

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGINT)
				select {
				case <-time.After(2 * time.Second):
					_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
				case <-done:
				}
			}
		case <-done:
		}
	}()

	waitErr := c.Wait()
	close(done)
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
	if waitErr != nil && !errors.Is(waitErr, io.EOF) {
		res.ExitCode = -1
		return res, waitErr
	}
	return res, nil
}
