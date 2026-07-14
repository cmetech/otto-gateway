//go:build darwin || windows

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// parseDotenv parses a minimal KEY=VALUE dotenv format. Comments
// (lines beginning with `#`) and blank lines are ignored. Values may
// be wrapped in single or double quotes; quotes are stripped. No
// variable interpolation, no `export ` prefix — the wrapper scripts
// own anything fancier.
func parseDotenv(body []byte) (map[string]string, error) {
	out := map[string]string{}
	s := bufio.NewScanner(bytes.NewReader(body))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') ||
				(val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		out[key] = val
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("scan dotenv: %w", err)
	}
	return out, nil
}

// readDotenvFile reads and parses a dotenv file. Missing file ⇒ nil
// map, nil error (caller treats absence as "no overrides here").
func readDotenvFile(path string) (map[string]string, error) {
	body, err := os.ReadFile(path) //nolint:gosec // path is operator-configured under GW_HOME
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dotenv %s: %w", path, err)
	}
	return parseDotenv(body)
}

// resolveDashboardURL returns the URL the tray's "Open dashboard"
// action should open. HTTP_ADDR precedence:
//  1. $GW_HOME/overrides.env
//  2. $GW_HOME/.env
//  3. $HTTP_ADDR
//  4. ":18080" default
//
// The host portion is always normalized to 127.0.0.1 because a
// bound 0.0.0.0 listener is still reachable on the loopback and
// that's what the operator wants to click.
func resolveDashboardURL(gwHome string) string {
	addr := lookupHTTPAddr(gwHome)
	if addr == "" {
		addr = ":18080"
	}
	port := strings.TrimPrefix(addr, ":")
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		port = addr[i+1:]
	}
	return "http://127.0.0.1:" + port
}

func lookupHTTPAddr(gwHome string) string {
	for _, path := range []string{gwOverridesPath(gwHome), gwEnvPath(gwHome)} {
		m, _ := readDotenvFile(path)
		if v, ok := m["HTTP_ADDR"]; ok {
			return v
		}
	}
	return os.Getenv("HTTP_ADDR")
}
