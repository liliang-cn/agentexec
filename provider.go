package agentexec

import (
	"errors"
	"fmt"
	"maps"
	"sort"
)

// ErrUnsupportedMode is returned by BuildCommand when Request.Mode is not allowed.
var ErrUnsupportedMode = errors.New("agentexec: unsupported mode")

// Registry maps provider names to Providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds (or overwrites) a provider keyed by its Name().
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
}

// Get returns the provider registered under name, or an error if absent.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("agentexec: no provider registered for %q", name)
	}
	return p, nil
}

// Names returns the registered provider names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// providerConfig holds resolved construction options shared by all providers.
type providerConfig struct {
	name         string
	binary       string
	baseEnv      map[string]string
	modelEnv     string   // env key to source the model value from (emitted as the provider's model flag)
	mcpFilename  string   // merged MCP config filename written under WorkspacePath
	strictMCP    bool     // append the provider's strict-mcp flag when a config is written
	allowedModes []string // when non-empty, BuildCommand validates Request.Mode against this
	argv         []string // text providers: argv template, "{prompt}" marks where the prompt goes
	modelFlag    string   // text providers: flag the model is passed behind, "" for none
}

// Option configures a provider constructor.
type Option func(*providerConfig)

// WithBinary overrides the CLI binary path/name.
func WithBinary(path string) Option { return func(c *providerConfig) { c.binary = path } }

// WithBaseEnv sets a base environment applied to every command.
func WithBaseEnv(env map[string]string) Option {
	return func(c *providerConfig) {
		c.baseEnv = make(map[string]string, len(env))
		maps.Copy(c.baseEnv, env)
	}
}

// WithModelEnv names the Request.Env key to source the model value from. The
// resolved value is emitted as the provider's model flag (e.g. claude --model).
func WithModelEnv(key string) Option { return func(c *providerConfig) { c.modelEnv = key } }

// WithName overrides the registered provider name.
func WithName(name string) Option { return func(c *providerConfig) { c.name = name } }

// WithMCPConfig sets the merged MCP config filename written under WorkspacePath
// and whether to append the provider's strict-mcp flag.
func WithMCPConfig(filename string, strict bool) Option {
	return func(c *providerConfig) { c.mcpFilename = filename; c.strictMCP = strict }
}

// WithAllowedModes restricts Request.Mode; an unlisted mode yields ErrUnsupportedMode.
func WithAllowedModes(modes []string) Option {
	return func(c *providerConfig) { c.allowedModes = append([]string(nil), modes...) }
}

// WithArgv sets a text provider's argument template. The argument containing
// "{prompt}" has the prompt substituted in; without one the prompt is appended
// last. Dialect providers know their own argv and ignore this.
func WithArgv(args ...string) Option {
	return func(c *providerConfig) { c.argv = append([]string(nil), args...) }
}

// WithModelFlag names the flag a text provider passes the model behind, as in
// WithModelFlag("--model"). Without one the model is dropped, because a text
// CLI has no flag this library could guess.
func WithModelFlag(flag string) Option { return func(c *providerConfig) { c.modelFlag = flag } }

// resolveOptions applies opts onto a config defaulting name+binary to def.
func resolveOptions(def string, opts []Option) providerConfig {
	cfg := providerConfig{name: def, binary: def, baseEnv: map[string]string{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
