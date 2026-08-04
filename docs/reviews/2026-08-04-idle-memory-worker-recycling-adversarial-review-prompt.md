# Adversarial code review — idle-memory Kiro worker recycling

> Paste everything below the line into a fresh coding session using a capable
> model or agent that did not implement this feature. Give it read access to
> the implementation worktree. This is a hostile, evidence-driven review. The
> reviewer may run tests and create disposable probes, but must not modify
> production files, commit, push, merge, tag, publish, or release.

---

You are a hostile senior Go, concurrency, Windows systems, and observability
reviewer. Your job is to break and disprove this feature, not to bless it.

Assume that:

- Passing tests may assert the wrong behavior.
- Mocks may hide real lifecycle problems.
- Comments and implementation summaries may repeat unproven claims.
- Locally correct components may fail when composed.
- A race-free program can still contain logical ownership races.
- Metrics and dashboards can be internally consistent while lying to operators.

Do not praise the architecture. Produce evidence-backed findings or state
exactly what you verified safe.

## Exact review scope

Review this checkout:

~~~text
Repository: /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/.worktrees/idle-memory-worker-recycling
Branch:     feat/idle-memory-worker-recycling
Base:       10bc32c7180caafc515d500ce2a7fb0427644001
Product tip: 344c3d462a7afe04088153a09429a4939ca04e12
Review HEAD: 6378742b118ef681587b66a3ba458c8588fa8a19
Diff:       10bc32c..6378742
~~~

6378742 adds only execution closeout documentation after the product tip.
Verify that statement instead of trusting it.

Before reviewing:

~~~bash
git status --short
git branch --show-current
git rev-parse HEAD
git merge-base HEAD 10bc32c7180caafc515d500ce2a7fb0427644001
git diff --check 10bc32c..HEAD
git diff --name-status 10bc32c..HEAD
git log --oneline 10bc32c..HEAD
~~~

The prompt file itself may appear as the sole untracked worktree entry:

~~~text
?? docs/reviews/2026-08-04-idle-memory-worker-recycling-adversarial-review-prompt.md
~~~

That expected entry is not an implementation change and must not be deleted.
Stop with a scope error if:

- The branch or HEAD differs materially.
- 10bc32c is not the merge base.
- The worktree contains any change other than this untracked prompt file.
- Commits after 6378742 contain anything this prompt does not describe.

Review every changed production and test file. Generated Grafana JSON may be
validated against its generator rather than manually inspecting all JSON, but
it may not be ignored.

This is a read-and-verify review. You may create disposable test probes when
static reasoning is insufficient, but:

- Do not modify production files.
- Do not commit, push, merge, tag, publish, or release.
- Remove disposable probes before finishing.
- Prove the worktree returned to its starting state.

## Authoritative contract

Read these completely before assessing the implementation:

1. CLAUDE.md
2. docs/superpowers/specs/2026-08-04-idle-memory-worker-recycling-design.md
3. docs/superpowers/plans/2026-08-04-idle-memory-worker-recycling.md
4. .planning/quick/260804-ae3-implement-the-approved-idle-memory-kiro-/260804-ae3-PLAN.md
5. .planning/quick/260804-ae3-implement-the-approved-idle-memory-kiro-/260804-ae3-SUMMARY.md

The design is the product authority. The plan describes promised
implementation and verification. The quick-task files and commit messages are
evidence claims, not proof.

If implementation and design disagree, identify the disagreement explicitly.

## What was built

The existing gateway recycles pooled Kiro workers after a configured number of
successful session/new calls. This branch adds a second, conjunctive policy:

~~~text
worker has served at least one real user request
AND worker is free
AND idle since the completed request >= configured duration
AND direct Kiro working set > configured memory threshold
~~~

A qualifying worker is eagerly replaced with another warm worker. The next
user request should not pay cold-start latency.

The intended platform posture is:

- Windows: supported and prioritized, using direct-process working set.
- Linux: supported using existing direct-process RSS.
- macOS/other unsupported platforms: policy is a no-op and is visibly reported
  as unsupported.

