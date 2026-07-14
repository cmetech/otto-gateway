#!/usr/bin/env bash
# scripts/lib/migrate.sh — shared bash helper for the one-time, idempotent
# migration of legacy ~/.otto-gw* config into the new ~/.gw config home.
# Sourced (not executed) by scripts/gw (and any other wrapper) during the
# de-brand/relayout. Mirror MUST stay byte-equivalent in behavior with
# scripts/lib/migrate.ps1.
#
# Surface:
#   gw_migrate_from_otto   moves legacy config into $HOME/.gw; no args.
#
# What moves (config only — mv, never cp, so contents including AUTH_TOKEN
# are preserved exactly and are not left behind in two places):
#   ~/.otto-gw.env                 -> ~/.gw/.env
#   ~/.otto-gw/.env.otto-gw        -> ~/.gw/.env         (fallback legacy path)
#   ~/.otto-gw.overrides.env       -> ~/.gw/overrides.env
#   ~/.otto-gw/tray.json           -> ~/.gw/tray.json
#
# What NEVER moves: the legacy CODE directory ~/.otto-gw/ itself (the
# installed binary, scripts/, etc.) is left untouched — only the specific
# config files named above are ever read out of it. This function never
# deletes or recurses into ~/.otto-gw/.
#
# Idempotent: if $HOME/.gw/.env already exists, this is a no-op (return 0
# immediately) so it is always safe to call unconditionally on every wrapper
# invocation (e.g. from load_config) without re-running the migration or
# clobbering config the operator has already customized post-migration.

# gw_migrate_from_otto: see file header. Always returns 0 (migration is
# best-effort / advisory — a missing legacy install is not an error).
gw_migrate_from_otto() {
	local gw="$HOME/.gw"

	# Already migrated (or a fresh ~/.gw install with no legacy history) —
	# nothing to do. This is the idempotency guard: re-running this function
	# any number of times after the first successful migration is a no-op.
	[ -f "$gw/.env" ] && return 0

	local legacy_env=""
	if   [ -f "$HOME/.otto-gw.env" ];          then legacy_env="$HOME/.otto-gw.env"
	elif [ -f "$HOME/.otto-gw/.env.otto-gw" ]; then legacy_env="$HOME/.otto-gw/.env.otto-gw"
	fi

	# No legacy env found at either known path — nothing to migrate.
	[ -z "$legacy_env" ] && return 0

	mkdir -p "$gw"
	mv "$legacy_env" "$gw/.env" && echo "gw: migrated $legacy_env -> $gw/.env"

	# Best-effort companions: only moved if present. Absence is normal (not
	# every legacy install used overrides.env or has a tray).
	[ -f "$HOME/.otto-gw.overrides.env" ] && mv "$HOME/.otto-gw.overrides.env" "$gw/overrides.env" \
		&& echo "gw: migrated $HOME/.otto-gw.overrides.env -> $gw/overrides.env"
	[ -f "$HOME/.otto-gw/tray.json" ] && mv "$HOME/.otto-gw/tray.json" "$gw/tray.json" \
		&& echo "gw: migrated $HOME/.otto-gw/tray.json -> $gw/tray.json"

	return 0
}
