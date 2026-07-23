# Computer Use Reliability and Deferred Tray Design

**Date:** 2026-07-23  
**Status:** Hermes reliability work recommended; Gateway tray lifecycle work deferred  
**Audience:** Engineers maintaining the LOOP24 Hermes fork and the Gateway tray  
**Post-read action:** Decide whether to implement either workstream and identify the exact integration, packaging, and verification points without repeating the Windows investigation.

## Decision Summary

The Gateway tray will not manage Cua Driver in the current workstream. The lifecycle design is retained below for possible future use.

The active recommendation is to improve the branded Hermes `computer_use` wrapper and its bundled skill. The long Windows interaction that eventually typed `2+2` into ChatGPT did not fail because Cua Driver was unavailable. It failed because the wrapper hid or discarded Cua Driver recovery controls, then left the model to improvise through PowerShell.

LOOP24 does not need a generic Cua Driver MCP entry or a separately installed Cua Driver skill for normal computer use. Hermes already exposes a higher-level built-in `computer_use` tool, launches `cua-driver mcp` as its private transport, and bundles a cross-platform `computer-use` skill. The bundled Hermes skill should remain the authoritative model guidance because it describes the wrapper vocabulary the model can actually call.

## Current Architecture

Computer use has three distinct layers:

1. **Cua Driver daemon:** `cua-driver serve` owns UI Automation state and the Windows named pipe. On Windows it must run in the interactive user session.
2. **Hermes transport:** the built-in backend starts `cua-driver mcp` as a private stdio child. That MCP process proxies calls to the long-lived daemon.
3. **Hermes tool and skill:** Hermes exposes one higher-level `computer_use` tool and teaches the model its action vocabulary through the bundled `computer-use` skill.

Do not also register Cua Driver as a generic Hermes MCP server. That would expose a second raw tool catalog alongside the built-in wrapper, creating duplicate actions and conflicting instructions.

## Why the Successful Windows Run Took Too Many Attempts

The recorded run contained a deterministic failure loop:

1. The first capture exposed only window chrome, so the model used a coordinate click.
2. Cua Driver rejected background text delivery to `Chrome_WidgetWin_1` and explicitly instructed the caller to retry with foreground delivery.
3. The model requested `delivery_mode: "foreground"`, but the Hermes schema did not declare it and the dispatcher did not forward it.
4. The model requested `focus_app` with `raise_window: true`, but the backend intentionally ignored the flag while returning a successful targeting result.
5. `list_apps` text fallback retained a leading bullet in `- ChatGPT.exe`, producing a failed app lookup.
6. With no valid recovery path, the model used terminal-driven PowerShell. Two quoting attempts failed before `SetForegroundWindow` ran, and the final text entry used `System.Windows.Forms.SendKeys` outside Cua Driver.
7. A zero exit code from SendKeys was treated as success even though it did not prove the target application accepted the text.

This should be a bounded capture, action, escalation, and verification sequence—not an open-ended retry loop.

## Recommended Hermes Behavior

### Tool contract

The wrapper should expose the Cua Driver controls needed for deterministic recovery:

- Add `delivery_mode` with `background` and `foreground` values to the applicable pointer and keyboard actions.
- Allow `type` and `key` to target an accessibility element or a screenshot coordinate, not only the last active process.
- Pass the active `window_id` for foreground delivery.
- Support Cua Driver's one-call Chromium/Electron path in which `type_text` pixel-clicks a field and types into it.
- Stop silently accepting unsupported arguments.
- Separate app/window selection from foreground activation. Either make `raise_window` truthful or replace it with an explicit, approval-gated foreground action.

### Recovery policy

For an authorized text-entry request, the recommended ladder is:

1. Capture and resolve a canonical target window and input field.
2. Attempt background delivery once.
3. If Cua Driver returns `background_unavailable` or its equivalent, retry the same action once with foreground delivery when the request permits foreground disruption.
4. Do not repeat an identical call after an identical failure.
5. Do not fall back to terminal, PowerShell, SendKeys, clipboard tricks, or another automation subsystem.
6. Verify with Cua Driver's `effect`/`verified` data and a post-action capture when needed.

The wrapper should return machine-actionable failures such as:

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

### Target identity

Actions should carry a canonical target snapshot containing the process ID, window ID, executable/app name, and title. `list_apps` fallback parsing must remove presentation bullets and should never require the model to copy a decorated display string back into an action.

