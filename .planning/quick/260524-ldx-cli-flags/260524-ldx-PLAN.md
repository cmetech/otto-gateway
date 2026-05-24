---
phase: quick-260524-ldx
plan: 01
type: tdd
wave: 1
depends_on: []
files_modified:
  - internal/config/config.go
  - internal/config/loadargs_test.go
  - cmd/loop24-gateway/main.go
autonomous: true
requirements:
  - QUICK-CLI-FLAGS
must_haves:
  truths:
    - "Running with no CLI args produces a Config byte-identical to today's config.Load() (env-only path unchanged)."
    - "A CLI flag explicitly passed overrides the env/default value for that field (flag-wins precedence)."
    - "A flag NOT passed leaves the env-resolved value untouched (fall-through to env)."
    - "An invalid flag value (bad CIDR, unknown surface, unparseable duration/int) returns an error the same way Load() does."
    - "--version causes main() to print version.Version and exit 0 without starting the server; config package never calls os.Exit."
    - "There is NO --auth-token flag — AUTH_TOKEN stays env-only (secret must not appear in argv)."
  artifacts:
    - path: "internal/config/config.go"
      provides: "Exported func LoadArgs(args []string) (Config, error) + ErrVersionRequested sentinel"
      contains: "func LoadArgs"
    - path: "internal/config/loadargs_test.go"
      provides: "Table-driven tests for flag-wins, fall-through, invalid values, --version, no --auth-token flag, env-only parity"
      contains: "func TestLoadArgs"
    - path: "cmd/loop24-gateway/main.go"
      provides: "main() calls config.LoadArgs(os.Args[1:]) and handles ErrVersionRequested + flag.ErrHelp as exit 0"
      contains: "config.LoadArgs"
  key_links:
    - from: "cmd/loop24-gateway/main.go"
      to: "config.LoadArgs"
      via: "os.Args[1:] passed at startup"
      pattern: "config\\.LoadArgs\\(os\\.Args\\[1:\\]\\)"
    - from: "internal/config/config.go LoadArgs"
      to: "internal/config/config.go Load"
      via: "LoadArgs calls Load() first to resolve env+defaults, then overlays set flags"
      pattern: "Load\\(\\)"
---

<objective>
Add CLI flag support to the loop24-gateway binary with flag-wins-over-env
precedence, using ONLY the Go stdlib `flag` package (no new dependencies —
preserves the no-cgo / minimal-deps constraint from CLAUDE.md).

`config.Load()` stays env-only and UNCHANGED. A new `config.LoadArgs(args)`
wraps it: resolve env+defaults via `Load()`, then overlay ONLY the flags the
operator explicitly passed (`flag.FlagSet.Visit` mechanism), re-validate the
overridden values, and return. `main()` switches `config.Load()` →
`config.LoadArgs(os.Args[1:])`.

Purpose: operators can override any non-secret config field on the command
line without env vars. AUTH_TOKEN stays env-only (secret — must not appear in
argv / ps / /proc). Env var names are unchanged (Node-parity hard constraint).

Output: `LoadArgs` + `ErrVersionRequested` sentinel in config.go, table-driven
tests, and the main.go one-line switch with --version / --help exit-0 handling.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@./CLAUDE.md

<read_first>
- internal/config/config.go — the env loader. LoadArgs is added HERE, alongside
  Load(). REUSE the existing helpers/validators rather than re-deriving parsing:
    - getEnvStrSlice does whitespace-split (KIRO_ARGS). For the --kiro-args flag,
      apply `strings.Fields(value)` to match exactly.
    - getEnvStrSliceComma does comma-split+trim+drop-empties (AUTH_TOKEN,
      ALLOWED_IPS, ENABLED_SURFACES). For comma flags, split with the SAME logic.
    - parseCIDRs([]string) ([]netip.Prefix, error) — reuse for --allowed-ips.
    - validateEnabledSurfaces([]string) error — reuse for --enabled-surfaces.
    - getEnvDuration accepts BOTH integer-ms ("60000" → 60s) AND Go duration
      strings ("60s"). For --ping-interval, prefer a `time.Duration` flag
      (fs.Duration) which accepts Go duration strings; integer-ms is an
      env-only nicety and need not be replicated on the flag.
    - Error style: Load() returns `fmt.Errorf("config: invalid env vars: %w",
      errors.Join(errs...))`. LoadArgs may use a parallel style, e.g.
      `fmt.Errorf("config: invalid flags: %w", errors.Join(errs...))`.
  Load()'s signature and behavior MUST NOT change — existing config_test.go and
  config_internal_test.go must keep passing untouched.
