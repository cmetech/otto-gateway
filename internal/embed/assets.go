// Package gatewayembed owns binary-embedded runtime assets materialized into
// the gateway-controlled workspace.
package gatewayembed

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed acp_proxy.json
var acpProxyJSON []byte

// GatewayDir returns the gateway-owned persistent directory. It follows the
// same GW_HOME → UserConfigDir/gateway precedence used by gateway identity
// persistence in cmd/otto-gateway.
func GatewayDir() (string, error) {
	if root := strings.TrimSpace(os.Getenv("GW_HOME")); root != "" {
		return filepath.Clean(root), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("derive gateway home: %w (set GW_HOME or KIRO_CWD)", err)
	}
	return filepath.Join(configDir, "gateway"), nil
}

// ACPProxyPath returns the workspace-local path at which Kiro discovers the
// embedded acp_proxy custom agent.
func ACPProxyPath(root string) string {
	return filepath.Join(root, ".kiro", "agents", "acp_proxy.json")
}

// EnsureACPProxy materializes the embedded agent when absent. Existing regular
// files are always preserved so operator customizations are never clobbered.
func EnsureACPProxy(root string) (path string, created bool, err error) {
	path = ACPProxyPath(root)
	if exists, inspectErr := regularFileExists(path); inspectErr != nil {
		return path, false, inspectErr
	} else if exists {
		return path, false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return path, false, fmt.Errorf("create acp_proxy agent directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		exists, inspectErr := regularFileExists(path)
		if inspectErr != nil {
			return path, false, inspectErr
		}
		if exists {
			return path, false, nil
		}
	}
	if err != nil {
		return path, false, fmt.Errorf("create acp_proxy agent: %w", err)
	}

	if _, err := file.Write(acpProxyJSON); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return path, false, fmt.Errorf("write acp_proxy agent: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return path, false, fmt.Errorf("close acp_proxy agent: %w", err)
	}
	return path, true, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect acp_proxy agent: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("acp_proxy agent path %q exists but is not a regular file", path)
	}
	return true, nil
}
