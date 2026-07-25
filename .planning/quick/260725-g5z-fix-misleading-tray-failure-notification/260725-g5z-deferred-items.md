# Deferred Items — quick-260725-g5z

Out-of-scope findings observed while running the CI trust gates. Neither is
caused by this task's changes; both live in files this branch never touched.
Confirmed pre-existing via
`golangci-lint run --new-from-rev=695462c16f0f19678618e77717bb0d97f6ae4f72 ./cmd/otto-tray/...`
which reports `0 issues`.

| Finding | File | Linter | Note |
|---------|------|--------|------|
| `runOpenDesktopFolder - goos always receives "darwin"` | `cmd/otto-tray/openfolder.go:120` | unparam | Surfaces only when linting with `GOOS=darwin`. |
| `error returned from external package is unwrapped: (*exec.Cmd).Run()` | `cmd/otto-tray/desktop_darwin.go:28` | wrapcheck | File is `darwin`-tagged, so CI's linux lint job never compiles it. |

**Why CI is green on main despite these:** `.github/workflows/ci.yml` runs
`golangci-lint run ./...` on `ubuntu-latest`. `desktop_darwin.go` is excluded by
build tag there, and `runOpenDesktopFolder`'s `goos` parameter is not constant
across the linux-visible call graph. Running the linter locally on a macOS box
compiles the darwin arm and surfaces both.

**Recommendation:** either fix both (wrap the `exec.Cmd.Run()` error; drop or
justify the `goos` parameter) or add a `GOOS=darwin` lint job so the darwin arm
stops drifting. Track separately — fixing them inside this quick task would have
widened its diff beyond the notification fix.
