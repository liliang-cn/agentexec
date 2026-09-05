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

// Command describes a process to run under a PTY. It maps from agentexec.CommandSpec.
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

	ptmx, pts, err := startPTY(c)
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
	// Releasing the slave is what ends the reader, so it happens here rather
	// than on a defer. Output already buffered survives it and is drained
	// before the reader sees EOF.
	_ = pts.Close()
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

// startPTY starts c under a new pseudo-terminal and hands back both ends. The
// slave comes back still open: the caller owns it, and must not close it until
// it has finished reading the master.
//
// pty.Start closes the parent's slave as soon as the child is running, which
// leaves the child holding the only one. When it exits, that is the terminal's
// last close: the kernel waits briefly for pending output to drain, then gives
// up, tears the terminal down, and drops whatever is still buffered. A command
// short-lived enough to beat the first read therefore produces nothing at all —
// zero exit status, no error, empty output, nothing anywhere saying the output
// was ever there. Keeping a slave open means the child's exit is never the last
// close, so nothing is torn down while there is still something to read.
func startPTY(c *exec.Cmd) (ptmx, pts *os.File, err error) {
	ptmx, pts, err = pty.Open()
	if err != nil {
		return nil, nil, err
	}
	c.Stdin, c.Stdout, c.Stderr = pts, pts, pts
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setsid = true
	// Ctty is left at 0, which is c.Stdin, matching what pty.Start does.
	c.SysProcAttr.Setctty = true
	if err := c.Start(); err != nil {
		_ = ptmx.Close()
		_ = pts.Close()
		return nil, nil, err
	}
	return ptmx, pts, nil
}
