# LLM Prompt — Plan and Implement Reliable Hermes Computer Use

> Copy everything below the horizontal rule into a new coding session whose
> working directory is the branded Hermes repository:
> `/Users/coreyellis/code/github.com/cmetech/otto_hermes/hermes-agent`.
>
> This prompt deliberately starts with Superpowers discovery, specification,
> and planning. Do not jump directly to implementation.

---

You are working in the branded Hermes fork at:

```text
/Users/coreyellis/code/github.com/cmetech/otto_hermes/hermes-agent
```

Your goal is to make the built-in Hermes `computer_use` tool and its bundled
`computer-use` skill use Cua Driver reliably on Windows, especially for
Chromium/Electron text entry, while preserving Hermes' cross-platform behavior,
safety boundaries, packaging, and upstream-merge durability.

This is shared, brand-neutral functionality. It belongs on the fork's neutral
`base` branch and must later flow unchanged into both `otto` and `loop24`.
Do not implement it only on a brand branch.

## Required Superpowers workflow

Before doing anything else, locate and follow the repository instructions and
the available Superpowers skills. Use this sequence:

1. Invoke `superpowers:using-superpowers` before responding.
2. Perform read-only repository discovery and evidence gathering.
3. Invoke `superpowers:brainstorming`. Ask clarification questions one at a
   time, present two or three credible approaches with trade-offs, recommend
   one, and obtain explicit design approval. Do not modify product code during
   brainstorming.
4. After design approval, write the design specification to
   `docs/superpowers/specs/YYYY-MM-DD-computer-use-reliability-design.md`.
   Self-review it for placeholders, contradictions, missing acceptance
   criteria, and ambiguous ownership. Ask the user to review the written spec.
5. After spec approval, invoke `superpowers:writing-plans`. Write a detailed,
   TDD-oriented implementation plan to
   `docs/superpowers/plans/YYYY-MM-DD-computer-use-reliability.md`. Include exact
   files, interfaces, failing tests, commands, expected results, small commits,
   and release/merge gates.
6. Stop after presenting the completed plan and offer the execution choices.
   Recommend `superpowers:subagent-driven-development`; offer
   `superpowers:executing-plans` as the inline alternative. Do not begin product
   implementation until the user selects an execution mode.
7. During later execution, use `superpowers:test-driven-development`, the
   selected execution skill, `superpowers:requesting-code-review`, and
   `superpowers:verification-before-completion`. Never claim Windows acceptance
   succeeded unless it was actually run on Windows.

If the current checkout is dirty, treat every existing modification and
untracked file as user-owned. Do not discard, reset, overwrite, stage, or commit
unrelated work. The expected starting branch is `base`, but do not work directly
in the dirty base checkout. After the design is approved, use
`superpowers:using-git-worktrees` to create an isolated feature worktree and
branch from the exact current `origin/base` or an explicitly approved base SHA.
Confirm the branch/worktree strategy before creating it if local `base` has
unpublished commits.

Read `AGENTS.md`, `CLAUDE.md` if present, and the parent workspace instructions
before proposing changes. Verify every path and behavior below against the
current source; this prompt contains investigation findings, not permission to
ignore newer code.

## Product outcome

The common request below should complete through Hermes' `computer_use` tool
and Cua Driver in a short, bounded sequence:

> Use computer use to type 2+2 into my ChatGPT window chat input box.

Expected behavior:

- Cua Driver is already installed and its daemon is healthy.
- Hermes resolves the ChatGPT/Codex window and input target canonically.
- It attempts background delivery once when appropriate.
- If Cua Driver says Chromium/Electron text entry requires foreground delivery,
  Hermes can perform exactly one authorized foreground escalation using the
  supported Cua Driver contract.
- It does not repeat an identical failed call.
- It does not escape to a terminal, PowerShell, `SetForegroundWindow`,
  `System.Windows.Forms.SendKeys`, clipboard injection, or an unrelated
  automation subsystem. If the current wrapper deliberately supports Cua
  Driver's own exact-bound `page` rung, it must remain an explicit,
  capability-proven, approval-gated route rather than a silent fallback.
- It preserves and reports Cua Driver's structured effect, verification, target,
  and error metadata.
