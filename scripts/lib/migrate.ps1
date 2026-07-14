# scripts/lib/migrate.ps1 — shared pwsh helper for the one-time, idempotent
# migration of legacy ~/.otto-gw* config into the new ~/.gw config home.
# Dot-sourced (not executed) by scripts/gw.ps1 (and any other wrapper) during
# the de-brand/relayout. Behavior MUST mirror scripts/lib/migrate.sh.
#
# Surface:
#   Invoke-GwMigration   moves legacy config into $env:USERPROFILE\.gw; no args.
#
# What moves (config only — Move-Item, never Copy-Item, so contents
# including AUTH_TOKEN are preserved exactly and are not left behind in two
# places):
#   $env:USERPROFILE\.otto-gw.env             -> $env:USERPROFILE\.gw\.env
#   $env:USERPROFILE\.otto-gw\.env.otto-gw    -> $env:USERPROFILE\.gw\.env   (fallback legacy path)
#   $env:USERPROFILE\.otto-gw.overrides.env   -> $env:USERPROFILE\.gw\overrides.env
#   $env:USERPROFILE\.otto-gw\tray.json       -> $env:USERPROFILE\.gw\tray.json
#
# What NEVER moves: the legacy CODE directory $env:USERPROFILE\.otto-gw\
# itself (the installed binary, scripts\, etc.) is left untouched — only the
# specific config files named above are ever read out of it. This function
# never deletes or recurses into $env:USERPROFILE\.otto-gw\.
#
# Idempotent: if $env:USERPROFILE\.gw\.env already exists, this is a no-op
# (return immediately) so it is always safe to call unconditionally on every
# wrapper invocation without re-running the migration or clobbering config
# the operator has already customized post-migration.

# Invoke-GwMigration: see file header. Never throws — a missing legacy
# install is not an error (migration is best-effort / advisory).
function Invoke-GwMigration {
    [CmdletBinding()]
    param()

    $gw = Join-Path $env:USERPROFILE '.gw'

    # Already migrated (or a fresh .gw install with no legacy history) —
    # nothing to do. This is the idempotency guard: re-running this function
    # any number of times after the first successful migration is a no-op.
    if (Test-Path (Join-Path $gw '.env')) { return }

    $legacy = Join-Path $env:USERPROFILE '.otto-gw.env'
    $legacyFallback = Join-Path (Join-Path $env:USERPROFILE '.otto-gw') '.env.otto-gw'
    if (-not (Test-Path $legacy)) {
        if (Test-Path $legacyFallback) {
            $legacy = $legacyFallback
        } else {
            # No legacy env found at either known path — nothing to migrate.
            return
        }
    }

    New-Item -ItemType Directory -Force -Path $gw | Out-Null
    Move-Item -Path $legacy -Destination (Join-Path $gw '.env')
    Write-Host "gw: migrated $legacy -> $(Join-Path $gw '.env')"

    # Best-effort companions: only moved if present. Absence is normal (not
    # every legacy install used overrides.env or has a tray).
    $ov = Join-Path $env:USERPROFILE '.otto-gw.overrides.env'
    if (Test-Path $ov) {
        $ovDest = Join-Path $gw 'overrides.env'
        Move-Item -Path $ov -Destination $ovDest
        Write-Host "gw: migrated $ov -> $ovDest"
    }

    $tray = Join-Path (Join-Path $env:USERPROFILE '.otto-gw') 'tray.json'
    if (Test-Path $tray) {
        $trayDest = Join-Path $gw 'tray.json'
        Move-Item -Path $tray -Destination $trayDest
        Write-Host "gw: migrated $tray -> $trayDest"
    }
}
