---
phase: quick-260531-fba
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/otto-gw
  - scripts/otto-gw.ps1
autonomous: true
requirements:
  - QUICK-260531-fba
must_haves:
  truths:
    - "otto-gw init --non-interactive produces an active KEY=VALUE set semantically identical to the old heredoc output"
    - "The template file scripts/.env.otto-gw.example is the single source of truth; no duplicate key list exists in the bash or PS1 wrappers"
    - "All eight resolved keys (AUTH_TOKEN, KIRO_CMD, HTTP_ADDR, ENABLED_HOOKS, PII_REDACTION_ENABLED, PII_REDACTION_MODE, PII_HASH_KEY, CHAT_TRACE) are written with the correct value and comment state"
    - "Keys the template ships that init does not resolve (POOL_SIZE, DEBUG, ALLOWED_IPS, etc.) pass through untouched"
    - "--force re-init preserves AUTH_TOKEN/PII_HASH_KEY/KIRO_CMD/HTTP_ADDR/PII mode/chat-trace/auth state; --regenerate-secrets mints fresh ones"
    - "shellcheck scripts/otto-gw passes clean"
    - "Two fresh inits produce different AUTH_TOKEN and PII_HASH_KEY values"
    - "Missing template file causes a clear error naming the expected path, not a silent fallback"
  artifacts:
    - path: "scripts/otto-gw"
      provides: "Refactored init_cmd() using template rendering"
      contains: "set_env_line"
    - path: "scripts/otto-gw.ps1"
      provides: "Refactored Invoke-Init using template rendering (review-only, no runtime)"
      contains: "Set-EnvLine"
  key_links:
    - from: "init_cmd() in scripts/otto-gw"
      to: "scripts/.env.otto-gw.example"
      via: "$OTTO_SCRIPT_DIR/.env.otto-gw.example"
      pattern: "OTTO_SCRIPT_DIR.*env.otto-gw.example"
---

<objective>
Refactor scripts/otto-gw init_cmd() (and the symmetric Invoke-Init in scripts/otto-gw.ps1)
to render the .env output by transforming scripts/.env.otto-gw.example rather than
writing a duplicate embedded heredoc.

Purpose: Eliminate the drift risk of two key-list sources. After this change, adding a
feature key to .env.otto-gw.example automatically flows to init output without touching
the wrapper. The per-key "set value + toggle comment" helper (set_env_line) is also the
building block for the planned env-merge-on-upgrade feature.

Output:
- scripts/otto-gw — heredoc replaced with template-copy + set_env_line calls
- scripts/otto-gw.ps1 — symmetric refactor (review-only: no pwsh on this macOS box;
  correctness asserted by code review, not runtime execution)
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@scripts/.env.otto-gw.example
@scripts/otto-gw
@scripts/otto-gw.ps1
</context>

<tasks>

<task type="auto">
  <name>Task 1: Capture baseline .env outputs BEFORE any edits</name>
  <files>
    /tmp/base-default.env
    /tmp/base-auth.env
    /tmp/base-chattrace.env
    /tmp/base-pii-off.env
    /tmp/base-pii-replace.env
    /tmp/base-addr.env
    /tmp/base-kiro.env
  </files>
  <action>
The worktree is at unmodified HEAD. Capture baseline outputs NOW, before touching any source
file. Run each command with --non-interactive and a unique --dest so they do not collide:

  bash scripts/otto-gw init --non-interactive --dest /tmp/base-default.env
  bash scripts/otto-gw init --non-interactive --auth-enabled --dest /tmp/base-auth.env
  bash scripts/otto-gw init --non-interactive --chat-trace --dest /tmp/base-chattrace.env
  bash scripts/otto-gw init --non-interactive --pii off --dest /tmp/base-pii-off.env
  bash scripts/otto-gw init --non-interactive --pii replace --dest /tmp/base-pii-replace.env
  bash scripts/otto-gw init --non-interactive --addr 127.0.0.1:11434 --dest /tmp/base-addr.env
  bash scripts/otto-gw init --non-interactive --kiro /usr/local/bin/kiro --dest /tmp/base-kiro.env