- It never reports confirmed success solely because a subprocess returned exit
  code zero.
- The bundled skill teaches exactly the arguments and recovery behavior exposed
  by the implemented tool schema.

The preferred steady-state interaction is roughly:

```text
capture/resolve target
  -> text entry in background
  -> at most one foreground retry if required and authorized
  -> verify or report a qualified/unverified outcome
```

Target two to five `computer_use` calls for the normal case, not an open-ended
trial-and-error loop. Call-count reduction is secondary to correctness,
authorization, and truthful verification.

## Proven environment state

The original Windows failure was not caused by a missing or stopped driver.
The following checks succeeded on the affected machine:

```text
cua-driver --version
cua-driver 0.11.0

cua-driver status
Cua Driver daemon is running
socket: \\.\pipe\cua-driver

cua-driver call health_report
overall: ok
session_active: MCP session is active
ax_capability: pass
screen_capture_capability: pass

loop24 computer-use doctor
✅ cua-driver 0.11.0 on win32 — ok
```

`cua-driver call list_apps` and `cua-driver call list_windows` also returned the
running ChatGPT/Codex window with a real PID and window ID. Direct Cua Driver
enumeration therefore worked.

Historical version note: Cua Driver 0.10.0 rejected `health_report` with:

```text
Permission denied: tool 'health_report' has no reviewed risk classification
```

After installing 0.11.0 and fully replacing the old daemon, `health_report`
succeeded. An updated CLI does not automatically prove the running daemon was
replaced: the old daemon can retain the Windows named pipe, and `autostart kick`
does not necessarily replace an already-running process. Treat 0.11.0 as the
currently proven Windows baseline, but determine from current Cua contracts and
tests whether Hermes should enforce a minimum version or degrade gracefully.
Do not add tray lifecycle management in this workstream.

## Reproduced failure sequence

The recorded Hermes session eventually entered the text, but only after many
unnecessary and unsafe attempts:

1. `capture(mode="som", app="ChatGPT")` initially returned only title-bar
   controls.
2. A coordinate click was delivered to the Chromium window in the background.
3. `type` failed with the actionable Cua Driver message:

   ```text
   Background delivery is not available for target window class
   'Chrome_WidgetWin_1' on this event kind (text_input). Either call
   bring_to_front then retry with delivery_mode:"foreground", or accept the
   foreground swap directly by setting delivery_mode:"foreground".
   ```

4. Hermes called `focus_app` with `raise_window:true`, but the result said the
   target was selected "without raising window."
5. Hermes retried the same `type` call and received the same background refusal.
6. It tried a `key` event, which received the same foreground requirement.
7. `list_apps` exposed the app name as `- ChatGPT.exe`; copying that decorated
   string into `focus_app` failed because no such app existed.
8. Hermes requested `delivery_mode:"foreground"`, but the call still behaved as
   background delivery. Current evidence indicates the model-visible schema and
   dispatcher did not carry that argument to the adapter.
9. The model abandoned the computer-use wrapper and used PowerShell. Two quoting
   attempts failed, then `SetForegroundWindow` returned true, and
   `System.Windows.Forms.SendKeys` returned exit code zero.
10. Hermes declared success after a screenshot, although the SendKeys exit code
    did not itself prove the intended input field accepted the text.

This is a contract and recovery-policy defect in the Hermes wrapper/skill, not a
reason to register a second generic Cua MCP server.

## Current architecture to preserve

Computer use has three layers:

1. `cua-driver serve` is the long-lived daemon that owns UI automation and the
   Windows named pipe in the interactive user session.
2. Hermes' built-in Cua backend starts `cua-driver mcp` as a private stdio child
   and uses it as transport to the daemon.
3. Hermes exposes a higher-level `computer_use` tool plus the bundled
   `skills/computer-use/SKILL.md` operating procedure.

Do not add Cua Driver as a generic user-configured Hermes MCP server. That would
expose a duplicate raw tool catalog alongside the built-in wrapper and create
conflicting action vocabularies, approvals, and instructions.

Do not require users to run `cua-driver skills install`. That command installs
guidance for agents using Cua Driver's raw MCP tools; it does not install the
driver. Hermes already owns and bundles a skill for its higher-level wrapper.
The Hermes skill must remain authoritative.