- internal/config/config_test.go — blackbox test patterns (package config_test).
  New LoadArgs tests go in loadargs_test.go using the SAME package + style
  (t.Setenv for env, reflect.DeepEqual for slices, t.Parallel only when no
  t.Setenv). Note: tests that DO call t.Setenv cannot use t.Parallel.
- internal/config/config_internal_test.go — whitebox patterns (package config),
  if any test needs unexported access (likely not needed for LoadArgs blackbox).
- cmd/loop24-gateway/main.go — main() at line 55 calls config.Load(). Switch to
  config.LoadArgs(os.Args[1:]) and add the --version / flag.ErrHelp exit-0 branch
  BEFORE the existing error→os.Exit(1) path.
- internal/version/version.go — version.Version (string) for the --version flag.
</read_first>

<interfaces>
Existing signatures the executor builds against (from internal/config/config.go):

```go
func Load() (Config, error)
func validateEnabledSurfaces(surfaces []string) error
func parseCIDRs(entries []string) ([]netip.Prefix, error)
```

New surface to create:

```go
// ErrVersionRequested is returned by LoadArgs when --version was passed.
// main() checks for it and exits 0 after printing version (config never exits).
var ErrVersionRequested = errors.New("version requested")

func LoadArgs(args []string) (Config, error)
```

From internal/version/version.go:

