//go:build darwin

package main

import "testing"

// TestEscapeApplescript verifies the escape-set contract for escapeApplescript
// per QUAL-01 / D-20-01:
//   - " and \ are prefixed with a backslash.
//   - Raw 0x0A/0x0D/0x09 are translated into the two-byte AS escape sequences
//     \n, \r, \t (backslash + n/r/t).
//   - Other C0 bytes (0x00..0x1F excluding 0x09/0x0A/0x0D) and DEL (0x7F) are
//     stripped entirely.
//   - All other bytes (including >= 0x80) pass through unchanged.
func TestEscapeApplescript(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain_ascii", in: "hello world", want: "hello world"},
		{name: "double_quote", in: `say "hi"`, want: `say \"hi\"`},
		{name: "backslash", in: `path\to\file`, want: `path\\to\\file`},
		{name: "newline_to_AS_escape", in: "a\nb", want: `a\nb`},
		{name: "carriage_return_to_AS_escape", in: "a\rb", want: `a\rb`},
		{name: "tab_to_AS_escape", in: "a\tb", want: `a\tb`},
		{name: "strip_nul", in: "a\x00b", want: "ab"},
		{name: "strip_1f", in: "a\x1fb", want: "ab"},
		{name: "strip_del_7f", in: "a\x7fb", want: "ab"},
		{name: "high_byte_passthrough", in: "a\xc3\xa9b", want: "a\xc3\xa9b"},
		{
			name: "mixed",
			in:   "\"line1\nline2\twith \\\"quote\\\"\x00trailing\"",
			want: `\"line1\nline2\twith \\\"quote\\\"trailing\"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := escapeApplescript(tc.in)
			if got != tc.want {
				t.Fatalf("escapeApplescript(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
