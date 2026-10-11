#!/usr/bin/env bash
# Frontend gate. Runs the bun suite and fails on any test that is not already
# recorded in known-failures.txt (`<path relative to internal/site>::<bun test name>`),
# so a known gap neither reddens the build nor hides a new one, and the registry is
# kept honest by the stale check below instead of by anyone remembering to prune it.
set -uo pipefail

site_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="${site_dir}/scripts/known-failures.txt"
cd "$site_dir"

output="$(bun test --preload ./tests/bun-preload.ts 2>&1)"
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

[ "$status" -eq 0 ] && exit 0

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

echo "site tests: green (only registered known failures remain)"