A capture that contains only title-bar controls should report a degraded or titlebar-only signal. The model can then use the existing screenshot, try a bounded accessibility recapture, or request foreground activation instead of guessing indefinitely.

### Optional composite action

After contract parity is restored, a higher-level `enter_text` action can make the common path nearly atomic:

```text
resolve app and field
  -> type in background
  -> retry once in foreground if explicitly permitted
  -> capture and verify
```

This would reduce the demonstrated run to approximately two or three tool calls while preserving the same approval and safety boundaries.

## Hermes Files to Touch

The paths below are relative to the branded `hermes-agent` repository.

### Required

| File | Responsibility | Expected change |
|---|---|---|
| `tools/computer_use/schema.py` | Model-visible tool contract | Add delivery mode and keyboard element/coordinate targeting; clarify foreground semantics; reject unknown fields if the tool surface supports strict schemas. |
| `tools/computer_use/backend.py` | Backend interface | Extend type/key and relevant pointer signatures with delivery and target parameters; define truthful foreground activation semantics. |
| `tools/computer_use/cua_backend.py` | Cua Driver adapter | Forward `delivery_mode`, `window_id`, element/token, and coordinate arguments; normalize app results; preserve structured Cua Driver error/effect metadata. |
| `tools/computer_use/tool.py` | Dispatcher, approval, and response shaping | Route the new arguments, produce structured recovery responses, enforce the retry/approval policy, and prevent silent argument loss. |
| `skills/computer-use/SKILL.md` | Model operating procedure | Document the background-to-foreground ladder, one-call Electron text targeting, retry budget, verification rules, and prohibition on terminal/SendKeys fallback. |
| `tests/tools/test_computer_use.py` or a focused sibling test module | Contract regression coverage | Prove argument forwarding, foreground recovery, structured failures, canonical targets, and no duplicate retries. |

### Likely or conditional

| File | When it is needed |
|---|---|
| `tools/computer_use_tool.py` | Update the registry description if it over-promises universal background delivery or omits the cross-platform foreground fallback. |
| `tools/computer_use/permissions.py` | Use only if foreground delivery requires a distinct approval/effect classification. |
| `tools/computer_use/doctor.py` | Use only if the active tool should enforce a minimum compatible driver version or expose readiness diagnostics before the first action. |
| `tests/tools/test_computer_use_null_pid_windows.py` | Extend if canonical Windows target parsing changes null-PID or hosted-window handling. |
| `tests/computer_use/test_doctor.py` | Extend if minimum-version or readiness behavior changes. |
| `tests/test_packaging_metadata.py` | Add a focused assertion if release verification needs stronger proof that the bundled computer-use skill is present. |

### Not required for the reliability fix

The lifecycle/install commands, desktop settings panel, and Gateway tray do not need to change merely to fix action delivery. `hermes_cli/tools_config.py`, `hermes_cli/main.py`, and Gateway tray sources belong to separate install/readiness and lifecycle workstreams.

## Skill Installation and Packaging Decision

### What is already bundled

The branded Hermes source already contains `skills/computer-use/SKILL.md`. It is cross-platform and is discovered as the `computer-use` skill on macOS, Windows, and Linux.

Hermes packaging already includes the entire bundled `skills` tree:

- Source distributions include it through the skills graft in `MANIFEST.in`.
- Wheels include it through the recursive data-file collection in `setup.py`.
- Windows and Unix installers run the manifest-based skill synchronizer after installing Hermes.
- The synchronizer copies new bundled skills into the active profile, updates pristine bundled copies on later releases, and preserves user-modified copies.

Therefore the improved Hermes skill can ship with LOOP24 simply by updating the existing bundled skill before building the custom release. No additional end-user skill-install command is needed.

Profiles created with bundled-skill opt-out enabled intentionally do not receive it. That behavior should remain respected.

### External Cua Driver skill pack

Cua Driver offers `cua-driver skills install`, but that command installs agent instructions, not the driver executable. It can install only the current platform guide or all platform guides. It is useful for generic MCP clients that expose Cua Driver's raw tools.

It should not be a runtime requirement for LOOP24 because:

- the Hermes wrapper uses a different, higher-level action vocabulary;
- a raw-tool skill can teach calls that the Hermes wrapper does not expose;
- runtime installation introduces network and upstream-version drift;
- two computer-use skills can give the model conflicting escalation and safety rules;
- LOOP24 already has a deterministic bundled-skill update path.

Recommended policy:

