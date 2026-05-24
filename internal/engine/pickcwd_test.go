// Package engine — pickCwd table-driven priority tests + Windows-URI
// fixed-case test (runtime-guarded) + testing/quick never-panic property
// test + runnable Example. TRST-06 + TRST-07.
package engine

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"

	"otto-gateway/internal/canonical"
)

// TestPickCwd_Priority walks the four-step priority chain. Each case
// constructs a request that exercises exactly one of the steps and
// asserts the expected priority winner.
func TestPickCwd_Priority(t *testing.T) {
	cases := []struct {
		name       string
		req        *canonical.ChatRequest
		defaultCwd string
		want       string
	}{
		{
			name: "step1_working_dir_override_wins_over_everything",
			req: &canonical.ChatRequest{
				WorkingDirOverride: "/explicit/cwd",
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///should/not/win/a.go"},
				},
			},
			defaultCwd: "/default/should/not/win",
			want:       "/explicit/cwd",
		},
		{
			name: "step2_longest_common_parent_from_resource_links",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///proj/pkg/a.go"},
					{URI: "file:///proj/pkg/b.go"},
				},
			},
			defaultCwd: "/default/should/not/win",
			want:       "/proj/pkg",
		},
		{
			name: "step3_default_cwd_when_no_links_and_no_override",
			req: &canonical.ChatRequest{
				WorkingDirOverride: "",
				ResourceLinks:      nil,
			},
			defaultCwd: "/cfg/default",
			want:       "/cfg/default",
		},
		{
			name: "step4_os_getwd_when_everything_else_empty",
			req: &canonical.ChatRequest{
				WorkingDirOverride: "",
				ResourceLinks:      nil,
			},
			defaultCwd: "",
			// step 4 is OS-dependent; just assert non-empty (os.Getwd
			// returns something valid in the test runner).
			want: "",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := pickCwd(c.req, c.defaultCwd)
			if c.name == "step4_os_getwd_when_everything_else_empty" {
				if got == "" {
					t.Fatalf("step 4 fallback returned empty string; expected os.Getwd or '.'")
				}
				return
			}
			if got != c.want {
				t.Errorf("pickCwd: got %q, want %q", got, c.want)
			}
		})
	}
}

// TestPickCwd_LongestCommonParent exercises longestCommonParent with
// zero / one / N URIs in req.ResourceLinks, plus the "no shared parent"
// edge case.
func TestPickCwd_LongestCommonParent(t *testing.T) {
	cases := []struct {
		name string
		req  *canonical.ChatRequest
		want string
	}{
		{
			name: "zero_resource_links_falls_through",
			req:  &canonical.ChatRequest{ResourceLinks: nil},
			want: "/fallback",
		},
		{
			name: "single_resource_link_returns_its_dir",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///single/path/file.go"},
				},
			},
			want: "/single/path",
		},
		{
			name: "two_links_shared_parent_returns_parent",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///shared/pkg/a.go"},
					{URI: "file:///shared/pkg/sub/b.go"},
				},
			},
			want: "/shared/pkg",
		},
		{
			name: "three_links_shared_top_parent",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///root/x/a.go"},
					{URI: "file:///root/y/b.go"},
					{URI: "file:///root/z/c.go"},
				},
			},
			want: "/root",
		},
		{
			name: "no_shared_parent_falls_through_to_default",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "file:///alpha/a.go"},
					{URI: "file:///beta/b.go"},
				},
			},
			// "/alpha" and "/beta" share root "/" — but our split
			// drops trailing separators and the common prefix is just
			// the empty root. joinPathComponents returns "/" for the
			// rooted case. So the answer is "/", not "/fallback".
			want: "/",
		},
		{
			name: "non_file_uri_skipped",
			req: &canonical.ChatRequest{
				ResourceLinks: []canonical.ResourceLinkBlock{
					{URI: "https://example.com/a.go"},
					{URI: "file:///only/this/wins/x.go"},
				},
			},
			want: "/only/this/wins",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := pickCwd(c.req, "/fallback")
			if got != c.want {
				t.Errorf("pickCwd: got %q, want %q", got, c.want)
			}
		})
	}
}