## Source surfaces to inspect

At minimum, inspect these current modules before designing:

- `tools/computer_use/schema.py`: model-visible JSON tool contract.
- `tools/computer_use/backend.py`: abstract backend interfaces and result types.
- `tools/computer_use/cua_backend.py`: Cua Driver MCP adapter, active target
  state, element tokens, app/window parsing, action/result extraction.
- `tools/computer_use/tool.py`: dispatcher, authorization, capture response
  shaping, and model-visible failures.
- `tools/computer_use_tool.py`: tool registration and high-level description.
- `tools/computer_use/permissions.py`: approval/effect classification.
- `tools/computer_use/doctor.py`: install/readiness/version diagnostics.
- `skills/computer-use/SKILL.md`: bundled model operating procedure.
- `tests/tools/test_computer_use.py` and focused sibling modules.
- `tests/computer_use/test_doctor.py` and other doctor/transport tests.
- `MANIFEST.in`, `setup.py`, and the bundled-skill synchronization/install
  scripts and tests.
- `docs/upstream-customizations/`, the customization checker, current workflow
  merge gates, and their tests.
- The external workspace skill at
  `../.claude/skills/otto-upstream-merge/SKILL.md`.

Confirm these current findings rather than assuming they remain true:

- `Backend.type_text` currently accepts only text.
- Cua adapter `type_text` currently sends only PID and text.
- Keyboard delivery similarly lacks the full target/delivery contract.
- `focus_app(..., raise_window=True)` currently documents or implements the
  operation as a selector and intentionally avoids raising the window.
- The dispatcher does not consistently reject or surface unsupported arguments,
  allowing requested recovery controls to be silently lost.
- App fallback formatting/parsing can leak display bullets into canonical names.
- Action response shaping may flatten structured Cua Driver metadata into a
  message and an `ok` boolean.
- The bundled skill emphasizes never raising a window and ends with an external
  Cua skill-install recommendation that is inappropriate for this wrapper.

Also inspect current upstream and installed Cua Driver contracts. Use official
sources and the locally installed CLI/tool schemas where available:

- https://cua.ai/docs/how-to-guides/driver/connect-your-agent
- https://cua.ai/docs/reference/cua-driver/contracts
- https://cua.ai/docs/concepts/capture-and-delivery-modalities
- https://cua.ai/docs/reference/cua-driver/action-selection-policy
- https://cua.ai/docs/concepts/browser-targeting-and-background-delivery
- https://cua.ai/docs/reference/cua-driver/mcp-tools
- https://cua.ai/docs/reference/cua-driver/cli-reference
- https://cua.ai/docs/how-to-guides/driver/install-agent-skill
- https://hermes-agent.nousresearch.com/docs/user-guide/features/computer-use

When using web research for technical claims, prefer official documentation and
current source/tool schemas. Record any inference separately from confirmed
contract behavior. Do not implement undocumented argument names based only on
the failure text in this prompt.

## Required behavioral contracts

The approved design and implementation plan must address all of the following.
The design may rename fields to match the current official Cua schema, but it
must preserve these outcomes.

### 1. Model-visible schema parity

- Expose the supported delivery modality for applicable pointer and keyboard
  actions, including background and foreground delivery when supported.
- Allow text/key entry to target a resolved element or coordinate as supported
  by Cua Driver, including its one-call Chromium/Electron text-targeting path.
- Carry a stable target identity where needed: PID, window ID, canonical app
  name, and window title.
- Define foreground activation truthfully. Do not advertise `raise_window` if it
  is ignored. Either implement it through the supported Cua primitive with the
  proper approval or replace it with an explicit foreground action/contract.
- Reject invalid or unsupported combinations with structured errors rather than
  silently dropping fields.
- Keep the schema cross-platform. Platform limitations should produce explicit
  capability/failure data, not Windows-only assumptions in shared interfaces.

### 2. Backend and adapter fidelity

- Extend abstract backend signatures and result types so the dispatcher cannot
  lose delivery, target, element, coordinate, window, and verification data.
