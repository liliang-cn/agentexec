package cliagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writePluginMCP(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPluginMCPServersMerges(t *testing.T) {
	p1 := t.TempDir()
	p2 := t.TempDir()
	writePluginMCP(t, p1, `{"mcpServers":{"a":{"command":"a-cmd"}}}`)
	writePluginMCP(t, p2, `{"mcpServers":{"b":{"command":"b-cmd"}}}`)

	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}, {Name: "p2", Path: p2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers["a"] == nil || servers["b"] == nil {
		t.Fatalf("merged servers = %v, want a and b", servers)
	}
}

func TestLoadPluginMCPServersSkipsMissingFile(t *testing.T) {
	p1 := t.TempDir() // no .mcp.json
	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}})
	if err != nil {
		t.Fatalf("missing .mcp.json should not error: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("want no servers, got %v", servers)
	}
}

func TestLoadPluginMCPServersLaterOverrides(t *testing.T) {
	p1 := t.TempDir()
	p2 := t.TempDir()
	writePluginMCP(t, p1, `{"mcpServers":{"x":{"command":"old"}}}`)
	writePluginMCP(t, p2, `{"mcpServers":{"x":{"command":"new"}}}`)
	servers, _ := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}, {Name: "p2", Path: p2}})
	x := servers["x"].(map[string]any)
	if x["command"] != "new" {
		t.Fatalf("command = %v, want 'new' (later plugin wins)", x["command"])
	}
}

func TestLoadPluginMCPServersBadJSONErrors(t *testing.T) {
	p1 := t.TempDir()
	writePluginMCP(t, p1, `{not json`)
	if _, err := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}}); err == nil {
		t.Fatal("bad JSON should error")
	}
}

func TestResolveRefsSubstitutesEnv(t *testing.T) {
	servers := map[string]any{
		"a": map[string]any{
			"command": "run",
			"env":     map[string]any{"TOKEN": "${API_KEY}"},
			"args":    []any{"--url", "${BASE}/v1"},
		},
	}
	resolved := resolveRefs(servers, map[string]string{"API_KEY": "secret", "BASE": "http://x"})
	a := resolved["a"].(map[string]any)
	if a["env"].(map[string]any)["TOKEN"] != "secret" {
		t.Fatalf("TOKEN not resolved: %v", a["env"])
	}
	if a["args"].([]any)[1] != "http://x/v1" {
		t.Fatalf("arg not resolved: %v", a["args"])
	}
}

func TestResolveRefsLeavesUnknownLiteral(t *testing.T) {
	servers := map[string]any{"a": map[string]any{"command": "${MISSING}"}}
	resolved := resolveRefs(servers, map[string]string{})
	a := resolved["a"].(map[string]any)
	if a["command"] != "${MISSING}" {
		t.Fatalf("unknown var should stay literal, got %v", a["command"])
	}
}

func TestWriteMCPConfigProducesReadableFile(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]any{"a": map[string]any{"command": "x"}}
	path, err := writeMCPConfig(dir, servers)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MCPServers["a"] == nil {
		t.Fatalf("written config missing server a: %s", data)
	}
}

func TestWritePluginMCPConfigEndToEnd(t *testing.T) {
	p1 := t.TempDir()
	out := t.TempDir()
	writePluginMCP(t, p1, `{"mcpServers":{"a":{"command":"run","env":{"K":"${V}"}}}}`)

	path, err := WritePluginMCPConfig([]PluginRef{{Name: "p1", Path: p1}}, map[string]string{"V": "val"}, out)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !contains(string(data), `"val"`) {
		t.Fatalf("env var not resolved in output: %s", data)
	}
}

func TestWritePluginMCPConfigNoPluginsReturnsEmpty(t *testing.T) {
	path, err := WritePluginMCPConfig(nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("no plugins should yield empty path, got %q", path)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