Also capture the force-reinit baseline: seed a .env with custom secrets, then run
--force --non-interactive to confirm preservation:

  cat > /tmp/reinit-seed.env <<'SEED'
  AUTH_TOKEN=BASELINE_TOKEN_ABCDEF
  KIRO_CMD=/baseline/kiro
  HTTP_ADDR=127.0.0.1:18080
  ENABLED_HOOKS=RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook
  PII_REDACTION_ENABLED=false
  PII_REDACTION_MODE=replace
  PII_HASH_KEY=BASELINE_HASH_KEY_ABCDEF
  CHAT_TRACE=false
  SEED
  cp /tmp/reinit-seed.env /tmp/reinit-before.env
  bash scripts/otto-gw init --force --non-interactive --dest /tmp/reinit-before.env

Verify /tmp/reinit-before.env still contains BASELINE_TOKEN_ABCDEF and BASELINE_HASH_KEY_ABCDEF.
Also note AUTH_TOKEN must be commented (auth disabled because the seeded file had it active-but-
disabled-by-PII-off convention — actually the seed above has no comment prefix, so re-check
which value it picks up via extract_env_any vs env_is_uncommented logic to set expected baseline).

Store all /tmp/base-*.env files. Do not modify any source file in this task.
  </action>
  <verify>
    <automated>ls -la /tmp/base-default.env /tmp/base-auth.env /tmp/base-chattrace.env /tmp/base-pii-off.env /tmp/base-pii-replace.env /tmp/base-addr.env /tmp/base-kiro.env && grep -E '^#? *[A-Z_]+=' /tmp/base-default.env | sort</automated>
  </verify>
  <done>
Seven /tmp/base-*.env files exist and are non-empty. The reinit-before.env test confirms
extract_env_any + env_is_uncommented baseline behavior is recorded.
  </done>
</task>

<task type="auto">
  <name>Task 2: Refactor init_cmd() in scripts/otto-gw to render from template</name>
  <files>scripts/otto-gw</files>
  <action>
Read scripts/otto-gw fully before editing. Then apply the following changes to init_cmd()
(lines ~795-844 in current HEAD). All value-resolution logic (flags, re-init defaults,
secret generation, prompt logic) stays exactly as-is — only the write step changes.

--- HELPER FUNCTION ---

Add a helper function set_env_line before init_cmd() (or in the helpers section near
extract_env_any, around line 110-160). Signature:

  set_env_line FILE KEY VALUE COMMENTED

Behavior:
- If COMMENTED is "1" (or "true"), writes:  # KEY=VALUE
- If COMMENTED is "0" (or "false"), writes: KEY=VALUE
- Operates in-place on FILE using sed. Must work on both BSD sed (macOS) and GNU sed.
  Use a temporary file + mv pattern to avoid BSD sed -i '' vs GNU sed -i '' portability
  issues: write to FILE.tmp then mv FILE.tmp FILE.
- The line to replace is any line matching:  ^[[:space:]]*#*[[:space:]]*KEY=
  (handles both commented and uncommented forms of the key)
- Use awk for the rewrite (more portable than sed for multi-pattern line replacement):

    awk -v key="$KEY" -v val="$VAL" -v commented="$COMMENTED" '
      BEGIN { prefix = (commented == "1") ? "# " : "" }
      $0 ~ ("^[[:space:]]*#*[[:space:]]*" key "=") {
        print prefix key "=" val
        next
      }
      { print }
    ' "$FILE" > "${FILE}.tmp" && mv "${FILE}.tmp" "$FILE"

  This is POSIX awk — no gawk extensions required.

--- TEMPLATE PATH RESOLUTION ---

