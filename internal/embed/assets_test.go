package gatewayembed_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gatewayembed "otto-gateway/internal/embed"
)

func TestGatewayDirPrefersGWHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway-home")
	t.Setenv("GW_HOME", "  "+root+"  ")

	got, err := gatewayembed.GatewayDir()
	if err != nil {
		t.Fatalf("GatewayDir: %v", err)
	}
	if got != root {
		t.Fatalf("GatewayDir = %q, want %q", got, root)
	}
}

func TestGatewayDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("GW_HOME", "")

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	got, err := gatewayembed.GatewayDir()
	if err != nil {
		t.Fatalf("GatewayDir: %v", err)
	}
	want := filepath.Join(configDir, "gateway")
	if got != want {
		t.Fatalf("GatewayDir = %q, want %q", got, want)
	}
}

func TestEnsureACPProxyCreatesToolLessAgent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	path, created, err := gatewayembed.EnsureACPProxy(root)
	if err != nil {
		t.Fatalf("EnsureACPProxy: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	wantPath := filepath.Join(root, ".kiro", "agents", "acp_proxy.json")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat agent file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("agent file mode = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat agent directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o750 {
		t.Errorf("agent directory mode = %04o, want 0750", got)
	}
	var cfg struct {
		Name           string         `json:"name"`
		Description    string         `json:"description"`
		Prompt         any            `json:"prompt"`
		MCPServers     map[string]any `json:"mcpServers"`
		Tools          []string       `json:"tools"`
		ToolAliases    map[string]any `json:"toolAliases"`
		AllowedTools   []string       `json:"allowedTools"`
		Resources      []string       `json:"resources"`
		Hooks          map[string]any `json:"hooks"`
		ToolsSettings  map[string]any `json:"toolsSettings"`
		IncludeMCPJSON bool           `json:"includeMcpJson"`
		Model          any            `json:"model"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Name != "acp_proxy" {
		t.Errorf("name = %q, want acp_proxy", cfg.Name)
	}
	if cfg.Description == "" {
		t.Error("description is empty")
	}
	if cfg.Prompt != nil || cfg.Model != nil {
		t.Errorf("prompt/model = %v/%v, want nil/nil", cfg.Prompt, cfg.Model)
	}
	if cfg.MCPServers == nil || len(cfg.MCPServers) != 0 ||
		cfg.Tools == nil || len(cfg.Tools) != 0 ||
		cfg.ToolAliases == nil || len(cfg.ToolAliases) != 0 ||
		cfg.AllowedTools == nil || len(cfg.AllowedTools) != 0 ||
		cfg.Resources == nil || len(cfg.Resources) != 0 ||
		cfg.Hooks == nil || len(cfg.Hooks) != 0 ||
		cfg.ToolsSettings == nil || len(cfg.ToolsSettings) != 0 ||
		cfg.IncludeMCPJSON {
		t.Fatalf("agent is not explicitly tool-less: %+v", cfg)
	}
}

func TestEnsureACPProxyPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := gatewayembed.ACPProxyPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const custom = `{"name":"user-customized"}`
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}

	gotPath, created, err := gatewayembed.EnsureACPProxy(root)
	if err != nil {
		t.Fatalf("EnsureACPProxy: %v", err)
	}
	if created {
		t.Fatal("created = true, want false")
	}
	body, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != custom {
		t.Fatalf("existing file was changed: %q", body)
	}
}

func TestEnsureACPProxyRejectsNonRegularTarget(t *testing.T) {
	root := t.TempDir()
	path := gatewayembed.ACPProxyPath(root)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := gatewayembed.EnsureACPProxy(root); err == nil {
		t.Fatal("EnsureACPProxy returned nil error for directory target")
	}
}
