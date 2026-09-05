package agentexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Discovery: which of the agent CLIs this library can drive are actually on
// this machine, and how to drive each one.
//
// It answers one narrow question — which binaries exist — and deliberately not
// the more useful one, which is whether the account behind a binary can still
// run anything. On the machine this was written on all four are on PATH and
// exactly one works; the other three are installed with an expired login or an
// ineligible account, and say so only when actually run. There is no cheap
// probe for that — the only honest test is a real, billable turn — so
// discovery reports presence, and the caller reports the exit code and the
// output it got back. A capability guessed from a PATH lookup would be worse
// than no capability at all, because the guess is what a model would repeat to
// the user.

// Installed is one agent CLI found on this machine.
type Installed struct {
	Name      string `json:"name"`              // a key of builtins ("claude", "codex", ...), or an alias
	Binary    string `json:"binary"`            // resolved absolute path
	Version   string `json:"version,omitempty"` // best effort; "" when it could not be asked
	Streaming bool   `json:"streaming"`
	Resume    bool   `json:"resume"`
}

// versionTimeout bounds the one subprocess discovery is allowed to start.
// `--version` is the cheapest thing these CLIs do, but "cheap" here still
// means booting a Node runtime, and a wedged install must not be able to hang
// a caller that is only building a tool list.
const versionTimeout = 2 * time.Second

// dialect names the provider that knows a CLI's flags and its JSON.
type dialect string

const (
	dialectClaude   dialect = "claude"
	dialectCodex    dialect = "codex"
	dialectGemini   dialect = "gemini"
	dialectCursor   dialect = "cursor-agent"
	dialectQwen     dialect = "qwen"
	dialectKimi     dialect = "kimi"
	dialectOpencode dialect = "opencode"
	dialectCopilot  dialect = "copilot"
	dialectGoose    dialect = "goose"
	dialectPi       dialect = "pi"
	dialectAgy      dialect = "agy"
	dialectHermes   dialect = "hermes"
	dialectAider    dialect = "aider"
)

// traits are the two things about an agent a caller can act on before running
// it: whether output arrives as it is produced, and whether a session id can
// be handed back later.
type traits struct {
	dialect   dialect
	streaming bool
	resume    bool
}

// builtins is the set of CLIs this library has a provider for. resume is true
// only where SessionID() can actually hand an id back: gemini's stream-json
// carries none, kimi prints none, aider has no sessions, so those are false
// rather than optimistic.
var builtins = map[string]traits{
	"claude":       {dialect: dialectClaude, streaming: true, resume: true},
	"codex":        {dialect: dialectCodex, streaming: true, resume: true},
	"gemini":       {dialect: dialectGemini, streaming: true, resume: false},
	"cursor-agent": {dialect: dialectCursor, streaming: true, resume: true},
	"qwen":         {dialect: dialectQwen, streaming: true, resume: true},
	"kimi":         {dialect: dialectKimi, streaming: true, resume: false},
	"opencode":     {dialect: dialectOpencode, streaming: true, resume: true},
	"copilot":      {dialect: dialectCopilot, streaming: true, resume: true},
	"goose":        {dialect: dialectGoose, streaming: true, resume: true},
	"pi":           {dialect: dialectPi, streaming: true, resume: true},
	"agy":          {dialect: dialectAgy, streaming: true, resume: true},
	"hermes":       {dialect: dialectHermes, streaming: true, resume: true},
	"aider":        {dialect: dialectAider, streaming: true, resume: false},
}

// dialectKeys is the substring pass of resolveTraits, in match order. Order
// matters where one name is inside another: "copilot" contains "pi", so
// "pi" must come after it or every copilot alias would be driven as pi.
var dialectKeys = []string{
	"cursor", "claude", "codex", "gemini", "qwen", "kimi", "opencode",
	"copilot", "goose", "hermes", "aider", "agy", "pi",
}

// constructors builds the provider for a dialect.
var constructors = map[dialect]func(...Option) Provider{
	dialectClaude:   NewClaude,
	dialectCodex:    NewCodex,
	dialectGemini:   NewGemini,
	dialectCursor:   NewCursor,
	dialectQwen:     NewQwen,
	dialectKimi:     NewKimi,
	dialectOpencode: NewOpencode,
	dialectCopilot:  NewCopilot,
	dialectGoose:    NewGoose,
	dialectPi:       NewPi,
	dialectAgy:      NewAgy,
	dialectHermes:   NewHermes,
	dialectAider:    NewAider,
}