Inside init_cmd(), before the `# Ensure parent dir exists` block, add:

  local template_file="$OTTO_SCRIPT_DIR/.env.otto-gw.example"
  if [[ ! -f "$template_file" ]]; then
      echo "ERROR: init template not found: $template_file" >&2
      echo "       The file scripts/.env.otto-gw.example must ship alongside otto-gw." >&2
      exit 1
  fi

$OTTO_SCRIPT_DIR is already resolved at script startup (line 40) — reuse it directly.

--- REPLACE THE HEREDOC ---

Remove the entire `cat > "$dest" <<EOF ... EOF` block (lines ~797-844) and replace with:

  # Copy the golden-copy template and substitute resolved values.
  # Header: strip the "Copy to…" instructional comment block (lines 1-7) and
  # prepend a "Generated by" header so the live .env reads cleanly. The
  # instructional block runs from line 1 through the first blank line after
  # the last header-only comment (before the first KEY= or section comment).
  # Detect it as: all lines up to (but not including) the first line that
  # is NOT a comment-only line — i.e., strip the leading comment-only header.
  {
      echo "# Generated by 'otto-gw init' on $(date -u +%Y-%m-%dT%H:%M:%SZ)"
      echo "# Edit any value; restart the gateway to apply."
      echo ""
      awk '/^[[:space:]]*[^#[:space:]]|^[[:space:]]*#[[:space:]]*-|^[[:space:]]*# [A-Z]/ { found=1 }
           found { print }' "$template_file"
  } > "$dest"

  IMPORTANT: The awk expression above may not correctly strip the header block. Use
  a simpler and safer approach: the template header is exactly the lines from line 1
  to the first blank line after the comment block (lines 1–7 based on current file).
  Use tail to skip the known header and let it be stable:

    Actually the most robust approach: strip all leading lines that match ^#[^-]
    (comment lines that are NOT section-header comments starting with "# ---").
    A line is "header" if it starts with "# " and contains no KEY= reference and
    appears before the first blank line. Use this awk:

      awk 'NR==1 { if ($0 ~ /^# /) { in_header=1 } }
           in_header && /^$/ { in_header=0; next }
           in_header { next }
           { print }' "$template_file"

    This drops everything from line 1 up to and including the first blank line
    when the file starts with comments (the instructional header). The template
    currently has lines 1-8 as the header block (7 comment lines + 1 blank line).
    If the template header is ever extended, the awk still works correctly.

  Full replacement block:

    # Copy template, strip instructional header, prepend generated header.
    {
        printf '# Generated by '"'"'otto-gw init'"'"' on %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf '# Edit any value; restart the gateway to apply.\n'
        printf '\n'
        awk 'NR==1 && /^#/ { in_header=1 }
             in_header && /^$/ { in_header=0; next }
             in_header { next }
             { print }' "$template_file"
    } > "$dest"

    # Apply per-install resolved values to the template copy.
    set_env_line "$dest" AUTH_TOKEN     "$auth_token"      "$( [[ "$auth_enabled" -ne 1 ]] && echo 1 || echo 0 )"
    set_env_line "$dest" KIRO_CMD       "$kiro_cmd"        "0"
    set_env_line "$dest" HTTP_ADDR      "$http_addr"       "0"
    set_env_line "$dest" ENABLED_HOOKS  "$enabled_hooks_value" "0"
    set_env_line "$dest" PII_REDACTION_ENABLED "$pii_enabled"   "0"
    set_env_line "$dest" PII_REDACTION_MODE    "$pii_mode_value" "0"
    set_env_line "$dest" PII_HASH_KEY   "$hash_key"        "0"
    set_env_line "$dest" CHAT_TRACE     "$chat_trace_value" "$( [[ "$chat_trace" -ne 1 ]] && echo 1 || echo 0 )"

