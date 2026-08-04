package cliagent

import "strings"

// LineBuffer accumulates streamed bytes and emits complete lines. Carriage
// returns are stripped; a trailing partial line (no newline yet) is buffered
// until completed by a later Feed or surfaced by Flush. The zero value is ready
// to use.
type LineBuffer struct {
	buf strings.Builder
	fed bool
}

// Fed reports whether Feed has ever been called. Finalize uses it to tell a
// caller that streamed the output from one that collected it and passed the
// whole thing at the end.
func (lb *LineBuffer) Fed() bool { return lb.fed }

// Feed appends chunk and returns any complete lines it produced.
func (lb *LineBuffer) Feed(chunk []byte) []string {
	lb.fed = true
	lb.buf.Write(chunk)
	s := lb.buf.String()

	idx := strings.LastIndexByte(s, '\n')
	if idx < 0 {
		return nil
	}

	complete := s[:idx]    // everything up to the last newline
	remainder := s[idx+1:] // partial tail after the last newline

	lb.buf.Reset()
	lb.buf.WriteString(remainder)

	lines := strings.Split(complete, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// Flush returns the buffered partial tail (if any) and clears the buffer.
func (lb *LineBuffer) Flush() []string {
	s := lb.buf.String()
	lb.buf.Reset()
	if s == "" {
		return nil
	}
	return []string{strings.TrimSuffix(s, "\r")}
}
