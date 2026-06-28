package cliagent

import (
	"fmt"
	"maps"
	"sort"
)

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
		return nil, fmt.Errorf("cliagent: no provider registered for %q", name)
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
	binary   string
	baseEnv  map[string]string
	modelEnv string
}

// Option configures a provider constructor.
type Option func(*providerConfig)

// WithBinary overrides the CLI binary path/name.
func WithBinary(path string) Option {
	return func(c *providerConfig) { c.binary = path }
}

// WithBaseEnv sets a base environment applied to every command.
func WithBaseEnv(env map[string]string) Option {
	return func(c *providerConfig) {
		c.baseEnv = make(map[string]string, len(env))
		maps.Copy(c.baseEnv, env)
	}
}

// WithModelEnv sets the environment variable name used to pass Request.Model.
func WithModelEnv(key string) Option {
	return func(c *providerConfig) { c.modelEnv = key }
}

// resolveOptions applies opts onto a config defaulting binary to defaultBinary.
func resolveOptions(defaultBinary string, opts []Option) providerConfig {
	cfg := providerConfig{binary: defaultBinary, baseEnv: map[string]string{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