Note: the auth_token_prefix and chat_trace_prefix local variables are no longer needed
for the write step — the comment state is now expressed via the COMMENTED argument to
set_env_line. They can be removed (they are not used anywhere else in init_cmd). The
values they guarded ($auth_token and $chat_trace_value) are still needed — keep those.
Remove only the *_prefix variables and their assignment blocks.

Keep everything else in init_cmd() unchanged:
- The chmod 600 line
- The summary echo block (lines ~848-872)
- All flag parsing
- All value-resolution logic
  </action>
  <verify>
    <automated>shellcheck scripts/otto-gw</automated>
  </verify>
  <done>
shellcheck passes clean. The heredoc is gone. set_env_line is defined. The template
path is resolved via $OTTO_SCRIPT_DIR/.env.otto-gw.example with a clear error on missing.
  </done>
</task>

<task type="auto">
  <name>Task 3: Diff-verify bash refactor + refactor PS1 Invoke-Init (review-only)</name>
  <files>
    scripts/otto-gw.ps1
  </files>
  <action>
--- PART A: BASH DIFF VERIFICATION ---

Run all seven variant inits with the refactored script:

  bash scripts/otto-gw init --non-interactive --dest /tmp/new-default.env
  bash scripts/otto-gw init --non-interactive --auth-enabled --dest /tmp/new-auth.env
  bash scripts/otto-gw init --non-interactive --chat-trace --dest /tmp/new-chattrace.env
  bash scripts/otto-gw init --non-interactive --pii off --dest /tmp/new-pii-off.env
  bash scripts/otto-gw init --non-interactive --pii replace --dest /tmp/new-pii-replace.env
  bash scripts/otto-gw init --non-interactive --addr 127.0.0.1:11434 --dest /tmp/new-addr.env
  bash scripts/otto-gw init --non-interactive --kiro /usr/local/bin/kiro --dest /tmp/new-kiro.env

For each pair, compare key-level equivalence (secrets differ by design — compare structure,
not secret values):

  for variant in default auth chattrace pii-off pii-replace addr kiro; do
    echo "=== $variant ==="
    diff <(grep -E '^#? *[A-Z_]+=' /tmp/base-${variant}.env | sed 's/=.*/=<VALUE>/' | sort) \
         <(grep -E '^#? *[A-Z_]+=' /tmp/new-${variant}.env  | sed 's/=.*/=<VALUE>/' | sort)
  done

This compares: (a) which keys are active vs commented, (b) that the full key set is identical.
Any diff output is a regression. Allow only one expected difference: the header text
("# Generated by..." timestamp changes; the instructional "# Copy to..." lines are gone in new
output — that is intentional). Verify no active KEY=VALUE pair is missing or gains/loses
its comment prefix unexpectedly.

Force-reinit secret preservation test:

  printf 'AUTH_TOKEN=REINIT_TOKEN_FIXED\nPII_HASH_KEY=REINIT_HASH_FIXED\nKIRO_CMD=/test/kiro\nHTTP_ADDR=127.0.0.1:18080\nENABLED_HOOKS=RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook\nPII_REDACTION_ENABLED=true\nPII_REDACTION_MODE=hash\nCHAT_TRACE=false\n' > /tmp/reinit-test.env
  bash scripts/otto-gw init --force --non-interactive --dest /tmp/reinit-test.env
  grep AUTH_TOKEN /tmp/reinit-test.env
  grep PII_HASH_KEY /tmp/reinit-test.env

Both must show the original REINIT_TOKEN_FIXED / REINIT_HASH_FIXED values (not regenerated).

Regenerate-secrets test:

  cp /tmp/reinit-test.env /tmp/reinit-regen.env
  bash scripts/otto-gw init --force --non-interactive --regenerate-secrets --dest /tmp/reinit-regen.env
  grep AUTH_TOKEN /tmp/reinit-regen.env
  grep PII_HASH_KEY /tmp/reinit-regen.env

Both must show values different from REINIT_TOKEN_FIXED / REINIT_HASH_FIXED.

