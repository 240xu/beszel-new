#!/usr/bin/env bash
# Frontend gate. Runs the bun suite and fails on any test not already recorded in
# known-failures.txt (`<path relative to internal/site>::<bun test name>`), so a known
# gap neither reddens the build nor hides a new one; the stale check at the bottom keeps
# that registry honest. When the suite cannot run at all this exits non-zero: it used to
# print SKIP and exit 0 from the Makefile, which was worse than no gate at all, because
# bun lives in ~/.bun/bin -- off PATH for cron, for CI, for `ssh host cmd` -- so 204
# frontend tests went unrun on a build that still reported success, and every conclusion
# drawn from that build rested on tests that had never executed.
set -uo pipefail

site_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="${site_dir}/scripts/known-failures.txt"
cd "$site_dir"

# Named for what it grants, not for the word "skip", so a log reading it shows a decision.
valve='BESZEL_ALLOW_SITE_TESTS_NOT_RUN'

# PATH alone left this gate blind on exactly the machines that did have bun, so the
# install location bun actually uses is probed as a source in its own right.
bun_bin=''
if command -v bun >/dev/null 2>&1; then
	bun_bin="$(command -v bun)"
elif [ -x "${HOME:-}/.bun/bin/bun" ]; then
	bun_bin="${HOME}/.bun/bin/bun"
fi

# Terminal path for "did not run": stderr only, and it never prints the word a passing
# gate prints, so a refused gate cannot be mistaken at a glance for one that passed.
not_run() {
	local reason="$1"
	shift
	echo "site tests: NOT RUN -- ${reason}" >&2
	printf '  remedy: %s\n' "$@" >&2
	if [ "${!valve:-}" != '1' ]; then
		echo "  or re-run deliberately with ${valve}=1 to accept an unrun suite" >&2
		exit 1
	fi
	echo "  WARNING: ${valve}=1 is set, so the frontend suite was deliberately not run." >&2
	echo "  WARNING: this run produced no evidence about the frontend at all." >&2
	exit 0
}

if [ -z "$bun_bin" ]; then
	not_run "bun is on neither PATH nor ${HOME:-}/.bun/bin/bun" \
		'install bun:  curl -fsSL https://bun.sh/install | bash'
fi

if [ ! -d "${site_dir}/node_modules" ]; then
	not_run "internal/site/node_modules is missing" \
		'install site dependencies:  bun install --cwd internal/site'
fi

output="$("$bun_bin" test --preload ./tests/bun-preload.ts 2>&1)"
status=$?

failing="$(printf '%s\n' "$output" | awk '
  /^[0-9]+ tests? failed:/ { exit }
  /^(tests|src)\// { file = $0; sub(/:$/, "", file); next }
  /^\(fail\)/ {
    name = $0
    sub(/^\(fail\) /, "", name)
    sub(/ \[[0-9.]+m?s\]$/, "", name)
    print file "::" name
  }
')"

printf '%s\n' "$output" | tail -n 5

# Counts come off bun's own summary, so a passing gate reports what the runner observed.
passed_count="$(printf '%s\n' "$output" | awk '/^[[:space:]]*[0-9]+ pass$/ { gsub(/[^0-9]/, ""); print; exit }')"
[ -n "$passed_count" ] || passed_count='unknown'
registered_count="$(printf '%s' "$failing" | awk 'NF { n++ } END { print n + 0 }')"

if [ "$status" -eq 0 ]; then
	echo "site tests: green (${passed_count} pass, ${registered_count} registered failures)"
	exit 0
fi

# A non-zero exit with nothing parseable means the runner itself broke. That must
# never be mistaken for "only the registered failures remain".
if [ -z "$failing" ]; then
	printf '%s\n' "$output"
	echo "site tests: runner exited ${status} without a parseable failure" >&2
	exit 1
fi

unknown="$(printf '%s\n' "$failing" | grep -vxF -f "$registry" || true)"
if [ -n "$unknown" ]; then
	echo "site tests: NEW failures absent from scripts/known-failures.txt:" >&2
	printf '  %s\n' "$unknown" >&2
	exit 1
fi

while IFS= read -r entry; do
	case "$entry" in ''|'#'*) continue ;; esac
	printf '%s\n' "$failing" | grep -qxF -- "$entry" ||
		echo "site tests: registered failure no longer fails, drop it: $entry" >&2
done < "$registry"

echo "site tests: green (${passed_count} pass, ${registered_count} registered failures)"