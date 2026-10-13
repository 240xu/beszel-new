#!/usr/bin/env bash
# Frontend gate. Runs the bun suite and fails on any test not already recorded in
# known-failures.txt (`<site-relative path>::<bun test name>`), so a known gap neither
# reddens the build nor hides a new one, and the stale sweep below keeps that registry
# from rotting unnoticed. When the suite cannot run at all this exits non-zero: bun lives
# in ~/.bun/bin -- off PATH for cron, for CI, for `ssh host cmd` -- so 204 frontend tests
# once went unrun on a build that still reported success.
#
# A BESZEL_ALLOW_SITE_TESTS_NOT_RUN valve used to turn that refusal back into exit 0. It is
# deleted rather than tightened: installing bun costs 5.78s and 505MB against a 0.22s suite,
# so the valve bought nothing measurable, while an exit code of 0 left "the gate ran" and
# "the gate declined to run" indistinguishable to anything reading CI. There is no supported
# way to leave this suite unrun and still get zero.
set -uo pipefail

site_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="${site_dir}/scripts/known-failures.txt"
cd "$site_dir"

# PATH alone left this gate blind on exactly the machines that did have bun, so the
# install location bun actually uses is probed as a source in its own right.
bun_bin=''
if command -v bun >/dev/null 2>&1; then
	bun_bin="$(command -v bun)"
elif [ -x "${HOME:-}/.bun/bin/bun" ]; then
	bun_bin="${HOME}/.bun/bin/bun"
fi

# Terminal path for "did not run": stderr only, non-zero, and it never prints the word a
# passing gate prints, so a refusal cannot be mistaken at a glance for a run. No
# environment variable re-opens this -- a knob that reports success without evidence is the
# defect, not the configuration.
not_run() {
	local reason="$1"
	shift
	echo "site tests: NOT RUN -- ${reason}" >&2
	printf '  remedy: %s\n' "$@" >&2
	exit 1
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

# A non-zero exit with nothing parseable means the runner itself broke, which must never be
# mistaken for "only the registered failures remain".
if [ "$status" -ne 0 ] && [ -z "$failing" ]; then
	printf '%s\n' "$output"
	echo "site tests: runner exited ${status} without a parseable failure" >&2
	exit 1
fi

if [ "$status" -ne 0 ]; then
	unknown="$(printf '%s\n' "$failing" | grep -vxF -f "$registry" || true)"
	if [ -n "$unknown" ]; then
		echo "site tests: NEW failures absent from scripts/known-failures.txt:" >&2
		printf '  %s\n' "$unknown" >&2
		exit 1
	fi
fi

# The sweep runs even when the suite is fully green, because green is the only state a
# repaired test can produce -- hiding it inside the failure branch is what let the registry
# rot in silence. A warning that still exits 0 is a warning no automation ever reads.
stale=''
while IFS= read -r entry; do
	case "$entry" in ''|'#'*) continue ;; esac
	printf '%s\n' "$failing" | grep -qxF -- "$entry" || stale="${stale}${entry}"$'\n'
done < "$registry"
if [ -n "$stale" ]; then
	echo "site tests: registered failures that no longer fail, drop them from scripts/known-failures.txt:" >&2
	printf '  %s' "$stale" >&2
	exit 1
fi

echo "site tests: green (${passed_count} pass, ${registered_count} registered failures)"