Two fresh inits must produce different secrets:

  bash scripts/otto-gw init --non-interactive --dest /tmp/fresh1.env
  bash scripts/otto-gw init --non-interactive --dest /tmp/fresh2.env
  tok1=$(grep '^AUTH_TOKEN=' /tmp/fresh1.env | cut -d= -f2)
  tok2=$(grep '^AUTH_TOKEN=' /tmp/fresh2.env | cut -d= -f2)
  [[ "$tok1" != "$tok2" ]] && echo "PASS: secrets are unique" || echo "FAIL: secrets identical"

--- PART B: POWERSHELL REFACTOR (review-only, no runtime) ---

NOTE: No pwsh is available on this macOS box. The PS1 refactor is code-review-only.
Assert correctness by reading the code, not by executing it.

Read scripts/otto-gw.ps1 Invoke-Init fully (already read during planning). Apply the
symmetric refactor to the `$content = @" ... "@` block (lines ~625-672):

1. Add a helper function Set-EnvLine before Invoke-Init:

    function Set-EnvLine {
        param(
            [string]$FilePath,
            [string]$Key,
            [string]$Value,
            [bool]$Commented
        )
        $prefix = if ($Commented) { '# ' } else { '' }
        $newLine = "${prefix}${Key}=${Value}"
        $content = Get-Content $FilePath
        $updated = $content | ForEach-Object {
            if ($_ -match "^\s*#*\s*${Key}=") { $newLine }
            else { $_ }
        }
        Set-Content -Path $FilePath -Value $updated -Encoding UTF8
    }

2. Resolve the template path. Near the top of Invoke-Init, after $destPath is set,
   add:

    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $templateFile = Join-Path $scriptDir '.env.otto-gw.example'
    if (-not (Test-Path $templateFile)) {
        Write-Error "ERROR: init template not found: $templateFile"
        Write-Error "       The file scripts\.env.otto-gw.example must ship alongside otto-gw.ps1."
        exit 1
    }

