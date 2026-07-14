// Command report renders `go test -json` NDJSON from stdin into an
// emoji-free markdown E2E report on stdout.
//
// It has NO build tag so it compiles and runs under the default toolchain
// (`go run ./tests/e2e/cmd/report`) — the e2e Makefile target pipes the
// test JSON through it while preserving go test's own exit code separately.
// This renderer never exits non-zero for normal input; its exit code is
// irrelevant to pass/fail accounting.
//
// Stdlib only (bufio, encoding/json, flag, fmt, os, os/exec, sort, strings,
// time) — no new go.mod dependencies.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// event mirrors the shape of a `go test -json` NDJSON line. Field names
// already match Go's case-insensitive JSON matching, but the explicit tags
// make the contract unambiguous.
type event struct {
	Action  string  `json:"Action"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// result accumulates the terminal state of a single test.
type result struct {
	name    string
	status  string // "pass" | "fail" | "skip"
	elapsed float64
	output  strings.Builder
}

func main() {
	versionFlag := flag.String("version", "", "report version label (default: git describe, else \"unknown\")")
	flag.Parse()

	version := strings.TrimSpace(*versionFlag)
	if version == "" {
		version = gitVersion()
	}

	results := parse(os.Stdin)
	md := render(results, version)
	if _, err := io.WriteString(os.Stdout, md); err != nil {
		fmt.Fprintf(os.Stderr, "report: write stdout: %v\n", err)
	}
}

// gitVersion is the best-effort version label when -version is not supplied.
func gitVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "unknown"
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "unknown"
	}
	return v
}

// parse reads NDJSON line-by-line and folds it into per-test results. Lines
// that fail to parse (non-JSON noise) and package-level events (empty Test)
// are ignored. The terminal action (pass/fail/skip) sets the final status
// and elapsed; all Output strings are concatenated for the Failures section.
func parse(r *os.File) map[string]*result {
	results := make(map[string]*result)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // non-JSON noise — skip
		}
		if ev.Test == "" {
			continue // package-level event
		}

		res, ok := results[ev.Test]
		if !ok {
			res = &result{name: ev.Test}
			results[ev.Test] = res
		}

		if ev.Output != "" {
			res.output.WriteString(ev.Output)
		}

		switch ev.Action {
		case "pass", "fail", "skip":
			res.status = ev.Action
			res.elapsed = ev.Elapsed
		}
	}

	return results
}

// render builds the markdown report and returns it. Emoji-free; plain
// PASS/FAIL/SKIP words. Building into a strings.Builder keeps the function
// pure (no I/O errors to thread) — the single stdout write happens in main.
func render(results map[string]*result, version string) string {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)

	var pass, fail, skip int
	for _, r := range results {
		switch r.status {
		case "pass":
			pass++
		case "fail":
			fail++
		case "skip":
			skip++
		}
	}
	total := len(results)

	var b strings.Builder
	b.WriteString("# Gateway E2E Report\n\n")
	fmt.Fprintf(&b, "Generated: %s  |  Version: %s\n\n", time.Now().UTC().Format(time.RFC3339), version)
	fmt.Fprintf(&b, "Summary: %d pass / %d fail / %d skip / %d total\n\n", pass, fail, skip, total)

	b.WriteString("| Test | Result | Duration |\n")
	b.WriteString("| ---- | ------ | -------- |\n")
	for _, name := range names {
		r := results[name]
		fmt.Fprintf(&b, "| %s | %s | %.2fs |\n", name, label(r.status), r.elapsed)
	}

	if fail == 0 {
		return b.String()
	}

	b.WriteString("\n## Failures\n")
	for _, name := range names {
		r := results[name]
		if r.status != "fail" {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", name)
		b.WriteString("```text\n")
		out := strings.TrimRight(r.output.String(), "\n")
		b.WriteString(out)
		b.WriteString("\n```\n")
	}

	return b.String()
}

// label maps a terminal status to its uppercase report word.
func label(status string) string {
	switch status {
	case "pass":
		return "PASS"
	case "fail":
		return "FAIL"
	case "skip":
		return "SKIP"
	default:
		return strings.ToUpper(status)
	}
}
