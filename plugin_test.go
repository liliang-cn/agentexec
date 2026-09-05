package agentexec

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMCP(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPluginMCPServersMergesAndResolves(t *testing.T) {
	p1 := t.TempDir()
	writeMCP(t, p1, `{"mcpServers":{"a":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/a","env":{"K":"${TOKEN}"}}}}`)
	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p1", Path: p1}}, map[string]string{"TOKEN": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	a := servers["a"].(map[string]any)
	if a["command"] != p1+"/bin/a" {
		t.Fatalf("CLAUDE_PLUGIN_ROOT not resolved: %v", a["command"])
	}
	if a["env"].(map[string]any)["K"] != "secret" {
		t.Fatalf("TOKEN not resolved: %v", a["env"])
	}
}

func TestLoadPluginMCPServersMissingDirIsError(t *testing.T) {
	if _, err := loadPluginMCPServers([]PluginRef{{Name: "x", Path: "/no/such/dir"}}, nil); err == nil {
		t.Fatal("missing plugin dir should error")
	}
}

func TestLoadPluginMCPServersEmptyPathIsError(t *testing.T) {
	if _, err := loadPluginMCPServers([]PluginRef{{Name: "x", Path: ""}}, nil); err == nil {
		t.Fatal("empty path should error")
	}
}

func TestLoadPluginMCPServersNoMCPFileSkips(t *testing.T) {
	servers, err := loadPluginMCPServers([]PluginRef{{Name: "p", Path: t.TempDir()}}, nil)
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers=%v err=%v", servers, err)
	}
}

func TestResolveRefsUppercaseOnly(t *testing.T) {
	// lowercase ${token} must NOT be substituted (uppercase-only pattern).
	got := resolveRefs(map[string]any{"c": "${token}-${UP}"}, map[string]string{"token": "x", "UP": "y"})
	if got.(map[string]any)["c"] != "${token}-y" {
		t.Fatalf("got %v", got.(map[string]any)["c"])
	}
}

func TestWriteMCPConfigUsesFilenameAndAbsPath(t *testing.T) {
	dir := t.TempDir()
	path, err := writeMCPConfig(dir, ".aas-mcp.json", map[string]any{"a": map[string]any{"command": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != ".aas-mcp.json" || !filepath.IsAbs(path) {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