3. Replace the $content = @" ... "@ block and the Set-Content call that follows it with:

    $ts = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

    # Read template, strip instructional header (leading comment block up to
    # first blank line), prepend generated header.
    $templateLines = Get-Content $templateFile
    $inHeader = $templateLines[0] -match '^#'
    $bodyLines = @()
    foreach ($ln in $templateLines) {
        if ($inHeader) {
            if ($ln -eq '') { $inHeader = $false }
            continue
        }
        $bodyLines += $ln
    }
    $generatedHeader = @(
        "# Generated by 'otto-gw init' on $ts",
        "# Edit any value; restart the gateway to apply.",
        ""
    )
    Set-Content -Path $destPath -Value ($generatedHeader + $bodyLines) -Encoding UTF8

    # Apply per-install resolved values.
    Set-EnvLine -FilePath $destPath -Key 'AUTH_TOKEN'            -Value $authTokenValue  -Commented (-not $authOn)
    Set-EnvLine -FilePath $destPath -Key 'KIRO_CMD'              -Value $kiroLine.Split('=',2)[1] -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'HTTP_ADDR'             -Value $addrValue       -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'ENABLED_HOOKS'         -Value $enabledHooksValue -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'PII_REDACTION_ENABLED' -Value $piiEnabled      -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'PII_REDACTION_MODE'    -Value $piiModeValue    -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'PII_HASH_KEY'          -Value $hashKeyValue    -Commented $false
    Set-EnvLine -FilePath $destPath -Key 'CHAT_TRACE'            -Value $chatValue       -Commented (-not $chatOn)

   For the KIRO_CMD line, replace `$kiroLine.Split('=',2)[1]` with just `$kiroValue`
   (which may be null/empty — that's correct; KIRO_CMD= with empty value is valid). Also
   remove the now-redundant `$kiroLine` variable if it only served the old @" "@ template.

   The $authPrefix and $chatPrefix variables become redundant (Set-EnvLine handles
   comment state). Remove them if they are not referenced elsewhere in Invoke-Init.

REVIEW CHECKLIST (assert by reading, not running):
- [ ] Set-EnvLine matches on both commented and uncommented key forms
- [ ] CHAT_TRACE: -Commented is $true when -not $chatOn (matches bash behavior)
- [ ] AUTH_TOKEN: -Commented is $true when -not $authOn
- [ ] Header stripping skips lines until first blank line when file starts with #
- [ ] generatedHeader has 3 elements (2 comment lines + empty line)
- [ ] $kiroValue (possibly empty string) is written as KIRO_CMD= (not omitted)
- [ ] Set-Content -Encoding UTF8 preserved (no BOM — UTF8 in PS5.1 is BOM-less via
      Set-Content; explicitly use [System.IO.File]::WriteAllLines if BOM appears in testing)
  </action>
  <verify>
    <automated>shellcheck scripts/otto-gw && for variant in default auth chattrace pii-off pii-replace addr kiro; do diff &lt;(grep -E '^#? *[A-Z_]+=' /tmp/base-${variant}.env | sed 's/=.*/=/' | sort) &lt;(grep -E '^#? *[A-Z_]+=' /tmp/new-${variant}.env | sed 's/=.*/=/' | sort) &amp;&amp; echo "${variant}: PASS" || echo "${variant}: FAIL"; done</automated>
  </verify>
  <done>
All seven variant diffs show zero key-set or comment-state regressions. Force-reinit
preserves REINIT_TOKEN_FIXED/REINIT_HASH_FIXED. --regenerate-secrets mints different values.
Two fresh inits produce unique secrets. shellcheck is clean. PS1 refactor is complete and
passes the review checklist assertions.
  </done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| template file → dest .env | Template content copied then mutated; no shell eval |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation |
|-----------|----------|-----------|-------------|------------|
| T-fba-01 | Tampering | set_env_line awk rewrite | mitigate | awk pattern anchors on KEY= to avoid partial-key matches (e.g. PII_REDACTION_MODE must not match PII_REDACTION_ENABLED) — verify anchors in review |
| T-fba-02 | Tampering | template file path ($OTTO_SCRIPT_DIR) | accept | $OTTO_SCRIPT_DIR is resolved at script startup via readlink loop to the script's own directory; not caller-controlled |
| T-fba-03 | Information Disclosure | /tmp/base-*.env files from verification | accept | /tmp files are ephemeral test artifacts containing generated secrets; acceptable for verification workflow |
</threat_model>

<verification>
1. `shellcheck scripts/otto-gw` — zero warnings or errors
2. Key-set diff across all 7 baseline variants shows no regressions (comment states and key presence match)
3. `--force --non-interactive` preserves existing AUTH_TOKEN and PII_HASH_KEY
4. `--force --non-interactive --regenerate-secrets` mints new values
5. Two independent fresh inits produce different AUTH_TOKEN and PII_HASH_KEY
6. `bash scripts/otto-gw init --non-interactive --dest /tmp/missing-tpl-test.env` with the template temporarily renamed produces: "ERROR: init template not found: ..."
7. PS1 review checklist assertions all pass (read-only check; no pwsh runtime)
</verification>

<success_criteria>
- scripts/otto-gw contains no heredoc in init_cmd(); contains set_env_line helper
- scripts/.env.otto-gw.example is the single source of truth for the key list
- All 7 variant diffs are clean (zero key-set or comment-state regressions)
- shellcheck passes clean
- --force preservation and --regenerate-secrets tests pass
- scripts/otto-gw.ps1 contains Set-EnvLine helper; @" "@ heredoc in Invoke-Init is removed
</success_criteria>

<output>
Create `.planning/quick/260531-fba-refactor-otto-gw-init-to-render-the-env-/260531-fba-SUMMARY.md` when done
</output>