```go
var Version string // build-time, defaults "0.0.0-dev"
```
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: RED — table-driven tests for config.LoadArgs</name>
  <files>internal/config/loadargs_test.go</files>
  <behavior>
    Write FAILING tests (package config_test) covering every contract before
    LoadArgs exists. Tests must compile-fail / fail until Task 2 lands.

    - flag-wins-over-env, one per type:
      - string: --http-addr :9999 overrides HTTP_ADDR=:8080 → cfg.HTTPAddr == ":9999"
      - bool: --debug overrides DEBUG="" → cfg.Debug == true
      - int: --pool-size 4 overrides POOL_SIZE=2 → cfg.PoolSize == 4
      - duration: --ping-interval 30s overrides PING_INTERVAL=60s → 30*time.Second
      - comma-slice: --allowed-ips 10.0.0.0/8,192.168.1.1 → 2 prefixes
        (192.168.1.1 → /32) reusing parseCIDRs semantics
      - whitespace-slice: --kiro-args "acp --verbose" → []string{"acp","--verbose"}
      - comma-slice validated: --enabled-surfaces ollama → []string{"ollama"}
    - fall-through: with PING_INTERVAL=30s set and NO --ping-interval passed,
      LoadArgs([]string{"--debug"}) leaves PingInterval == 30s (only --debug overlaid).
    - env-only parity: LoadArgs(nil) (and LoadArgs([]string{})) returns a Config
      reflect.DeepEqual to Load() under the same env. Assert this with both a
      clean env (defaults) and a couple of t.Setenv values.
    - invalid flag values error (assert err != nil):
      - --allowed-ips not-an-ip (bad CIDR)
      - --enabled-surfaces ollama,olama (unknown surface; assert err names "olama")
      - --ping-interval abc (unparseable duration)
      - --pool-size abc (unparseable int)
    - --version: LoadArgs([]string{"--version"}) returns errors.Is(err,
      config.ErrVersionRequested) == true.
    - NO --auth-token flag registered: this is a blackbox package so it cannot
      inspect the private FlagSet directly. Instead assert behaviorally that
      passing "--auth-token" is rejected as an unknown flag — i.e.
      LoadArgs([]string{"--auth-token", "secret"}) returns a non-nil error
      (flag.ContinueOnError surfaces "flag provided but not defined"). Add a
      comment that the source-level guarantee (no fs.String("auth-token", ...))
      is also asserted by the grep gate in acceptance.

    Naming: TestLoadArgs_* functions, table-driven where a type axis exists.
    t.Setenv tests must NOT call t.Parallel.
  </behavior>
  <action>
    Create internal/config/loadargs_test.go in package config_test, importing
    "loop24-gateway/internal/config", "errors", "reflect", "strings", "testing",
    "time". Mirror the existing config_test.go style: descriptive sub-test names,
    reflect.DeepEqual for slices, p.String() comparison for netip prefixes.
    Do NOT inline LoadArgs's implementation here — only call config.LoadArgs and
    config.ErrVersionRequested (both will not exist yet → RED). Commit the
    failing test: `test(quick-260524-ldx): add failing LoadArgs flag tests`.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/Projects/repos/local/loop24-gateway && go test ./internal/config/ -run TestLoadArgs -count=1 2>&1 | grep -Eq 'undefined: config.LoadArgs|undefined: config.ErrVersionRequested|build failed|FAIL' && echo RED-OK</automated>
  </verify>
  <done>loadargs_test.go exists; `go test ./internal/config/ -run TestLoadArgs` fails to build or fails (LoadArgs/ErrVersionRequested undefined). Existing config tests untouched.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: GREEN — implement config.LoadArgs + ErrVersionRequested, switch main.go</name>
  <files>internal/config/config.go, cmd/loop24-gateway/main.go</files>
  <behavior>
    Make Task 1's tests pass. LoadArgs(args):
    1. cfg, err := Load(); if err != nil return cfg, err (env errors surface first).
    2. fs := flag.NewFlagSet("loop24-gateway", flag.ContinueOnError) — NOT the
       global flag.CommandLine (testability, no global state).
    3. Set fs.SetOutput to a buffer (or io.Discard) so --help / parse errors do
       not pollute stderr during tests; the FlagSet owns its output.
    4. Register Bucket A flags with DEFAULTS taken from the already-resolved cfg
       (so an unset flag's "default" equals the env-resolved value — but we will
       NOT trust defaults; see step 6). Register into local vars:
         httpAddr (fs.String, default cfg.HTTPAddr), kiroCmd, kiroArgs (string),
         kiroCWD, debug (fs.Bool), pingInterval (fs.Duration, default
         cfg.PingInterval), poolSize (fs.Int), enabledSurfaces (string),
         ollamaPath, anthropicPath, openaiPath, allowedIPs (string),
         authTrustXFF (fs.Bool).
       Meta: version (fs.Bool "version").
       DO NOT register "auth-token" (secret — env-only).
    5. if err := fs.Parse(args); err != nil { if errors.Is(err, flag.ErrHelp)
       return cfg, err (let main treat help as exit 0); return cfg,
       fmt.Errorf("config: %w", err) } — unknown flags (e.g. --auth-token)
       surface here as a non-nil error.
    6. if version flag set → return cfg, ErrVersionRequested (check via the bool
       var, BEFORE overlay; main prints + exits 0).
    7. Overlay ONLY explicitly-set flags: fs.Visit(func(f *flag.Flag){ switch
       f.Name { ... assign to cfg fields ... }}). This is the flag-wins
       mechanism — Visit walks ONLY flags the user passed, so unset flags leave
       cfg's env value intact. For each:
         - http-addr/kiro-cmd/kiro-cwd/ollama-path-prefix/anthropic-path-prefix/
           openai-path-prefix → assign string var to corresponding cfg field.
         - debug/auth-trust-xff → assign bool var.
         - pool-size → assign int var.
         - ping-interval → assign time.Duration var.
         - kiro-args → cfg.KiroArgs = strings.Fields(kiroArgs) (whitespace-split,
           matching getEnvStrSlice).
         - enabled-surfaces → split comma+trim+drop-empty (same as
           getEnvStrSliceComma), assign to cfg.EnabledSurfaces.
         - allowed-ips → split comma+trim+drop-empty, then parseCIDRs; on error
           accumulate.
       Note: OpenAIPathPrefix exists on Config (line 49) — wire --openai-path-prefix
       to it for completeness even though it is forward-design.
    8. Re-validate flag-overridden values the same way Load() does: if
       enabled-surfaces was overlaid, validateEnabledSurfaces(cfg.EnabledSurfaces);
       allowed-ips parse error from step 7. Accumulate errs; if len(errs) > 0
       return Config{}, fmt.Errorf("config: invalid flags: %w",
       errors.Join(errs...)).
    9. return cfg, nil.

    main.go: replace `cfg, err := config.Load()` with
    `cfg, err := config.LoadArgs(os.Args[1:])`. Immediately after, handle the two
    exit-0 cases BEFORE the existing error→os.Exit(1):
      if errors.Is(err, config.ErrVersionRequested) {
          fmt.Println(version.Version); os.Exit(0)
      }
      if errors.Is(err, flag.ErrHelp) { os.Exit(0) } // usage already printed by FlagSet
    Then keep the existing `if err != nil { ...Error...; os.Exit(1) }`. main owns
    process exit; config package NEVER calls os.Exit. Add "flag" + (already
    present) "errors", "fmt" imports to main.go as needed; version is already
    imported.
  </behavior>
  <action>
    Edit internal/config/config.go: add "flag" + "io" to imports, define
    `var ErrVersionRequested = errors.New("version requested")` near the top, and
    implement LoadArgs per the behavior block, reusing parseCIDRs,
    validateEnabledSurfaces, and strings.Fields. Add a small comment-block above
    LoadArgs noting (a) flag-wins via fs.Visit, (b) AUTH_TOKEN is deliberately
    NOT a flag (secret), (c) Load() is intentionally left env-only. Edit
    cmd/loop24-gateway/main.go to call LoadArgs and add the ErrVersionRequested /
    flag.ErrHelp exit-0 branches. Commit: `feat(quick-260524-ldx): add config.LoadArgs CLI flag overlay (flag-wins)`.
    If a refactor pass is needed for lint cleanliness, commit separately:
    `refactor(quick-260524-ldx): tidy LoadArgs flag wiring`.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/Projects/repos/local/loop24-gateway && go test ./internal/config/ -race -count=1 && go build ./... && go vet ./... && golangci-lint run ./internal/config/... ./cmd/... && grep -c 'auth-token' internal/config/config.go | grep -qx 0 && echo NO-AUTH-TOKEN-FLAG-OK && make build && ./bin/loop24-gateway --version | grep -Eq '.' && echo VERSION-OK</automated>
  </verify>
  <done>
    `go test ./internal/config/ -race -count=1` passes (Task 1 tests GREEN, plus
    all pre-existing config tests still pass). `go build ./...`, `go vet ./...`,
    and `golangci-lint run ./internal/config/... ./cmd/...` are clean.
    `grep -c 'auth-token' internal/config/config.go` returns 0 (no --auth-token
    flag registered — source-level secret guarantee). `make build` succeeds and
    `./bin/loop24-gateway --version` prints the version string and exits 0.
  </done>