// Discover finds the agent CLIs on PATH. overrides maps a name to an explicit
// binary path and also admits names that are not built in, as long as they
// can be placed onto a dialect (see resolveTraits).
//
// An overridden name that cannot be placed is dropped rather than listed:
// everything Discover returns must be something RegistryFrom can build a
// runner for, or the list becomes a promise this library cannot keep.
func Discover(overrides map[string]string) []Installed {
	names := make([]string, 0, len(builtins)+len(overrides))
	for name := range builtins {
		names = append(names, name)
	}
	for name := range overrides {
		if _, known := builtins[name]; !known {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]Installed, 0, len(names))
	for _, name := range names {
		binary, ok := resolveBinary(name, overrides[name])
		if !ok {
			continue
		}
		t, ok := resolveTraits(name, binary)
		if !ok {
			continue
		}
		out = append(out, Installed{
			Name:      name,
			Binary:    binary,
			Version:   probeVersion(binary),
			Streaming: t.streaming,
			Resume:    t.resume,
		})
	}
	return out
}

// RegistryFrom builds a Registry holding one provider per discovered CLI, each
// bound to its resolved binary under its discovered name. opts are applied to
// every provider after those two, so a caller can add its own — an MCP config
// filename, a base env — without this library having an opinion about them.
func RegistryFrom(installed []Installed, opts ...Option) *Registry {
	reg := NewRegistry()
	for _, a := range installed {
		t, ok := resolveTraits(a.Name, a.Binary)
		if !ok {
			continue
		}
		all := append([]Option{WithName(a.Name), WithBinary(a.Binary)}, opts...)
		if build, ok := constructors[t.dialect]; ok {
			reg.Register(build(all...))
		}
	}
	return reg
}

// resolveBinary turns a name plus an optional override into an absolute path.
//
// The override is looked up the same way a bare name is, so it can be a path,
// a relative path, or another name on PATH. It is not trusted to exist: a
// stale entry in someone's config should drop that agent off the list, not
// produce an Installed whose Binary points at nothing.
func resolveBinary(name, override string) (string, bool) {
	candidate := strings.TrimSpace(override)
	if candidate == "" {
		candidate = name
	}
	path, err := exec.LookPath(candidate)
	if err != nil {
		// LookPath only searches PATH for bare names; an explicit path that is
		// not executable fails here too, which is the answer we want.
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return "", false
	}
	return abs, true
}

// resolveTraits decides which CLI a name is.
//
// The name wins, then the binary's own base name, then a substring of either.
// The substring pass is what makes an override useful for the case it actually
// gets used for: the same CLI under a second name — a beta build, a wrapper
// script, two accounts — where "claude-work" pointed at a claude is still
// claude and still speaks stream-json. A name that matches nothing is reported
// as unknown, because inventing a dialect for a CLI this library has never
// seen would mean parsing its output as somebody else's JSON.
func resolveTraits(name, binary string) (traits, bool) {
	if t, ok := builtins[name]; ok {
		return t, true
	}
	if t, ok := builtins[filepath.Base(binary)]; ok {
		return t, true
	}
	haystack := strings.ToLower(name + " " + filepath.Base(binary))
	for _, key := range dialectKeys {
		if !strings.Contains(haystack, key) {
			continue
		}
		if key == "cursor" {
			return builtins["cursor-agent"], true
		}
		return builtins[key], true
	}
	return traits{}, false
}

// probeVersion asks a binary what version it is. Best effort by construction:
// any failure — missing flag, expired login printed to stderr, a CLI that
// needs 20 seconds to boot — yields "" and no error, because a version string
// is a nicety and discovery must not be able to fail because of one.
func probeVersion(binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--version")
	// A nil Stdin is /dev/null to os/exec. It matters: these CLIs read stdin
	// when they cannot tell they are unattended, and a probe that blocks on
	// input nobody is going to type would outlive its own timeout.
	cmd.Stdin = nil
	// The timeout alone does not bound this. CommandContext signals the
	// process it started and nothing else, while Output waits for the stdout
	// pipe to close — so a CLI whose wrapper script forks something that
	// outlives it holds the probe open long past the deadline. A run that
	// should have taken 2 seconds took 30. WaitDelay is the answer: closes the
	// pipes and gives up.
	cmd.WaitDelay = versionTimeout
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if len(line) > 120 {
			line = line[:120]
		}
		return line
	}
	return ""
}
