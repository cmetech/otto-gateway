#!/usr/bin/env bash
# Acceptance contract for observability-specific release gates. It asks make
# for the real `ci` execution plan and parses the workflow structure so renamed
# steps are harmless while missing commands fail deterministically.
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"

ci_plan="$(make -n -C "$repo_root" ci)"
if ! grep -Fq 'bash tests/scripts/test-metrics-remote-write-defaults.sh' <<<"$ci_plan"; then
    echo 'FAIL: make ci does not execute the metrics remote-write defaults acceptance test' >&2
    exit 1
fi

ruby - "$repo_root/.github/workflows/ci.yml" <<'RUBY'
require "yaml"

path = ARGV.fetch(0)
workflow = YAML.safe_load(File.read(path), aliases: true)
jobs = workflow.fetch("jobs")

commands = lambda do |job|
  jobs.fetch(job).fetch("steps").map { |step| step["run"] }.compact.join("\n")
end

linux = commands.call("lint-test-arch")
unless linux.include?("bash tests/scripts/test-metrics-remote-write-defaults.sh")
  abort "FAIL: Linux CI does not run metrics remote-write defaults acceptance"
end

darwin = commands.call("lint-darwin")
unless darwin.match?(/go test -race \.\/cmd\/otto-tray/) &&
       darwin.match?(/RemoteWrite|MetricsRW/) &&
       darwin.match?(/Preference/)
  abort "FAIL: macOS CI does not run targeted race-enabled remote-write/preference tests"
end

windows = commands.call("support-powershell-windows")
unless windows.match?(/go test \.\/cmd\/otto-tray/) &&
       windows.match?(/RemoteWrite|MetricsRW/) &&
       windows.match?(/WrapperCancellation/)
  abort "FAIL: Windows CI does not run feasible native tray remote-write/cancellation tests"
end

publish = commands.call("publish-dry-run")
unless publish.include?("bash tests/scripts/test-windows-package-support.sh")
  abort "FAIL: release-package CI does not execute the packaged PowerShell support smoke"
end
RUBY

echo 'passed: observability acceptance gates are wired into Makefile and CI'