Configuration:

~~~text
KIRO_WORKER_IDLE_RECYCLE_MS
  Binary default: 0, which disables the policy
  Laptop wrapper default: 900000 / 15 minutes
  Accepts integer milliseconds or Go duration strings

KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB
  Binary and wrapper default: 500
  Must be positive
  Maximum: 1,048,576 MiB
~~~

Threshold semantics are exact:

~~~text
idle >= configured duration
memory > configured bytes
~~~

The implementation also adds:

- Per-worker successful user-path session/new count
- Last completed user-request release time
- Idle-memory recycling loop
- Unified max-turn/idle-memory recycle admission
- Windows/Linux process-memory support capability
- Prometheus counters, gauges, and trigger histograms
- Admin snapshot and slot-card state
- Windows/POSIX wrapper configuration and support visibility
- Grafana panels generated from Python
- Shutdown, lifecycle, race, UI, and cross-platform tests

## Change map — verify, do not trust

| Area | Landmarks | Intended responsibility |
|---|---|---|
| Configuration | internal/config/config.go, worker_idle_recycle_test.go | Parse defaults, integers/durations, negative values, overflow and memory cap |
| Main wiring | cmd/otto-gateway/main.go, main_test.go | Convert MiB safely and wire policy, sampler, metrics, and admin state |
| Pool state | internal/pool/pool.go, config.go, detail.go | User activity, checkout ownership, release timestamps, replacement reset and projections |
| Idle sweep | internal/pool/idle_recycle.go, idle_recycle_test.go | Cadence, free-slot claiming, eligibility, memory sampling and trigger construction |
| Existing turn recycling | internal/pool/worker_recycle_test.go | Preserve max-turn behavior and unified admission |
| Process sampling | internal/procstat/* | Windows/Linux support and unsupported-platform behavior |
| Metrics | internal/metrics/metrics.go, worker_collector.go, tests | Bounded reason labels, gauges and successful-trigger histograms |
| Admin API/UI | internal/admin/*, static/js/admin.js, admin_js_test.js | Additive snapshot fields and honest worker-policy presentation |
| Wrappers/support | scripts/gw, gw.ps1, .env.example, support-bundle tests | Windows-first defaults, override behavior and environment visibility |
| Grafana | scripts/gen_grafana_dashboard.py, its tests, checked-in JSON | Generated recycling panels and honest empty-series behavior |
| Operator docs | docs/operating.md, docs/grafana/README.md | Configuration and metric semantics |

Use git diff --name-status 10bc32c..6378742 as the authoritative inventory.

## Locked invariants to falsify

Produce a PASS / FAIL / UNPROVEN verdict for every invariant.

1. Binary behavior remains backward-compatible: idle-memory recycling is
   disabled when KIRO_WORKER_IDLE_RECYCLE_MS=0.

2. Wrapper defaults enable the Windows-first policy at 15 minutes and 500 MiB
   without overwriting explicit overrides.env values.

3. Eligibility is exactly conjunctive:

   ~~~text
   used >= 1
   AND free
   AND idle >= threshold
   AND RSS > threshold
   ~~~

   Equality at the memory threshold must not recycle. Equality at the idle
   threshold must be eligible.

4. Only successful user-path Pool.NewSession calls increment the user counter.
   Catalog warmup and self-heal probes may affect the existing turn budget but
   must not make an unused worker idle-memory eligible.

5. Idle begins at the exact-once terminal release winner, not at session
   creation, first token, prompt return, or an arbitrary cleanup path.

6. Successful result drainage, request cancellation, explicit cancellation,
   and prompt failure each release the slot and update user-idle state exactly
   once despite racing terminal paths.

7. Busy workers are never sampled, interrupted, recycled, or shown with
   nonzero idle time.

8. The idle sweep gains exclusive ownership only by receiving a free slot from
   Pool.slots. It never selects a candidate from Pool.all and later closes a
   worker that a request acquired.

9. No client call or OS process-memory call occurs while Pool.mu is held.

10. Max-turn and idle-memory recycling use one admission mechanism. At most one
    scheduled recycle is in flight across both reasons, and max-turn precedence
    remains intact.

11. A deferred candidate remains usable and can be retried later. A failed
    defensive return cannot silently shrink healthy pool capacity during a
    reachable execution.

12. Successful replacement atomically resets:

    - turns
    - user requests since spawn
    - last user release time
    - spawn time
    - client/PID lifecycle state

    The stable slot label must remain unchanged.

13. Unavailable memory sampling is not treated as zero, high memory, or a
    worker failure. It requeues the slot and does not poison pool health.

14. Shutdown wins over sweeps, memory reads, probes, acquisitions, and recycle
    launches. No WaitGroup.Add may occur after a corresponding Wait; no slot
    may be requeued or admitted after close begins; shutdown must not leak
    goroutines.

15. Metrics use only bounded labels and describe the implemented semantics
    honestly. Trigger histograms are recorded only after successful
    idle-memory replacement.

16. Admin and Grafana views never imply the policy is active on unsupported
    platforms, fabricate idle values from future timestamps, or fabricate
    quantiles when no trigger samples exist.

17. Logs, metrics, admin state, support output, and dashboards expose no session
    ID, prompt, user identity, working directory, or arbitrary request-derived
    label.

18. Dedicated X-Session-Id workers remain governed by SESSION_TTL_MS; this
    feature must not recycle stateful session workers by memory.

## Attack campaign 1 — configuration and conversion

Try to boot or wire a policy with unintended values:

- Empty, whitespace, signed, negative, zero, extremely large, and malformed
  values.
- Integer millisecond values near time.Duration overflow.
- Go duration strings such as 0s, -1s, 1ns, 1.5s, and overflow-scale durations.
- Memory values -1, 0, 1, 500, 1048576, 1048577, and platform-sized integer
  extremes.
- A valid idle duration combined with invalid memory and vice versa.
- Disabled idle recycling combined with invalid memory. Determine whether
  startup rejection matches the contract.
- Conversion from signed configuration fields to uint64 bytes in main and
  admin projections. Find any negative-to-huge unsigned conversion.
- MiB conversion overflow on both 32-bit and 64-bit assumptions.
- Duplicate environment assignments and wrapper-versus-override precedence.
- POSIX and PowerShell differences in quoting, comments, CRLF, numeric parsing,
  and upgrade preservation.

Confirm error messages identify the offending environment variable.

## Attack campaign 2 — user activity and exact-once release

Trace the real lifecycle through Pool.NewSession, session registration, stream
wrapping, cancellation, and release.

Construct exact interleavings for:

- Result() completing while the request context is cancelled.
- Explicit Pool.Cancel racing result drainage.
- Prompt failure after successful session/new.
- Client.NewSession failing before session registration.
- SetModel failing after session creation.
- Multiple terminal callers attempting to release the same session.
- A later user request resetting the idle clock.
- Catalog warmup and self-heal running before any user request.
- Worker replacement racing a terminal release.

Determine whether:

- The user counter increments at the approved semantic point: successful
  user-path session/new, even if prompting later fails.
- The release timestamp changes once.
- A losing terminal path can update the timestamp again.
- A session mapping can be lost, overwritten, or retained after release.
- A replacement can inherit stale activity from the prior process.

A test name containing "exact once" is not proof. Identify the synchronization
primitive and winning mutation.

## Attack campaign 3 — channel ownership and logical concurrency

This is the highest-risk area.

Trace every receive from and send to Pool.slots, including:

- Request admission
- Catalog self-heal
- Idle sweep
- Test seams
- Failed session creation
- Deferred recycle
- Failed respawn
- Successful recycle
- Shutdown/drop paths

Prove or break these properties:

- A slot cannot be simultaneously owned by a request, sweep, probe, or recycle
  goroutine.
- Every successful channel receive has exactly one later return, transfer, or
  shutdown drop.
- No error path double-requeues a slot.
- No reachable failed nonblocking send silently removes a healthy slot from
  capacity.
- checkedOut is cleared exactly when the slot becomes observable as free.
- Pool observers cannot read inconsistent state between channel ownership and
  checkedOut.

### Known observation to challenge

A prior scoped review observed that NewSession receives from p.slots before it
acquires p.mu and sets slot.checkedOut. During that small interval, WorkerProcs
may report stale nonzero idle for a slot already acquired by a request.

Do not simply repeat that observation. Determine:

- Every receive site where this gap exists.
- Whether it is limited to metric/UI sampling or can affect recycling or
  ownership.
- Whether a realistic scheduler can expose it.
- Whether the current gated test begins too late to prove the complete
  receive-to-release interval.
- Appropriate severity and the smallest design-consistent correction.

Use a deterministic synchronization probe if needed. Do not rely on time.Sleep.

## Attack campaign 4 — idle sweep, cadence, and memory sampling

Inspect startIdleRecycler, idleRecycleLoop, the single-sweep logic, and
process-memory calls.

Attack:

- Idle thresholds below 4 seconds, very large thresholds, and cadence clamp
  boundaries.
- Loop startup racing Close.
- WaitGroup.Add versus Wait ordering.
- A memory reader blocked while Close begins.
- A worker exiting or changing PID between the state snapshot and memory read.
- PID zero, negative, stale, or reused by the OS.
- Access denied and process-not-found results.
- A sample reporting (0, true) versus (0, false).
- A memory sample exactly at and one byte above the threshold.
- Wall-clock movement backward or a future release timestamp.
- Multiple free slots whose samples take different amounts of time.
- A request arriving while the sweep temporarily owns most free slots.
- Pool sizes 1, 2, and 4.

Assess whether a slow or stuck platform memory API can delay requests or
shutdown. Distinguish a test-only malicious injected reader from a plausible
real Windows/Linux failure.

Confirm the policy measures direct worker working set/RSS only, not descendants,
and that all public descriptions match that limitation.

## Attack campaign 5 — unified recycling and shutdown

Model exact interleavings between:

- Max-turn release and idle-memory sweep
- Two simultaneous idle candidates
- Idle sweep and catalog probe
- Recycle admission and Close
- Respawn success and Close
- Respawn failure and a new acquisition
- close(p.closing), p.closed, idle-loop joining, closeAll, probe joining, and
  recycle joining

Specifically interrogate the final shutdown fix:

- Close sets admission closed before waiting for a blocked idle sweep.
- Actual client teardown remains after the sweep joins.
- NewSession during that interval must return exactly "pool: closed".
- A claimed sweep slot must be dropped rather than requeued.
- Setting p.closed earlier must not cause closeAll, self-heal, release, or
  respawn cleanup to skip work they formerly performed.

Check every p.closed branch for assumptions that "closed" means clients have
already been torn down.

Look for:

- Add-after-Wait races
- Double client closes that are not actually idempotent
- Slots disappearing from p.all
- New clients spawned after shutdown
- A successful respawn client escaping the snapshot used by closeAll
- Goroutines outliving Pool.Close
- Capacity permanently shrinking after a transient respawn failure

## Attack campaign 6 — platform behavior, especially Windows

Inspect every internal/procstat build-tag implementation and its production
wiring.

On Windows, verify:

- Supported() is true only in the Windows build.
- The implementation reports direct-process working set using the intended
  Win32 API.
- Handles are closed on every path.
- Access-denied, exited-process, invalid-PID, and partial-query failures return
  unavailable safely.
- Integer widths do not truncate working-set values.
- Windows cgo-free compilation succeeds.
- The sampler does not require privileges unavailable to an ordinary desktop
  user.
- PID reuse cannot make the gateway act on another process in a materially
  unsafe way.

On Linux, verify procfs parsing and direct-RSS semantics remain unchanged.

On macOS/other platforms:

- Supported() is false.
- The recycler loop does not start.
- The admin UI renders the policy as unavailable.
- Configuration remains visible without implying enforcement.
- No warning repeats every cadence.

If native Windows execution is unavailable, mark runtime Win32 claims UNPROVEN.
A cross-build is not proof of actual working-set behavior.

## Attack campaign 7 — metrics semantics and cardinality

Verify all new series:

~~~text
gw_pool_slot_recycles_by_reason_total{reason="max_turns|idle_memory"}
gw_worker_user_requests_since_spawn{slot}
gw_worker_idle_seconds{slot}
gw_pool_idle_memory_recycle_trigger_rss_bytes
gw_pool_idle_memory_recycle_trigger_idle_seconds
~~~

Attack:

- Arbitrary or empty recycle reasons escaping the closed label set.
- Aggregate recycle count diverging from reason-specific totals.
- Failed respawns incrementing success metrics.
- Histograms recording attempts rather than successful replacements.
- An idle-memory event recorded twice.
- Max-turn events polluting idle-memory histograms.
- Negative, NaN, infinite, or future-derived idle values.
- Busy workers exporting nonzero idle.
- Unused workers being indistinguishable from used workers unless gauges are
  interpreted together.
- Dead, respawning, or dropped slots retaining stale metrics.
- Slot labels or gateway labels becoming request-controlled.
- Help text claiming "completed requests" when the gauge counts successful
  session/new calls.

Trace the real scrape path through WorkerProcs, main projection, collector, and
registry. Tests that instantiate only a collector with fabricated rows do not
prove production wiring.

## Attack campaign 8 — admin API and browser rendering

Verify the additive JSON contract and actual rendered cards.

Test or reason through:

- Policy disabled.
- Policy supported and enabled.
- Policy configured but unsupported.
- Worker unused.
- Worker busy during slow session/new.
- Worker released below idle threshold.
- Worker idle and above threshold.
- Future last_user_release_at.
- Missing/null timestamp.
- Dead/recovering worker.
- Worker replaced while a snapshot is being rendered.
- Very large thresholds and durations.
- Older snapshot payloads missing the new fields.

Confirm unsupported platforms render IDLE n/a rather than an active countdown.

Determine whether client-side time passage between snapshots can make the UI
disagree materially with server metrics or eligibility.

Inspect HTML/JavaScript/CSS for:

- undefined, NaN, negative or fabricated durations
- Breaking older snapshot compatibility
- Layout regression at narrow widths
- Misleading memory threshold presentation
- Mutation controls accidentally added to what should remain read-only

## Attack campaign 9 — Grafana correctness

Treat scripts/gen_grafana_dashboard.py as the source of truth and prove the
checked-in JSON is generated from it.

Review:

1. Worker recycles by reason
2. Idle-memory recycles per 100 LLM requests
3. Worker idle/use since spawn
4. Trigger RSS and idle distributions

Attack:

- Zero denominators.
- Missing series.
- Counter resets.
- Sparse traffic windows.
- No histogram samples.
- histogram_quantile fabricating a value from empty input.
- Wrong units: bytes versus MiB, seconds versus milliseconds.
- Grouping that mixes gateways or slots.
- Rates and increases over inconsistent windows.
- Ratio semantics that operators could mistake for a per-user measure.
- Dashboard metric inventory drifting from runtime names.
- Hand-edited JSON differing from the generator.

Confirm the dashboard does not claim actual "bytes freed"; that metric was
intentionally excluded.

## Attack campaign 10 — wrappers, support, and documentation

Compare POSIX and PowerShell behavior:

- Cold init
- Upgrade
- Existing overrides
- Duplicate environment keys
- CRLF/LF
- Paths containing spaces
- Missing PowerShell
- Support-bundle output

Verify:

- Laptop defaults are 15 minutes and 500 MiB.
- Existing explicit values survive upgrade.
- Both new keys appear where operators expect configuration visibility.
- Neither key is treated as a secret.
- Support bundles do not accidentally include unrelated environment values.
- Documentation uses the same units, comparison operators, platform support,
  and direct-process memory definition as code.
- macOS is not described as enforcing a policy it cannot sample.

## Attack campaign 11 — test integrity

Assume the 1,000-line idle recycler test file may still miss the important
interleaving.

For each high-risk claim, identify whether the test:

- Exercises the real production path.
- Uses deterministic gates rather than sleeps.
- Asserts actual public behavior rather than internal mock state.
- Would fail if the claimed invariant regressed.
- Runs under the race detector.
- Cleans up every goroutine and fake client.
- Proves channel ownership rather than only final counts.

Pay particular attention to:

- The complete receive-to-return checkedOut interval
- Close during a blocked memory read
- Add/Wait ordering
- Max-turn versus idle-memory serialization
- Failed defensive channel sends
- Prompt failure after successful session/new
- Unsupported-platform UI rendering
- Production main projections rather than isolated structs
- Actual Prometheus exposition
- Generated dashboard byte parity

Name the single highest-risk behavior the existing suite does not truly prove.

## Mandatory verification

Record exact commands, exit codes, skips, cache use, and unavailable tools. Do
not silently substitute narrower commands.

~~~bash
git status --short
git diff --check 10bc32c..6378742
git diff --name-status 10bc32c..6378742

make fmt-check
go vet ./...
go test ./... -count=1

go test -race ./internal/pool \
  -run 'UserActivity|IdleMemory|WorkerRecycle|WorkerProcs|Close' \
  -count=1

go test ./internal/config ./internal/procstat ./internal/metrics \
  ./internal/admin ./cmd/otto-gateway -count=1

node --test internal/admin/admin_js_test.js
python3 -m unittest scripts.test_gen_grafana_dashboard
bash -n scripts/gw
bash tests/scripts/test-support-bundle.sh
python3 scripts/test_privacy_docs.py

review_lint_cache="$(mktemp -d)"
PATH="$(go env GOPATH)/bin:$PATH" \
  GOLANGCI_LINT_CACHE="$review_lint_cache" \
  golangci-lint run ./...

"$(go env GOPATH)/bin/go-arch-lint" check --project-path .
"$(go env GOPATH)/bin/govulncheck" ./...
~~~

If pwsh is available:

~~~powershell
pwsh -NoProfile -File tests/scripts/test-support-bundle.ps1
~~~

Run cgo-free targeted builds into a new temporary directory:

~~~bash
review_build_dir="$(mktemp -d)"

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -o "$review_build_dir/otto-gateway-windows-amd64.exe" \
  ./cmd/otto-gateway

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o "$review_build_dir/otto-gateway-linux-amd64" \
  ./cmd/otto-gateway

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -o "$review_build_dir/otto-gateway-darwin-arm64" \
  ./cmd/otto-gateway

shasum -a 256 "$review_build_dir"/*
~~~

Finally run the canonical gate with a fresh lint cache:

~~~bash
review_ci_cache="$(mktemp -d)"
PATH="$(go env GOPATH)/bin:$PATH" \
  GOLANGCI_LINT_CACHE="$review_ci_cache" \
  make ci
~~~

Do not claim a native Windows runtime property from a cross-build.

## Known baseline exclusions and evidence gaps

Do not attribute these to this branch without reproducing a changed-code cause:

1. Whole-repository cgo-free Darwin build:

   ~~~bash
   CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
   ~~~

   This has a pre-existing failure in the systray/cgo portion of
   cmd/otto-tray. The targeted cgo-free Gateway and internal/procstat paths are
   the relevant feature gates.

2. Exact ShellCheck SC1091 findings in wrapper source-loading paths were
   reproduced before the feature. New ShellCheck findings remain in scope.

3. The Windows/real-Kiro observational walkthrough has not been completed.
   This is a release evidence gap, not automatically a code defect.

4. macOS memory enforcement is intentionally unsupported. Do not flag the
   absence of a macOS sampler as a missing feature.

5. The checkout-state sampling window described above is a known Minor
   observation, not an accepted invariant. Independently determine its actual
   scope and severity.

If you believe a failure is pre-existing, prove it against an isolated checkout
of the stated pre-feature commit before excluding it.

## Explicitly out of scope

Do not report these locked decisions as missing features:

- Shrinking or parking the configured worker pool
- Cold workers
- Recycling dedicated stateful sessions by memory
- macOS process-memory implementation
- Descendant-process or process-tree RSS aggregation
- Changing Kiro's allocator
- Forcing Kiro to release memory in place
- Replacing or weakening max-turn recycling
- A separately configurable sweep interval
- Public CLI flags for this policy
- Request/session/user labels in metrics
- Estimated "bytes freed"
- A writable admin dashboard

You may report a defect where the implementation accidentally contradicts one
of these boundaries.

## Severity

Use these levels without inflation:

- **Critical** — a busy worker can be closed or recycled; slot ownership
  corruption; a user request can run on a closed/wrong client; cross-request
  session corruption; deadlock or goroutine leak that wedges shutdown;
  exploitable data race; sensitive request data enters logs or metric labels.

- **High** — an unused, below-idle, or at/below-memory-threshold worker is
  recycled; shutdown admits new work; pool capacity can permanently shrink on
  a reachable normal failure; concurrent recycle admission violates
  availability; Windows policy falsely claims support while being unusable for
  ordinary users; configuration overflow enables materially different policy.

- **Medium** — metrics/admin/Grafana materially misrepresent behavior; wrapper
  defaults or upgrade precedence are wrong; trigger accounting is incorrect;
  unsupported-platform state is misleading; bounded but meaningful
  availability or performance regression.

- **Low** — weak test, minor documentation drift, tiny transient presentation
  error without lifecycle impact, or maintainability issue with a concrete
  consequence.

A theoretical concern without a reachable input or wrong observable result is
not a finding.

## Required deliverable

Produce one self-contained review with this exact structure:

1. **Scope verification**

   - Branch and HEAD
   - Base ancestry and exact diff
   - Starting worktree status
   - Changed files reviewed
   - Tests/tools/platforms unavailable

2. **Verdict**

   One of:

   - SHIP
   - SHIP WITH FOLLOW-UPS
   - DON'T SHIP

   Give the single most important reason.

3. **Findings table**

   Sort by severity:

   ~~~text
   file:line | severity | invariant violated | defect | concrete failure scenario | minimal fix
   ~~~

   Every finding must include an exact input, event sequence, environment,
   platform condition, or goroutine interleaving leading to an observable wrong
   result.

4. **Top-five reproductions**

   Provide exact requests, configuration, channel/lifecycle sequence, or
   runnable disposable test for the five most important findings.

5. **Invariant matrix**

   Include all 18 locked invariants, each marked:

   - PASS
   - FAIL
   - UNPROVEN

   Cite source and test evidence for every row.

6. **Concurrency and ownership audit**

   Explicitly account for every Pool.slots receive/return/drop path and every
   shutdown/recycle goroutine.

7. **Windows readiness**

   Separate:

   - Statically verified
   - Cross-build verified
   - Native Windows verified
   - Still unproven

8. **Test-integrity assessment**

   Name the highest-risk behavior the existing tests do not actually prove.

9. **What I verified safe and why**

   Cover at minimum:

   - Busy-worker exclusion
   - Threshold boundaries
   - Exact-once activity tracking
   - Unified recycle admission
   - Shutdown joining
   - Bounded metrics
   - Unsupported-platform presentation

10. **Verification evidence**

    Exact commands, exit codes, skips, race results, lint result, vulnerability
    result, build hashes, and final worktree status.

11. **Required remediation before ship**

    Ordered minimal fixes for every Critical/High finding, with the focused
    regression test each fix requires.

Do not stop after finding one bug. Do not report concerns without tracing them
to a reachable wrong outcome. Do not accept a green test merely because its
name matches the invariant. Be specific or be silent.