- Forward supported arguments exactly to the Cua Driver MCP call.
- Preserve active `window_id` and element-token stale-detection behavior.
- Normalize `list_apps`/`list_windows` into canonical data. Presentation bullets
  or labels must never become lookup keys.
- Preserve structured Cua output, including error code/category, retryability,
  effect disposition, verification state, and target metadata when provided.
- Maintain compatibility or produce a deliberate, tested failure for older Cua
  versions whose schemas lack the required arguments.

### 3. Bounded recovery and authorization

- Attempt background delivery no more than once for the same target/action.
- Recognize the structured foreground-required/background-unavailable condition.
- Follow Cua Driver's structured `effect` and `escalation.recommended` data when
  choosing between the element, pixel, page (when deliberately supported), and
  foreground rungs. Do not infer escalation from a successful status alone.
- Permit at most one retry with foreground delivery when the original user
  request authorizes the visible focus change or the approval policy obtains
  consent.
- Do not loop on the same call after the same failure.
- Do not fall back to terminal, shell, PowerShell, SendKeys, clipboard tricks,
  or an unrelated desktop-control path. A Cua `page` route is allowed only if
  the approved design explicitly exposes its exact window/tab binding,
  capability lifetime, and stronger approval boundary; otherwise defer it.
- Decide during design whether the retry belongs inside the wrapper, in the
  skill/model procedure, or in an optional composite action. Compare at least:
  primitive parity only, wrapper-managed one-shot escalation, and a higher-level
  `enter_text` action. Recommend the smallest design that guarantees bounded,
  truthful behavior without hiding approvals.
- Keep foreground delivery separately classified if it changes the user's
  active window, keyboard focus, or desktop.

An example of the shape the model needs—not a mandated exact schema—is:

```json
{
  "ok": false,
  "code": "foreground_required",
  "retryable": true,
  "required_delivery_mode": "foreground",
  "target": {
    "pid": 9656,
    "window_id": 524358,
    "app_name": "ChatGPT.exe",
    "title": "ChatGPT"
  }
}
```

### 4. Truthful effects and verification

- Never report that a window was raised when it was only selected internally.
- Never convert an unverified action into confirmed success.
- Preserve Cua Driver effect/verification metadata through response shaping.
- When verification is unavailable, say so explicitly and use a post-action Cua
  capture only when it can materially verify the requested result.
- Detect a titlebar-only/degraded capture if the available Cua response supports
  doing so reliably; otherwise document why it cannot be inferred safely.

### 5. Bundled skill alignment

Update `skills/computer-use/SKILL.md` so it teaches only calls that the schema
and dispatcher actually support. It must describe:

- canonical capture and target selection;
- element/coordinate text targeting;
- the background-to-foreground escalation ladder;
- the foreground authorization boundary;
- the one-retry budget;
- structured failure handling and verification;
- recapture rules for stale elements;
- an explicit prohibition on terminal/PowerShell/SendKeys fallback;
- the distinction between Cua daemon readiness and Hermes' private MCP
  transport.

Remove the ordinary-user instruction to run `cua-driver skills install`. If
platform reference material is needed, prefer concise, version-aware references
under the bundled Hermes skill rather than runtime downloads.

### 6. Packaging and upgrades

Hermes already ships its bundled `skills` tree through source and wheel
packaging, and its installers synchronize bundled skills into the active
profile. Preserve that architecture.

The plan must verify:

- a clean install contains the updated `computer-use` skill;
- the built wheel/source distribution contains it;
- an upgrade updates an installed copy that still matches the previous bundled
  origin hash;
- a user-modified installed skill is not overwritten;
- bundled-skill opt-out remains respected;
- a new session/reload sees the updated skill contract.

Do not introduce an online skill download into normal install or startup.

### 7. Upstream-merge durability

These changes are downstream, brand-neutral customizations on `base`. They must
not disappear when upstream Hermes `main` is merged into the fork.

The repository already has a machine-readable customization-ledger pattern in
`docs/upstream-customizations/`, a checker, an evidence schema, and workflow
merge gates. The external merge procedure currently hardcodes the
workflow-orchestration manifest in several places.

The design and plan must include:

- A repo-local computer-use customization manifest, preferably
  `docs/upstream-customizations/computer-use.yaml`, recording each modified
  upstream-owned file, owned symbol/contract, tests, expected commit boundary,
  upstream candidacy, merge guidance, removal condition, and
  `last_verified_upstream` baseline.
- Coverage for the tool contract, adapter, dispatcher, permission behavior,
  bundled skill, and packaging invariants.
- A focused offline computer-use merge gate, plus tests for the gate itself.
- Generalization of `../.claude/skills/otto-upstream-merge/SKILL.md` so it
  discovers and validates all customization manifests rather than assuming
  workflow orchestration is the only protected subsystem.
- Stage-0 overlap classification against incoming upstream changes:
  `none`, `same_file`, `owned_symbol`, or `possible_upstream_equivalent`.
- Explicit `preserve`, `adapt`, or `remove-as-upstream-equivalent` decisions for
  owned-symbol/equivalent overlap.
- A prohibition on whole-file `ours`/`theirs` replay for partially owned files.
  Preserve the behavioral contract while unioning valid upstream fixes.
- Base-gate certification of the exact tested `base` SHA before propagating it
  to any brand.
- Brand gates proving each brand contains that exact tested base behavior and
  has not diverged on generic computer-use source or skill files.
- Release evidence that records the manifest baselines, overlap report,
  decisions, test results, and certified SHA.

The repo-local manifest and tests are the durable source of truth. The external
merge skill is an executor and must not be the only place the customization is
documented. Do not "protect" the work by blindly copying old downstream files
back after every upstream merge; that could delete legitimate upstream fixes.

Determine whether the existing customization checker/gates should become a
generic aggregate runner or whether a small computer-use gate should be composed
by a generic top-level gate. Prefer discovery and composition over another
hardcoded feature list. Preserve existing workflow-orchestration behavior and
tests while generalizing it.

## Branch and release model

The fork uses this flow:

```text
upstream origin/main
  -> neutral fork base
  -> generated brand overlays: otto, loop24, and future descriptors
```

For this feature:

```text
feature worktree/branch from base
  -> reviewed and verified feature commits
  -> merge into base
  -> certify exact base SHA with all customization gates
  -> merge that SHA into every discovered brand
  -> regenerate/check branding overlays
  -> build/release verification
```

Do not gate computer-use behavior on brand. Do not hand-edit generated branding
anchors as part of this work.

## Non-goals

Keep the specification focused. The following are out of scope unless discovery
proves they are strictly required for the reliability contract:

- Gateway tray controls for installing, updating, starting, stopping, or
  enabling Cua Driver.
- Generic Hermes MCP registration for Cua Driver.
- Requiring `cua-driver skills install`.
- Replacing Hermes' computer-use wrapper with raw Cua MCP tools.
- Adding Cua browser/CDP attachment or page automation unless the design proves
  it is necessary for this reliability slice and preserves its explicit
  security approvals.
- Redesigning Cua Driver's daemon/autostart/update lifecycle.
- Adding brand-specific behavior for OTTO or LOOP24.
- Unrelated computer-use refactoring.
- Claiming support for untested platforms or driver versions.

## Required automated verification

The spec must turn these into explicit acceptance criteria, and the plan must
name exact tests and expected assertions.

### Schema and dispatch

- Delivery mode appears in the model schema with only supported values.
- Text/key targeting fields appear only where valid.
- Unknown or invalid field combinations fail explicitly.
- The dispatcher forwards every supported field to the backend exactly once.
- Model-visible descriptions and bundled skill examples match the schema.

### Adapter and targeting

- A mocked Cua MCP call receives the expected delivery mode, PID, window ID,
  element token/index or coordinate, and text/key payload.
- Canonical app/window results never include presentation bullets in lookup
  names.
- Null-PID/hosted-window Windows behavior remains covered.
- Active-window and stale-element protections remain intact.
- Cua structured effects, verification, targets, and failure metadata survive
  extraction and response shaping.

### Recovery and safety

- A simulated Chromium background refusal produces either one structured
  foreground-required response or exactly one approved foreground retry,
  according to the approved architecture.
