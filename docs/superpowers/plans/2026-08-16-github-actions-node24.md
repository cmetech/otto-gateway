# GitHub Actions Node 24 Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the GitHub Actions used by CI and releases that still run on Node 20 to Node 24-native majors, then prove the CI and release paths by publishing `v3.6.1`.

**Architecture:** Change only the major tags in the two existing workflow files; preserve every trigger, job, input, and command. Validate the workflow sources and repository locally, push the verified commit, then use the existing tag-triggered release pipeline as the end-to-end test.

**Tech Stack:** GitHub Actions YAML, official `actions/*` actions, Ruby YAML parser, Go and Node.js test suites, GitHub CLI.

## Global Constraints

- Use `actions/checkout@v7`, `actions/setup-go@v7`, `actions/setup-node@v7`, `actions/upload-artifact@v7`, `actions/download-artifact@v8`, and `golangci/golangci-lint-action@v9` everywhere the affected actions occur.
- Modify only `.github/workflows/ci.yml` and `.github/workflows/release.yml` for the implementation commit.
- Do not change triggers, permissions, runners, jobs, steps, inputs, artifact names, retention, packaging, checksums, or publishing behavior.
- Keep `actions/download-artifact@v8` digest mismatch handling at its secure default; do not add a suppression.
- Preserve the repository convention of major-version action tags; do not introduce commit-SHA pinning.
- Run all local gates before pushing or tagging.
- Create annotated tag `v3.6.1` only after the verified implementation commit is on `main`.
- Do not delete or retarget `v3.6.1` automatically if the tag-triggered release fails.
- Do not modify or commit the pre-existing untracked `.superpowers/` directory.

---

### Task 1: Upgrade Node 20 actions in CI and release workflows

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Test: workflow-source assertions and YAML parsing commands in this task

**Interfaces:**
- Produces: CI workflows using `checkout@v7`, `setup-go@v7`, `setup-node@v7`, and `golangci/golangci-lint-action@v9`.
- Produces: release workflow using `checkout@v7`, `setup-go@v7`, `upload-artifact@v7`, and `download-artifact@v8`.
- Consumes: the existing workflow job topology and all existing action inputs unchanged.

- [ ] **Step 1: Run the failing legacy-reference assertion**

```bash
! rg -n 'actions/setup-node@v4|golangci/golangci-lint-action@v7' \
  .github/workflows/ci.yml
```

Expected: FAIL because CI contains exactly the three remaining legacy
references: one `actions/setup-node@v4` and two
`golangci/golangci-lint-action@v7` references.

- [ ] **Step 2: Replace only the affected action major tags**

In both workflow files, make these exact substitutions wherever they occur:

```text
actions/checkout@v4          -> actions/checkout@v7
actions/setup-go@v5          -> actions/setup-go@v7
actions/setup-node@v4        -> actions/setup-node@v7
actions/upload-artifact@v4   -> actions/upload-artifact@v7
actions/download-artifact@v4 -> actions/download-artifact@v8
golangci/golangci-lint-action@v7 -> golangci/golangci-lint-action@v9
```

Do not reformat or otherwise edit surrounding workflow content.

- [ ] **Step 3: Re-run the legacy-reference assertion**

```bash
! rg -n 'actions/(checkout@v4|setup-go@v5|setup-node@v4|upload-artifact@v4|download-artifact@v4)|golangci/golangci-lint-action@v7' \
  .github/workflows/ci.yml .github/workflows/release.yml
```

Expected: PASS with no output.

- [ ] **Step 4: Assert the exact target reference counts**

```bash
test "$(rg -o 'actions/checkout@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 9
test "$(rg -o 'actions/setup-go@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 8
test "$(rg -o 'actions/setup-node@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 1
test "$(rg -o 'actions/upload-artifact@v7' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
test "$(rg -o 'actions/download-artifact@v8' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
test "$(rg -o 'golangci/golangci-lint-action@v9' .github/workflows/ci.yml .github/workflows/release.yml | wc -l | tr -d ' ')" = 2
```

Expected: all six commands exit 0.

- [ ] **Step 5: Validate YAML syntax**

```bash
ruby -e 'require "yaml"; ARGV.each { |path| YAML.parse_file(path) }' \
  .github/workflows/ci.yml .github/workflows/release.yml
```

Expected: exit 0 with no output.

- [ ] **Step 6: Verify the target action metadata declares Node 24**

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

Expected: exit 0 for all six target actions.

- [ ] **Step 7: Assert the workflow topology and inputs are unchanged**

```bash
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

Expected: exit 0. This reconstructs each workflow from the implementation
base using only the six approved action-major substitutions, proving that all
jobs, steps, inputs, triggers, permissions, runners, and commands are intact.

- [ ] **Step 8: Inspect the scoped diff**

```bash
git diff --check
git diff -- .github/workflows/ci.yml .github/workflows/release.yml
```

Expected: no whitespace errors and only the exact action-tag substitutions.

- [ ] **Step 9: Commit the workflow upgrade**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "ci: upgrade official actions to Node 24"
```

---

### Task 2: Verify the isolated implementation branch

**Files:**
- Verify only; no source files should change.

**Interfaces:**
- Consumes: Task 1's workflow commit on the isolated feature branch.
- Produces: current-session verification evidence for final branch review.

- [ ] **Step 1: Run complete local verification**

```bash
node --test internal/admin/admin_js_test.js
go test ./...
go build ./...
git diff --check
git status --short
```

