package cliagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
)

// mcpFileName is the per-plugin MCP server definition file.
const mcpFileName = ".mcp.json"

// mcpConfigFileName is the merged config written for the CLI's --mcp-config flag.
const mcpConfigFileName = "mcp-config.json"

var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// loadPluginMCPServers reads each plugin's .mcp.json and merges its "mcpServers"
// into a single map keyed by server name. Plugins without a .mcp.json are
// skipped; later plugins override earlier ones on name conflicts.
func loadPluginMCPServers(plugins []PluginRef) (map[string]any, error) {
	merged := map[string]any{}
	for _, pl := range plugins {
		path := filepath.Join(pl.Path, mcpFileName)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cliagent: reading %s: %w", path, err)
		}
		var doc struct {
			MCPServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("cliagent: parsing %s: %w", path, err)
		}
		maps.Copy(merged, doc.MCPServers)
	}
	return merged, nil
}

// resolveRefs recursively replaces ${VAR} references in string values using env.
// Unknown variables are left as their literal ${VAR} form.
func resolveRefs(servers map[string]any, env map[string]string) map[string]any {
	out, _ := resolveValue(servers, env).(map[string]any)
	return out
}

func resolveValue(v any, env map[string]string) any {
	switch t := v.(type) {
	case string:
		return envRefPattern.ReplaceAllStringFunc(t, func(match string) string {
			name := envRefPattern.FindStringSubmatch(match)[1]
			if val, ok := env[name]; ok {
				return val
			}
			return match
		})
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = resolveValue(val, env)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = resolveValue(val, env)
		}
		return out
	default:
		return v
	}
}

// writeMCPConfig writes {"mcpServers": servers} into dir and returns its path.
func writeMCPConfig(dir string, servers map[string]any) (string, error) {
	doc := map[string]any{"mcpServers": servers}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, mcpConfigFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// WritePluginMCPConfig loads the plugins' MCP servers, resolves ${VAR} refs
// against env, writes a merged config into outDir, and returns its path. If
// there are no servers to write, it returns an empty path and no error.
func WritePluginMCPConfig(plugins []PluginRef, env map[string]string, outDir string) (string, error) {
	servers, err := loadPluginMCPServers(plugins)
	if err != nil {
		return "", err
	}
	if len(servers) == 0 {
		return "", nil
	}
	return writeMCPConfig(outDir, resolveRefs(servers, env))
}