- The same failed call is not repeated indefinitely.
- Foreground delivery cannot bypass its approval/effect classification.
- `focus_app` does not claim to raise a window unless it actually does.
- No computer-use recovery path invokes terminal/shell/PowerShell/SendKeys.
- Failed or unverifiable actions cannot become confirmed success.

### Packaging and merge protection

- Packaging metadata includes the updated bundled skill.
- Clean profile sync installs it.
- Pristine old copies update and user-modified copies remain untouched.
- The new customization manifest validates against the checker.
- Upstream overlap detection identifies edits to an owned computer-use file or
  symbol.
- The aggregate/base gate runs the focused computer-use tests.
- Brand gates reject generic computer-use divergence from the certified base
  SHA.
- Existing workflow customization gates continue to pass after generalization.

Run the focused tests first, then the broader affected Python test suite,
packaging tests, customization-gate tests, formatting/lint checks required by
the repo, and any documented release preflight. The plan must specify actual
commands after inspecting the environment (`venv`, `.venv`, or repo tooling);
do not guess commands that cannot run.

## Required Windows acceptance verification

Automated mocks are necessary but insufficient. Before release, repeat the
original scenario on Windows with Cua Driver 0.11.0 or the newly approved
minimum:

1. Record `cua-driver --version`.
2. Record `cua-driver status` and confirm the expected running daemon version,
   not only the CLI version.
3. Run `cua-driver call health_report`; require `overall: ok` and an active MCP
   session.
4. Run the branded `computer-use doctor`; require a readable healthy report.
5. Open ChatGPT/Codex and run:

   ```text
   Use computer use to type 2+2 into my ChatGPT window chat input box.
   ```

6. Capture tool frames or an exported session and prove:
   - the correct canonical window was selected;
   - no generic MCP configuration was required;
   - no terminal, PowerShell, SendKeys, clipboard, or alternate automation tool
     was used;
   - no identical failure was repeated;
   - foreground escalation occurred at most once and only with authorization;
   - the text appeared in the intended input field;
   - the final response distinguished verified success from unverified action.
7. Regression-check capture, click, key, type, drag, scroll, and focus behavior
   on the supported Windows path.

If the active development environment is not Windows, write this as a pending
release acceptance gate. Do not fake it, silently skip it, or claim completion.

For other platforms, preserve existing macOS/Linux unit and integration
coverage. Add real-platform acceptance only when an appropriate host is
available.

## Design questions the brainstorming session must resolve

Ask these one at a time and use repository/official-contract evidence to narrow
them before asking the user:

1. Should foreground escalation be wrapper-managed, model/skill-managed, or an
   optional composite `enter_text` action? Recommend one based on approval
   visibility, bounded retries, and implementation size.
2. What exact Cua Driver tool arguments and structured failure codes exist in
   the current supported version?
3. Does foreground delivery require a distinct approval category, or can an
   existing visible-focus effect classification represent it precisely?
4. Should Hermes enforce a minimum driver version, feature-detect supported MCP
   schemas, or combine a minimum with graceful capability detection?
5. What constitutes verified text-entry success with the current Cua effect
   contract, and when is a follow-up capture necessary?
6. Should merge protection use one generic aggregate gate or compose focused
   feature gates under a generic runner?
7. Is an optional composite action justified in the first implementation, or
   should it be deferred until primitive parity is proven?

Do not ask the user questions that the current source, tests, installed CLI, or
official documentation can answer.

## Deliverables from this planning session

Before implementation begins, produce:

1. A repository evidence summary with current file/symbol references and any
   corrections to this prompt's findings.
2. Two or three design approaches with explicit trade-offs and a recommendation.
3. An approved design specification containing architecture, interfaces, state
   flow, approvals, error model, compatibility, skill guidance, packaging,
   upstream-merge protection, and acceptance criteria.
4. A self-reviewed, committed specification on an isolated feature branch.
5. A TDD implementation plan with small independently reviewable tasks and
   exact test commands.
6. A clear list of verification that can run locally versus Windows acceptance
   that remains pending.
7. An execution handoff offering subagent-driven implementation as the
   recommended option.

The plan should be complete enough that independent task agents can implement
it without rereading this prompt, while the spec remains the source of product
and behavioral truth.