Expected: 29 JavaScript tests pass, all Go packages pass, the build exits 0,
there are no whitespace errors, and only the pre-existing untracked
`.superpowers/` directory appears in status.

- [ ] **Step 2: Inspect branch identity and the complete scoped diff**

```bash
git branch --show-current
git log --oneline 480e14f..HEAD
git diff --check 480e14f..HEAD
git diff --stat 480e14f..HEAD
git diff 480e14f..HEAD -- .github/workflows/ci.yml .github/workflows/release.yml
```

Expected: the branch is `ci/node24-actions`; the only implementation changes
are the approved action-major substitutions in the two workflow files.

- [ ] **Step 3: Record verification without an empty commit**

Report every command and result from Steps 1-2. If verification does not change
tracked files, do not create an empty commit.

---

## Post-review integration and v3.6.1 release

Run this section only after Tasks 1-2 and the broad whole-branch review are
clean. Use the finishing-development-branch workflow to merge
`ci/node24-actions` into `main` and re-run `go test ./...` on the merged tree
before continuing.

- [ ] **Step 1: Push the verified main branch**

```bash
git branch --show-current
git push origin main
git rev-parse HEAD
git ls-remote --heads origin main
```

Expected: the current branch is `main` and the local/remote main SHAs match.

- [ ] **Step 2: Verify the exact pushed CI run before creating the tag**

```bash
pushed_main_sha=$(git rev-parse HEAD)
ci_run_id=""
for attempt in {1..20}; do
  ci_run_id=$(gh run list -R cmetech/otto-gateway --workflow ci.yml \
    --commit "$pushed_main_sha" --limit 1 --json databaseId,headSha \
    --jq '.[] | select(.headSha == "'"$pushed_main_sha"'") | .databaseId')
  test -n "$ci_run_id" && break
  sleep 2
done
test -n "$ci_run_id"
gh run watch "$ci_run_id" -R cmetech/otto-gateway --exit-status
ci_job_ids=$(gh api "repos/cmetech/otto-gateway/actions/runs/$ci_run_id/jobs" \
  --paginate --jq '.jobs[].id')
ci_annotation_text=""
for job_id in $ci_job_ids; do
  ci_annotation_text="$ci_annotation_text$(gh api \
    "repos/cmetech/otto-gateway/check-runs/$job_id/annotations" --paginate \
    --jq '.[].message')"
done
test -z "$(printf '%s' "$ci_annotation_text" | rg 'Node.js 20 is deprecated' || true)"
```

Expected: the CI run whose `headSha` exactly matches the pushed `main` SHA
completes successfully, and none of its job annotations contains the Node.js
20 deprecation text.

- [ ] **Step 3: Guard and create the annotated release tag**

```bash
test -z "$(git tag -l v3.6.1)"
test -z "$(git ls-remote --tags origin refs/tags/v3.6.1)"
git tag -a v3.6.1 -m "Release v3.6.1"
git push origin v3.6.1
git show -s --format='%H %s' v3.6.1
```

Expected: the tag points to the same verified commit pushed in Step 1.

- [ ] **Step 4: Discover and monitor the tag-triggered release run**

```bash
release_run_id=""
for attempt in {1..20}; do
  release_run_id=$(gh run list -R cmetech/otto-gateway --workflow release.yml \
    --branch v3.6.1 --limit 1 --json databaseId --jq '.[0].databaseId')
  test -n "$release_run_id" && break
  sleep 2
done
test -n "$release_run_id"
gh run watch "$release_run_id" -R cmetech/otto-gateway --exit-status
```

Expected: `build-macos`, `build-linux`, and `publish` all complete successfully.

- [ ] **Step 5: Confirm the published release and exact assets**

```bash
gh release view v3.6.1 -R cmetech/otto-gateway \
  --json isDraft,isPrerelease,url,assets \
  --jq '{draft:.isDraft,prerelease:.isPrerelease,url,assets:[.assets[].name]|sort}'
```

Expected: `draft` and `prerelease` are false, and the sorted assets are exactly:

```text
SHA256SUMS-v3.6.1.txt
otto_gateway-darwin-amd64-v3.6.1.tar.gz
otto_gateway-darwin-arm64-v3.6.1.tar.gz
otto_gateway-linux-amd64-v3.6.1.tar.gz
otto_gateway-windows-amd64-v3.6.1.zip
```

- [ ] **Step 6: Prove the Node 20 annotations are gone from the release run**

```bash
release_run_id=$(gh run list -R cmetech/otto-gateway --workflow release.yml \
  --branch v3.6.1 --limit 1 --json databaseId --jq '.[0].databaseId')
job_ids=$(gh api "repos/cmetech/otto-gateway/actions/runs/$release_run_id/jobs" \
  --paginate --jq '.jobs[].id')
annotation_text=""
for job_id in $job_ids; do
  annotation_text="$annotation_text$(gh api \
    "repos/cmetech/otto-gateway/check-runs/$job_id/annotations" --paginate \
    --jq '.[].message')"
done
test -z "$(printf '%s' "$annotation_text" | rg 'Node.js 20 is deprecated' || true)"
```

Expected: exit 0; none of the three release jobs emits the Node.js 20
deprecation annotation.

- [ ] **Step 7: Report release evidence**

Report the implementation commit, pushed main SHA, CI workflow URL and
conclusion, CI annotation result, tag SHA/target, release workflow URL and
conclusion, release URL, five asset names, and release annotation result. Do
not create an empty verification commit.
