// Package toolcontract defines request-scoped negotiation metadata shared by
// the Gateway's HTTP surfaces.
package toolcontract

import (
	"errors"
	"strings"
)

const (
	HeaderContract = "X-Otto-Tool-Contract"
	HeaderCallRole = "X-Otto-Call-Role"
	VersionV1      = "v1"
)

// ErrUnsupportedVersion indicates a non-empty contract version outside the
// Gateway's exact allowlist.
var ErrUnsupportedVersion = errors.New("unsupported tool contract version")

// Metadata is bounded request-scoped contract and diagnostic metadata.
type Metadata struct {
	Version  string
	CallRole string
}

// Parse validates the contract version and normalizes the diagnostic call
// role. Contract versions are exact after surrounding whitespace is removed.
func Parse(version, role string) (Metadata, error) {
	version = strings.TrimSpace(version)
	if version != "" && version != VersionV1 {
		return Metadata{}, ErrUnsupportedVersion
	}

	return Metadata{
		Version:  version,
		CallRole: normalizeCallRole(role),
	}, nil
}

func normalizeCallRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "primary", "post_tool", "correction", "title", "compression", "auxiliary":
		return role
	default:
		return "unknown"
	}
}
