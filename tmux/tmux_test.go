package tmux

import "testing"

func TestAttachCmdValidation(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	if _, err := AttachCmd("main_1"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	for _, bad := range []string{"", "a b", "a;rm -rf", "../x", "a$b"} {
		if _, err := AttachCmd(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
}

func TestListNoErrorWithoutServer(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	// Should not error even if no tmux server is running.
	if _, err := List(); err != nil {
		t.Fatalf("List errored: %v", err)
	}
}
