package toolcontract

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestParseContractVersion(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		wantVersion string
		wantErr     bool
	}{
		{name: "absent preserves legacy behavior"},
		{name: "empty whitespace preserves legacy behavior", version: " \t\n "},
		{name: "exact v1 enables contract", version: "v1", wantVersion: VersionV1},
		{name: "surrounding whitespace is ignored", version: "  v1\t", wantVersion: VersionV1},
		{name: "value comparison is case sensitive", version: "V1", wantErr: true},
		{name: "unsupported version fails closed", version: "v2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.version, "")
			if tt.wantErr {
				if !errors.Is(err, ErrUnsupportedVersion) {
					t.Fatalf("Parse() error = %v, want ErrUnsupportedVersion", err)
				}
				if got != (Metadata{}) {
					t.Fatalf("Parse() metadata = %+v, want zero value on error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got.Version != tt.wantVersion {
				t.Errorf("Parse() Version = %q, want %q", got.Version, tt.wantVersion)
			}
		})
	}
}

func TestParseContractCallRoleAllowlist(t *testing.T) {
	tests := []struct {
		name string
		role string
		want string
	}{
		{name: "absent", want: "unknown"},
		{name: "primary", role: "primary", want: "primary"},
		{name: "post tool", role: "post_tool", want: "post_tool"},
		{name: "correction", role: "correction", want: "correction"},
		{name: "title", role: "title", want: "title"},
		{name: "compression", role: "compression", want: "compression"},
		{name: "auxiliary", role: "auxiliary", want: "auxiliary"},
		{name: "trimmed and normalized", role: "  POST_TOOL\t", want: "post_tool"},
		{name: "unknown is bounded", role: "private-operation-name", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse("v1", tt.role)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got.CallRole != tt.want {
				t.Errorf("Parse() CallRole = %q, want %q", got.CallRole, tt.want)
			}
		})
	}
}

func TestParseContractUnsupportedErrorDoesNotEchoValue(t *testing.T) {
	const privateValue = "private-version-canary"

	_, err := Parse(privateValue, "primary")
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Parse() error = %v, want ErrUnsupportedVersion", err)
	}
	if strings.Contains(err.Error(), privateValue) {
		t.Fatalf("Parse() error exposed contract value: %q", err)
	}
}

func TestParseHeadersRejectsDuplicateContractValues(t *testing.T) {
	for _, versions := range [][]string{
		{"v1", "v2"},
		{"v2", "v1"},
		{"v1", "v1"},
		{"", "v1"},
	} {
		header := make(http.Header)
		for _, version := range versions {
			header.Add(HeaderContract, version)
		}

		got, err := ParseHeaders(header)
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("ParseHeaders(%q) error = %v, want ErrUnsupportedVersion", versions, err)
		}
		if got != (Metadata{}) {
			t.Errorf("ParseHeaders(%q) metadata = %+v, want zero value", versions, got)
		}
	}
}

func TestParseHeadersPreservesSingleValueAndDiagnosticCallRole(t *testing.T) {
	tests := []struct {
		name        string
		header      http.Header
		wantVersion string
		wantRole    string
	}{
		{name: "absent", header: make(http.Header), wantRole: "unknown"},
		{name: "single empty", header: http.Header{HeaderContract: []string{""}}, wantRole: "unknown"},
		{name: "single v1", header: http.Header{HeaderContract: []string{"v1"}}, wantVersion: VersionV1, wantRole: "unknown"},
		{
			name: "duplicate diagnostic role does not block",
			header: http.Header{
				HeaderContract: []string{"v1"},
				HeaderCallRole: []string{"post_tool", "private-operation-name"},
			},
			wantVersion: VersionV1,
			wantRole:    "post_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHeaders(tt.header)
			if err != nil {
				t.Fatalf("ParseHeaders() error = %v", err)
			}
			if got.Version != tt.wantVersion || got.CallRole != tt.wantRole {
				t.Errorf("ParseHeaders() = %+v, want {Version:%q CallRole:%q}", got, tt.wantVersion, tt.wantRole)
			}
		})
	}
}

func TestContractHeaderConstants(t *testing.T) {
	if HeaderContract != "X-Otto-Tool-Contract" {
		t.Errorf("HeaderContract = %q", HeaderContract)
	}
	if HeaderCallRole != "X-Otto-Call-Role" {
		t.Errorf("HeaderCallRole = %q", HeaderCallRole)
	}
}
