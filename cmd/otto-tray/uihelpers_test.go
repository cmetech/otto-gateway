//go:build darwin || windows

package main

import "testing"

// Fixtures matching the exact informational lines the wrappers emit on
// every invocation: scripts/gw load_config and scripts/gw.ps1
// Initialize-Config. Note the two spaces after "loaded overrides:".
const (
	fixtureEnvLine       = `loaded env file: C:\Users\you\.gw\.env`
	fixtureOverridesLine = `loaded overrides:  C:\Users\you\.gw\overrides.env`
)

func TestFirstMeaningfulLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "informational only falls back to last non-empty line",
			in:   fixtureEnvLine + "\n" + fixtureOverridesLine + "\n",
			want: fixtureOverridesLine,
		},
		{
			name: "informational preamble is skipped in favour of the real error",
			in:   fixtureEnvLine + "\n" + fixtureOverridesLine + "\nkiro: command not found\n",
			want: "kiro: command not found",
		},
		{
			name: "real error on line one is returned unchanged",
			in:   "bind: address already in use\nlisten tcp 127.0.0.1:11434\n",
			want: "bind: address already in use",
		},
		{
			name: "empty stderr",
			in:   "",
			want: "(no stderr)",
		},
		{
			name: "whitespace only stderr",
			in:   "  \n\t\n   \t  \n",
			want: "(no stderr)",
		},
		{
			name: "CRLF informational preamble then error",
			in:   fixtureEnvLine + "\r\n" + fixtureOverridesLine + "\r\nkiro: command not found\r\n",
			want: "kiro: command not found",
		},
		{
			name: "blank lines interleaved between informational line and error",
			in:   "\n" + fixtureEnvLine + "\n\n\nkiro: command not found\n",
			want: "kiro: command not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstMeaningfulLine(tc.in); got != tc.want {
				t.Errorf("firstMeaningfulLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