</task>

</tasks>

<verification>
Full acceptance (run from repo root /Users/coreyellis/Projects/repos/local/loop24-gateway):

1. `go test ./internal/config/ -race -count=1` — passes (new + existing).
2. `go build ./...` — clean.
3. `go vet ./...` — clean.
4. `golangci-lint run ./internal/config/... ./cmd/...` — clean.
5. `grep -c 'auth-token' internal/config/config.go` → 0 (no secret flag).
6. `make build` succeeds; `./bin/loop24-gateway --version` prints version, exit 0.
7. Behavioral override (manual / short-lived): the env-only path with no args is
   byte-identical to Load() (covered by the parity test), and
   `--http-addr :9999 --enabled-surfaces ollama` overlays onto env (covered by
   flag-wins tests). Optionally confirm `./bin/loop24-gateway --help` prints usage
   and exits 0.
</verification>

<success_criteria>
- config.Load() unchanged; all pre-existing config tests pass untouched.
- config.LoadArgs(args) exists: Load() first, FlagSet overlay via fs.Visit
  (flag-wins), re-validation, parallel error style.
- No args → Config equal to Load() (env-only parity).
- Every flag type has a flag-wins test; fall-through, invalid-value, --version,
  and no-auth-token-flag cases all covered.
- main.go calls config.LoadArgs(os.Args[1:]) and exits 0 on --version / --help;
  config package never calls os.Exit.
- Only stdlib `flag` used; no new dependencies; env var names unchanged.
</success_criteria>

<output>
Create `.planning/quick/260524-ldx-cli-flags/260524-ldx-SUMMARY.md` when done.
</output>
