package agentexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const pluginMCPFile = ".mcp.json"

// loadPluginMCPServers reads each plugin's .mcp.json (if present), resolves
// ${CLAUDE_PLUGIN_ROOT} and ${ENV_VAR} references against env, and returns a
// merged mcpServers map. A missing plugin directory (or empty Path) is an error;
// a plugin without .mcp.json contributes nothing. Last write wins.
func loadPluginMCPServers(plugins []PluginRef, env map[string]string) (map[string]any, error) {
	merged := map[string]any{}
	for _, p := range plugins {
		if p.Path == "" {
			return nil, fmt.Errorf("plugin %q has empty Path", p.Name)
		}
		info, err := os.Stat(p.Path)
		if err != nil {
			return nil, fmt.Errorf("plugin %q at %s: %w", p.Name, p.Path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("plugin %q at %s: not a directory", p.Name, p.Path)
		}
		raw, err := os.ReadFile(filepath.Join(p.Path, pluginMCPFile))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s/.mcp.json: %w", p.Path, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s/.mcp.json: %w", p.Path, err)
		}
		servers, _ := parsed["mcpServers"].(map[string]any)
		if servers == nil {
			continue
		}
		vars := make(map[string]string, len(env)+1)
		for k, v := range env {
			vars[k] = v
		}
		vars["CLAUDE_PLUGIN_ROOT"] = p.Path
		resolved := resolveRefs(servers, vars).(map[string]any)
		for name, server := range resolved {
			merged[name] = server
		}
	}
	return merged, nil
}

// refPattern matches ${VAR_NAME} (uppercase + underscore only).
var refPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// resolveRefs walks a parsed JSON value substituting ${VAR} references in
// strings in place. Unknown variables stay literal.
func resolveRefs(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		return refPattern.ReplaceAllStringFunc(x, func(m string) string {
			key := m[2 : len(m)-1]
			if val, ok := vars[key]; ok {
				return val
			}
			return m
		})
	case map[string]any:
		for k, vv := range x {
			x[k] = resolveRefs(vv, vars)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = resolveRefs(vv, vars)
		}
		return x
	default:
		return v
	}
}

// writeMCPConfig writes {"mcpServers": servers} to dir/filename (0600) and
// returns the absolute path.
func writeMCPConfig(dir, filename string, servers map[string]any) (string, error) {
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, filename)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs, nil
	}
	return p, nil
}

// WritePluginMCPConfig loads + resolves plugin MCP servers and writes a merged
// config into outDir/filename, returning its path (empty if nothing to write).
func WritePluginMCPConfig(plugins []PluginRef, env map[string]string, outDir, filename string) (string, error) {
	servers, err := loadPluginMCPServers(plugins, env)
	if err != nil {
		return "", err
	}
	if len(servers) == 0 {
		return "", nil
	}
	return writeMCPConfig(outDir, filename, servers)
}
