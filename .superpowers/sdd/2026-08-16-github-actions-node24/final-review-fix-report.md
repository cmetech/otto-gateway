# Final-review fix report — GitHub Actions Node 24

**Fix base:** `cd885a180bb4f1cb7506fd7ebba477f801c31466`

## Changes

- Replaced the one remaining `actions/setup-node@v4` use with
  `actions/setup-node@v7` in ordinary CI.
- Replaced both remaining `golangci/golangci-lint-action@v7` uses with
  `golangci/golangci-lint-action@v9` in ordinary CI.
- Updated the design and implementation plan for all six Node 24-native action
  families, the complete hosted-runner inventory, exact CI-before-tag release
  sequencing, and annotation checks for both CI and release runs.

## RED evidence

Command:

```bash
! rg -n 'actions/setup-node@v4|golangci/golangci-lint-action@v7' \
  .github/workflows/ci.yml
```

Relevant output before the substitutions:

```text
157:        uses: actions/setup-node@v4
192:        uses: golangci/golangci-lint-action@v7
271:        uses: golangci/golangci-lint-action@v7
RED assertion exit=1 (legacy references intentionally detected)
```

## GREEN workflow validation

Commands:

```bash
! rg -n 'actions/(checkout@v4|setup-go@v5|setup-node@v4|upload-artifact@v4|download-artifact@v4)|golangci/golangci-lint-action@v7' \
  .github/workflows/ci.yml .github/workflows/release.yml
test "$(rg -o 'actions/checkout@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 9
test "$(rg -o 'actions/setup-go@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 8
test "$(rg -o 'actions/setup-node@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 1
test "$(rg -o 'actions/upload-artifact@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
test "$(rg -o 'actions/download-artifact@v8' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
test "$(rg -o 'golangci/golangci-lint-action@v9' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
ruby -e 'require "yaml"; ARGV.each { |path| YAML.parse_file(path) }' \
  .github/workflows/ci.yml .github/workflows/release.yml
node24_base=480e14f
for workflow in ci release; do
  diff -u \
    <(git show "$node24_base:.github/workflows/$workflow.yml" | sed -E \
      -e 's#actions/checkout@v4#actions/checkout@v7#g' \
      -e 's#actions/setup-go@v5#actions/setup-go@v7#g' \
      -e 's#actions/setup-node@v4#actions/setup-node@v7#g' \
      -e 's#actions/upload-artifact@v4#actions/upload-artifact@v7#g' \
      -e 's#actions/download-artifact@v4#actions/download-artifact@v8#g' \
      -e 's#golangci/golangci-lint-action@v7#golangci/golangci-lint-action@v9#g') \
    ".github/workflows/$workflow.yml"
done
```

Relevant output:

```text
legacy-reference assertion exit=0
all six target reference counts passed
YAML parse passed
unchanged-topology assertion passed
```

## Upstream action metadata

Command:

```bash
for action_ref in \
  actions/checkout:v7 \
  actions/setup-go:v7 \
  actions/setup-node:v7 \
  actions/upload-artifact:v7 \
  actions/download-artifact:v8 \
  golangci/golangci-lint-action:v9; do
  action_name=${action_ref%%:*}
  action_tag=${action_ref##*:}
  gh api "repos/$action_name/contents/action.yml?ref=$action_tag" --jq .content \
    | base64 --decode \
    | rg -q "using: [\"']?node24[\"']?"
done
```

Relevant output from the equivalent named check:

```text
actions/checkout@v7: node24
actions/setup-go@v7: node24
actions/setup-node@v7: node24
actions/upload-artifact@v7: node24
actions/download-artifact@v8: node24
golangci/golangci-lint-action@v9: node24
```

## Full local verification

Command:

```bash
node --test internal/admin/admin_js_test.js && go test ./... && go build ./... && git diff --check
```

Relevant output:

```text
1..29
# tests 29
# pass 29
# fail 0
ok   otto-gateway/cmd/otto-gateway (cached)
ok   otto-gateway/cmd/otto-tray (cached)
... all tested Go packages passed ...
```

`go build ./...` and `git diff --check` exited 0 with no output. The workflow
source topology assertion confirms the workflow files differ from `480e14f`
only by the six approved action-major substitutions.

## Self-review

- CI changes are limited to the three approved action-major substitutions; the
  `node-version`, golangci `version`, and golangci `args` inputs are unchanged.
- The release workflow is unchanged, retaining the exact `v3.6.1` assets and
  release behavior.
- Documentation now requires a successful CI run matching the pushed `main`
  SHA and a clean CI annotation check before tag creation, followed by the
  existing release wait and release annotation check.
