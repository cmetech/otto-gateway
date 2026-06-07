# Deferred items — Phase 10

Out-of-scope discoveries encountered during execution. Tracked here; not
fixed in the originating plan because they fall outside its scope boundary.

## From 10-01 (Wave 1, mechanical drain)

- **Two QF1001 sites not in the 10-BASELINE.txt snapshot (golangci-lint v2.12.2):**
  - `internal/plugin/pii/luhn.go:55:5` — QF1001 De Morgan
  - `internal/plugin/pii/recognizers_test.go:866:26` — QF1001 De Morgan
  These were reported by the lint run after Task 1, but are absent from
  the captured baseline (lines 21-23). Likely a linter-rev or rule-tuning
  delta since the baseline snapshot was taken. Scope boundary: Wave 1 owns
  only the three baseline QF1001 lines; routing these is a follow-up
  (re-baseline at phase close, then re-decide).

- **Two G703 path-traversal hits unmasked by Task 4 (G301 → G750):**
  - `cmd/otto-gateway/main.go:989:24` — G703 path traversal via taint analysis
  - `internal/config/config.go:493:27` — G703 path traversal via taint analysis
  Both sites previously emitted G301 (perm tightening). Now that 0o755 is
  0o750, gosec's secondary rule G703 surfaces on the same `os.MkdirAll(dir, …)`
  argument. Wave 2/3 owns the remaining gosec subcategories; defer to that
  wave (the dir argument originates from env-supplied paths, so the proper
  mitigation is a `filepath.Clean` + allowlist check — not a Wave 1 fix).
