//go:build darwin

package main

// readUserEnvVar has no registry equivalent on darwin — HERMES_HOME resolution
// falls through to the env var / default home dir.
func readUserEnvVar(string) string { return "" }
