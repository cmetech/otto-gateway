# GitHub Actions Node 24 Upgrade Design

**Date:** 2026-08-16

## Goal

Remove the GitHub Actions Node.js 20 deprecation annotations from ordinary CI
and tagged releases, then prove the updated release pipeline by publishing
`v3.6.1`.

## Scope

Update every use of the six affected action families in
`.github/workflows/ci.yml` and `.github/workflows/release.yml`:

| Action | Current | Target |
|---|---:|---:|
| `actions/checkout` | `v4` | `v7` |
| `actions/setup-go` | `v5` | `v7` |
| `actions/setup-node` | `v4` | `v7` |
| `actions/upload-artifact` | `v4` | `v7` |
| `actions/download-artifact` | `v4` | `v8` |
| `golangci/golangci-lint-action` | `v7` | `v9` |

The target majors are the current stable releases selected for this change on
2026-08-16. The upstream action metadata for all six target references declares
the Node 24 runtime.

No job topology, triggers, permissions, runner labels, inputs, artifact names,
retention policy, packaging commands, or publishing commands will change.

## Behavior and Compatibility

The workflows continue to use major-version tags, matching the repository's
existing dependency convention. Pinning immutable commit SHAs is a separate
supply-chain policy decision and is outside this change.

`actions/download-artifact@v8` fails on artifact digest mismatches by default.
The release pipeline should retain that secure default. The existing release
jobs download artifacts produced in the same workflow run, so no compatibility
override is expected or desired.

All runners are GitHub-hosted (`ubuntu-latest`, `macos-latest`, and
`windows-latest`), so no self-hosted runner upgrade is required for Node 24
action execution.

## Verification

Before push:

1. Confirm no affected legacy references remain in either workflow and that
   every target action reference has its expected count.
2. Validate both workflow files parse as YAML.
3. Run `node --test internal/admin/admin_js_test.js`.
4. Run `go test ./...`.
5. Run `go build ./...`.
6. Run `git diff --check` and inspect the scoped workflow diff.

After the verified commit is pushed to `main`:

1. Identify the CI run for the exact pushed `main` commit, wait for it to
   complete successfully, and inspect every CI job annotation for the absence
   of Node.js 20 deprecation warnings.
2. Create annotated tag `v3.6.1` at that verified `main` commit with message
   `Release v3.6.1`.
3. Push the tag to the configured remotes.
4. Monitor the tag-triggered `release` workflow through completion.
5. Confirm the macOS build, Linux/Windows build, and publish jobs succeed.
6. Confirm the GitHub Release is neither draft nor prerelease and contains the
   four platform archives plus `SHA256SUMS-v3.6.1.txt`.
7. Inspect all three release-job annotations and confirm there are no Node.js 20
   deprecation warnings.

## Failure Handling

Do not create the release tag if local validation fails. If the tag-triggered
workflow fails, preserve the tag and published state, inspect the failed job,
and fix forward only with explicit user direction; do not delete or retarget a
published release tag automatically.

## Out of Scope

- Changing workflow triggers, permissions, job structure, or packaging logic.
- Updating third-party actions other than the in-scope
  `golangci/golangci-lint-action` upgrade.
- Pinning action dependencies to commit SHAs.
- Suppressing digest verification or deprecation warnings.
