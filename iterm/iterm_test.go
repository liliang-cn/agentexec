package iterm

import (
	"strings"
	"testing"
)

func TestQuoteEscapesAndJoinsLines(t *testing.T) {
	cases := map[string]string{
		"":         `""`,
		"hi":       `"hi"`,
		`say "x"`:  `"say \"x\""`,
		`a\b`:      `"a\\b"`,
		"one\ntwo": `"one" & linefeed & "two"`,
		"tail\n":   `"tail" & linefeed & ""`,
	}
	for in, want := range cases {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFindScriptAddressesTheTTY(t *testing.T) {
	s := findScript("/dev/ttys003", `return "yes"`)
	if !strings.Contains(s, `(tty of s) is "/dev/ttys003"`) || !strings.Contains(s, `return "yes"`) {
		t.Fatalf("findScript did not address the tty:\n%s", s)
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote(`it's`); got != `'it'\''s'` {
		t.Fatalf("ShellQuote = %s", got)
	}
}

func TestSendTextNeedsATTY(t *testing.T) {
	if err := SendText(t.Context(), "", "x"); err == nil {
		t.Fatal("SendText with no tty should fail")
	}
}
