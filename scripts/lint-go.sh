#!/usr/bin/env bash
# Go lint gate: staticcheck and golangci-lint, both against a recorded baseline.
#
# The baseline is the same idea as internal/site/scripts/known-failures.txt: the
# findings that exist on the tree today are recorded, a finding that is new fails the
# build, and the recorded list is checked in so the debt is visible and countable.
#
#   staticcheck  14 findings   (scripts/staticcheck-baseline.txt)
#   golangci-lint  237 findings (scripts/golangci-baseline.txt)
#
# Toolchain note, and it is not a detail: staticcheck 2025.1.1 (v0.6.1) cannot read
# go1.27 export data at all. It aborts with "cannot decode \"cmp\", export data version
# 4 is greater than maximum supported version 2" before it reaches a single project
# file. The version pinned below (v0.8.2 = 2026.2.2) is the first one that can, and it
# must be pinned rather than floated: a staticcheck that cannot read the module
# reports nothing, which reads exactly like a clean run.
#
# Both tools must be on PATH. This script installs nothing: a missing tool is a
# missing tool, and a gate that silently installs a different version of its own
# linter is not a gate.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root" || exit 1

# The repository's own test configuration. `go vet ./...` without these tags does not
# compile: 64 test files carry `//go:build testing` and internal/tests/hub.go does
# too, and two untagged test files reference helpers that live behind that tag.
TAGS="testing no_ui"

failures=0

not_run() {
	local reason="$1"
	shift
	echo "go lint: NOT RUN -- ${reason}" >&2
	printf '  remedy: %s\n' "$@" >&2
	exit 1
}

if ! command -v staticcheck >/dev/null 2>&1; then
	not_run "staticcheck is not on PATH" \
		'go install honnef.co/go/tools/cmd/staticcheck@v0.8.2   # 2026.2.2; v0.6.1 cannot read go1.27 export data'
fi
if ! command -v golangci-lint >/dev/null 2>&1; then
	not_run "golangci-lint is not on PATH" \
		'go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0'
fi

staticcheck -version
golangci-lint --version

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- staticcheck -----------------------------------------------------------------------
# staticcheck has no JSON output, so its plain output is the report. The exit code is
# the number of findings, which is why it is not used as the gate's verdict: the
# baseline comparison decides.
staticcheck -tags "${TAGS}" ./... > "$tmp/staticcheck.txt" 2>&1
staticcheck_status=$?
echo "go lint: staticcheck reported $(wc -l < "$tmp/staticcheck.txt" | tr -d ' ') finding(s) (exit ${staticcheck_status})"
if ! node scripts/lint-baseline.mjs --tool staticcheck --report "$tmp/staticcheck.txt" --baseline scripts/staticcheck-baseline.txt; then
	failures=$((failures + 1))
fi

# --- golangci-lint ---------------------------------------------------------------------
golangci-lint run --config scripts/golangci-lint.yml --output.json.path="$tmp/golangci.json" ./... > "$tmp/golangci.log" 2>&1
golangci_status=$?
if [ ! -s "$tmp/golangci.json" ]; then
	echo "go lint: golangci-lint produced no report (exit ${golangci_status}):" >&2
	tail -n 20 "$tmp/golangci.log" >&2
	exit 1
fi
golangci_count=$(node -e "const r=require('${tmp}/golangci.json');console.log((r.Issues||[]).length)")
echo "go lint: golangci-lint reported ${golangci_count} finding(s) (exit ${golangci_status})"
if ! node scripts/lint-baseline.mjs --tool golangci --report "$tmp/golangci.json" --baseline scripts/golangci-baseline.txt; then
	failures=$((failures + 1))
fi

if [ "$failures" -ne 0 ]; then
	echo "go lint: ${failures} linter(s) reported a finding outside the recorded baseline" >&2
	exit 1
fi

echo "go lint: green -- no finding outside the recorded baselines"
