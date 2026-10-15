package reach

import "testing"

// feed builds an oldest→newest sighting list from (session, tty) pairs.
func feed(pairs ...[2]string) []Sighting {
	out := make([]Sighting, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, Sighting{SessionID: p[0], TTY: p[1]})
	}
	return out
}

func TestCurrentTTYFollowsTheSession(t *testing.T) {
	// Reopening an agent in a new tab moves it; the newest report wins.
	events := feed([2]string{"a", "/dev/ttys003"}, [2]string{"a", "/dev/ttys005"})
	tty, known := CurrentTTY(events, "a")
	if !known || tty != "/dev/ttys005" {
		t.Fatalf("currentTTY = (%q, %v), want (/dev/ttys005, true)", tty, known)
	}
}

func TestCurrentTTYDropsAReusedTerminal(t *testing.T) {
	// The heart of it: macOS hands a closed tab's tty to the next one that opens.
	// "a" still has ttys002 on record, but "b" is in that tab now — typing there
	// would put a's instruction into b's session.
	events := feed([2]string{"a", "/dev/ttys002"}, [2]string{"b", "/dev/ttys002"})
	tty, known := CurrentTTY(events, "a")
	if tty != "" {
		t.Fatalf("currentTTY = %q, want \"\" (b owns that tty now)", tty)
	}
	if !known {
		t.Fatal("known = false; a did report a terminal, so this is proof it lost it")
	}
	if tty, known := CurrentTTY(events, "b"); tty != "/dev/ttys002" || !known {
		t.Fatalf("CurrentTTY(b) = (%q, %v), want (/dev/ttys002, true)", tty, known)
	}
}

func TestCurrentTTYUnknownWithoutEvidence(t *testing.T) {
	// An app-launched run never reports a terminal. That is not proof it is gone.
	events := feed([2]string{"b", "/dev/ttys002"})
	if tty, known := CurrentTTY(events, "a"); tty != "" || known {
		t.Fatalf("currentTTY = (%q, %v), want (\"\", false)", tty, known)
	}
}

func TestReachForRefusesAClosedTab(t *testing.T) {
	events := feed([2]string{"a", "/dev/ttys003"})
	at := For(events, map[string]bool{}, "a") // no agent on any terminal
	if !at.Gone {
		t.Fatal("gone = false; the tab holds no agent, so nothing can receive this")
	}
	if at.Reason == "" {
		t.Fatal("a refusal with no reason gives the user nothing to act on")
	}
}

func TestReachForRefusesAStolenTab(t *testing.T) {
	events := feed([2]string{"a", "/dev/ttys002"}, [2]string{"b", "/dev/ttys002"})
	at := For(events, map[string]bool{"/dev/ttys002": true}, "a")
	if !at.Gone || at.TTY != "" {
		t.Fatalf("reachFor = %+v, want gone with no tty: ttys002 belongs to b", at)
	}
}

func TestReachForQueuesWhenNothingIsKnown(t *testing.T) {
	// No evidence either way: queue it rather than claim it failed.
	at := For(feed(), map[string]bool{}, "a")
	if at.Gone || at.TTY != "" {
		t.Fatalf("reachFor = %+v, want the neutral zero value", at)
	}
}

func TestReachForTrustsTTYWhenProcessListIsUnreadable(t *testing.T) {
	// ps failing must not make every agent look dead.
	events := feed([2]string{"a", "/dev/ttys003"})
	at := For(events, nil, "a")
	if at.Gone || at.TTY != "/dev/ttys003" {
		t.Fatalf("reachFor = %+v, want delivery to /dev/ttys003", at)
	}
}

func TestReachForDeliversToALiveTab(t *testing.T) {
	events := feed([2]string{"a", "/dev/ttys003"})
	at := For(events, map[string]bool{"/dev/ttys003": true}, "a")
	if at.Gone || at.TTY != "/dev/ttys003" {
		t.Fatalf("reachFor = %+v, want delivery to /dev/ttys003", at)
	}
}