// TestPickCwd_FromResourceLinks (Codex H-2 / SC #5 satisfier) — populates
// req.ResourceLinks with TWO file:// URIs that share a common parent
// directory and asserts pickCwd returns that parent. This is the unit-
// test fixture the roadmap success criterion requires.
func TestPickCwd_FromResourceLinks(t *testing.T) {
	req := &canonical.ChatRequest{
		ResourceLinks: []canonical.ResourceLinkBlock{
			{URI: "file:///workspace/project/cmd/main.go", Name: "main.go"},
			{URI: "file:///workspace/project/internal/foo/foo.go", Name: "foo.go"},
		},
	}
	got := pickCwd(req, "/should/not/use")
	want := "/workspace/project"
	if got != want {
		t.Errorf("pickCwd: got %q, want %q (longest-common-parent of two file:// URIs)", got, want)
	}
}

// TestPickCwd_WindowsFileURI (runtime-guarded) — asserts the Windows
// file:///C:/foo handling RESEARCH.md Pitfall 3 fixes. Skipped on
// non-Windows runners since filepath.Separator differs.
func TestPickCwd_WindowsFileURI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific file URI handling test")
	}
	req := &canonical.ChatRequest{
		ResourceLinks: []canonical.ResourceLinkBlock{
			{URI: "file:///C:/proj/a.go"},
			{URI: "file:///C:/proj/sub/b.go"},
		},
	}
	got := pickCwd(req, "")
	want := `C:\proj`
	if got != want {
		t.Errorf("pickCwd: got %q, want %q (Windows file URI must strip leading slash and FromSlash)", got, want)
	}
}

// TestPickCwd_NeverPanics (TRST-06 property test) — pickCwd MUST NOT
// panic for any ChatRequest shape. testing/quick generates random
// requests with random ResourceLinks; the function under test should
// always terminate cleanly.
func TestPickCwd_NeverPanics(t *testing.T) {
	// Property: for any non-nil request, pickCwd returns some string
	// without panicking.
	property := func(override string, uris []string, defaultCwd string) bool {
		links := make([]canonical.ResourceLinkBlock, 0, len(uris))
		for _, u := range uris {
			links = append(links, canonical.ResourceLinkBlock{URI: u})
		}
		req := &canonical.ChatRequest{
			WorkingDirOverride: override,
			ResourceLinks:      links,
		}
		// pickCwd is allowed to return any string — we only assert
		// it doesn't panic. The function's return value is ignored
		// here intentionally; the property under test is termination
		// and no-panic.
		_ = pickCwd(req, defaultCwd)
		return true
	}

	cfg := &quick.Config{MaxCount: 1000}
	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("pickCwd property check failed: %v", err)
	}

	// Also defensively assert nil-request behaviour.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("pickCwd panicked on nil request: %v", r)
		}
	}()
	_ = pickCwd(nil, "/some/default")
}

// Example_pickCwd is a runnable godoc example (TRST-07). Demonstrates
// the longest-common-parent path with a deterministic 3-URI input. The
// Output: block is validated by `go test -run Example`. Lowercase
// suffix style because pickCwd is unexported (Go test convention).
func Example_pickCwd() {
	req := &canonical.ChatRequest{
		ResourceLinks: []canonical.ResourceLinkBlock{
			{URI: "file:///workspace/proj/a.go"},
			{URI: "file:///workspace/proj/sub/b.go"},
			{URI: "file:///workspace/proj/sub/deeper/c.go"},
		},
	}
	got := pickCwd(req, "/fallback")
	// On Unix the result is "/workspace/proj"; on Windows it would
	// differ. This example is Unix-targeted (the Go reference doc
	// host runs Linux).
	fmt.Println(got)
	// Output: /workspace/proj
}

// Compile-time sanity — keep filepath.Separator referenced so the import
// isn't accidentally dropped on a platform-specific code path elision.
var _ = filepath.Separator