1. Keep the bundled Hermes `computer-use` skill authoritative.
2. Remove the current instruction that asks ordinary users to run `cua-driver skills install`.
3. If a platform-specific Cua explanation is essential, summarize the relevant behavior in `skills/computer-use/references/` and keep it aligned with the minimum supported Cua Driver version.
4. Do not expose the raw Cua Driver MCP catalog solely to make an external skill applicable.

An alternative build-time approach is to fetch a version-pinned Cua skill pack and stage vetted references into the bundled Hermes skill. This is less desirable because it adds licensing, provenance, and version synchronization work to every release. It should be used only if maintaining concise Hermes-owned references proves insufficient.

## Release and Upgrade Behavior

For a new LOOP24 release:

1. The improved source skill is included in the build artifacts by existing packaging rules.
2. The installer or updater runs Hermes' bundled-skill synchronizer.
3. A new installation receives the skill in its active profile automatically.
4. An existing installation receives the updated skill only when its installed copy still matches the previously bundled origin hash.
5. A user-modified copy is preserved and reported as user-modified rather than overwritten.

This behavior is appropriate for upgrades. It prevents LOOP24 from clobbering local skill customization while ensuring ordinary installs receive the correction.

Release verification should inspect both the wheel/source artifact and a clean upgraded profile. Confirm that the active profile contains the updated `computer-use` skill, that a deliberately modified copy is not overwritten, and that the model-visible skill description appears after a new session or skill reload.

## Deferred Gateway Tray Work

No Gateway tray changes are authorized by this document. If lifecycle management is revived, the tray should distinguish these states:

- Cua Driver not installed.
- CLI installed but daemon stopped.
- Daemon running but installed and running versions differ.
- Daemon reachable but MCP health degraded.
- Ready: daemon reachable and `health_report` reports overall success.
- Update available.

The tray should use official Cua Driver lifecycle commands rather than owning an unmanaged `serve` process. On Windows, autostart is a Scheduled Task tied to the interactive user session.

Known Windows findings that must be preserved:

- Cua Driver 0.10.0 denied `health_report` because it lacked a reviewed risk classification. Version 0.11.0 corrected this and should be treated as the practical minimum for the current integration.
- Updating the CLI does not replace an already-running daemon. The old daemon can continue owning the named pipe and serving the old behavior until it is explicitly stopped and restarted.
- `autostart kick` does not replace an existing daemon. Stop and verify the old process is gone before kicking the task.
- A successful `status` command proves daemon liveness, not end-to-end MCP readiness.
- A future update flow must validate the advertised release asset before applying it and must not rely on an updater process tree that a legacy migration can terminate.

If tray lifecycle work is revived, one product decision remains open: whether **Disable Computer Use** persistently stops the daemon and removes autostart, or merely stops it until the next logon. Persistent disable was the earlier recommendation because it matches the wording users will see.

## Verification Criteria for Hermes Reliability Work

Automated coverage should prove:

- `delivery_mode` is present in the model schema and reaches the exact Cua Driver call.
- Type/key can carry element or coordinate targeting plus `window_id`.
- A simulated Chromium background refusal yields one structured foreground recovery, not repeated identical calls.
- Foreground delivery is approval-gated according to the chosen policy.
- `focus_app` never claims to raise a window when it did not.
- App names are canonical and presentation bullets are removed.
- Cua Driver `effect`, `verified`, error code, and metadata survive response shaping.
- The wrapper never invokes terminal or PowerShell as a GUI-input fallback.
- A failed or unverifiable action cannot be reported as confirmed success.
- The bundled skill teaches the same contract implemented by the schema and dispatcher.

Windows acceptance should repeat the original request against ChatGPT/Codex:

> Use computer use to type 2+2 into my ChatGPT window chat input box.

Expected behavior: no generic MCP configuration, no PowerShell, no SendKeys, no repeated identical failure, at most one foreground escalation, and a verified or explicitly qualified result.

## References

- [Cua Driver interface contracts](https://cua.ai/docs/reference/cua-driver/contracts)
- [Capture and delivery modalities](https://cua.ai/docs/concepts/capture-and-delivery-modalities)
- [Cua Driver MCP tools](https://cua.ai/docs/reference/cua-driver/mcp-tools)
- [Install the Cua Driver agent skill](https://cua.ai/docs/how-to-guides/driver/install-agent-skill)
- [Cua Driver CLI reference](https://cua.ai/docs/reference/cua-driver/cli-reference)
- [Hermes computer use documentation](https://hermes-agent.nousresearch.com/docs/user-guide/features/computer-use)
