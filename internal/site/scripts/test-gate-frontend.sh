#!/usr/bin/env bash
# Frontend build gate: the checks that stand between a source tree and a bundle that
# `go:embed` can ship.
#
# These live beside internal/site/scripts/test-gate.sh rather than inside it, for one
# reason: that script has a forty-assertion self-test which pins its decision logic by
# running it against a deliberately restricted PATH -- a PATH with no node, no npx and no
# real node_modules. Folding five tool-dependent gates into it would mean either
# stubbing a whole toolchain in those fixtures, which would test the stubs rather than
# the gate, or making the new gates conditional on a tool being present, which is the
# "refusal that reads like success" this repository has already paid for once. A
# separate script keeps the suite gate's logic testable and the build gate's logic
# testable, and `make test-site` runs both.
#
# What each step is for:
#
#   lingui compile     the catalogs are compiled into src/locales/<locale>/<locale>.ts,
#                      which the type check and the bundle both import. `lingui extract
#                      --check` does not exist in @lingui/cli 5.4.1, so the catalog/source
#                      contract is checked by comparing message *sets*
#                      (scripts/check-catalogs.mjs) rather than by diffing files: the
#                      committed catalogs are not byte-stable, and a byte diff fails on
#                      a green tree (260 insertions / 260 deletions, all reordering).
#   tsc -b             the type check the gate did not have. The UI branch adds 2350
#                      lines of TypeScript and nothing compiled them.
#   biome ci           the linter the gate did not have, against a recorded baseline
#                      (scripts/biome-baseline.txt): a finding that is already recorded
#                      does not fail the build, a finding that is new does. CSS is
#                      excluded from the run: Biome 2.2.4's CSS parser does not implement
#                      Tailwind v4's @theme / @utility / @custom-variant, so it reports
#                      22 errors on a file the baseline has failing identically and
#                      `vite build` compiles without complaint.
#   npm audit          the dependency advisories, judged by whether they ship
#                      (scripts/audit-policy.mjs).
#
# The production bundle (`vite build`) is not in here: it takes minutes, and a gate that
# takes minutes is a gate people stop waiting for. It is `make site-bundle`, and it runs
# as its own job.
set -uo pipefail

site_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "${site_dir}/../.." && pwd)"
cd "$site_dir" || exit 1

# Terminal path for "did not run": stderr only, non-zero, and it never prints the word a
# passing gate prints, so a refusal cannot be mistaken at a glance for a run.
not_run() {
	local reason="$1"
	shift
	echo "site build gate: NOT RUN -- ${reason}" >&2
	printf '  remedy: %s\n' "$@" >&2
	exit 1
}

if ! command -v npx >/dev/null 2>&1; then
	not_run "npx is not on PATH" \
		'install node (>= 20.19; vite 7 refuses anything older):  https://nodejs.org'
fi

if [ ! -d "${site_dir}/node_modules" ]; then
	not_run "internal/site/node_modules is missing" \
		'install site dependencies:  bun install --cwd internal/site'
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- lingui compile ---------------------------------------------------------------
# The compiled catalogs are imported by the type check and by the bundle, so they have
# to exist before either runs. `lingui compile` reads the .po files and writes
# src/locales/<locale>/<locale>.ts; it does not touch the .po files, so it leaves the
# committed tree alone.
if ! npx --no-install lingui compile > "$tmp/lingui.log" 2>&1; then
	echo "site build gate: lingui compile failed:" >&2
	tail -n 20 "$tmp/lingui.log" >&2
	exit 1
fi
echo "site build gate: lingui compile -- catalogs compiled"

# --- catalog / source consistency --------------------------------------------------
# `lingui extract --overwrite` rewrites every .po file with the entries in the order the
# source scan reached them. On this tree that produces a 260-insertion / 260-deletion
# diff whose two sides are the same entries in a different order, so a byte diff would
# fail on a tree whose catalogs are perfectly consistent. The catalogs are therefore
# compared by message set: what the source needs, against what the catalog carries.
#
# The committed catalogs are put back afterwards, whatever the outcome, so a run of the
# gate never leaves the tree dirty.
cp -a src/locales "$tmp/locales-committed"
extract_status=0
npx --no-install lingui extract --overwrite > "$tmp/lingui-extract.log" 2>&1 || extract_status=$?
cp -a src/locales "$tmp/locales-extracted"
rm -rf src/locales
cp -a "$tmp/locales-committed" src/locales
if [ "$extract_status" -ne 0 ]; then
	echo "site build gate: lingui extract failed:" >&2
	tail -n 20 "$tmp/lingui-extract.log" >&2
	exit 1
fi
if ! node "${repo_root}/scripts/check-catalogs.mjs" "$tmp/locales-committed" "$tmp/locales-extracted"; then
	exit 1
fi

# --- tsc -b ------------------------------------------------------------------------
# The type check the gate did not have. `tsc -b` is incremental and writes its build
# info under node_modules/.tmp, so a second run in the same workspace is fast and the
# first is a full check.
if ! npx --no-install tsc -b > "$tmp/tsc.log" 2>&1; then
	echo "site build gate: tsc -b reported type errors:" >&2
	tail -n 40 "$tmp/tsc.log" >&2
	exit 1
fi
echo "site build gate: tsc -b -- no type errors"

# --- biome -------------------------------------------------------------------------
# Against a recorded baseline: a finding that is already recorded does not fail the
# build, a finding that is new does. biome ci exits 1 whenever it reports anything, so
# its exit code is not the verdict here; the baseline comparison is.
biome_status=0
npx --no-install biome ci --reporter=json --colors=off > "$tmp/biome.json" 2>/dev/null || biome_status=$?
if [ ! -s "$tmp/biome.json" ]; then
	echo "site build gate: biome produced no report (exit ${biome_status})" >&2
	exit 1
fi
biome_errors="$(node -e "const r=require('${tmp}/biome.json');console.log((r.diagnostics||[]).filter(d=>d.severity==='error').length)")"
echo "site build gate: biome reported ${biome_errors} error(s)"
if ! node "${repo_root}/scripts/lint-baseline.mjs" --tool biome --report "$tmp/biome.json" --baseline "${repo_root}/scripts/biome-baseline.txt"; then
	exit 1
fi

# --- npm audit ---------------------------------------------------------------------
# Judged by whether an advisory ships. The production set must carry nothing above the
# threshold that is not on the recorded allowlist, each entry of which says why.
if ! node "${repo_root}/scripts/audit-policy.mjs" --site-dir "$site_dir" --allowlist "${repo_root}/scripts/npm-audit-allowlist.json"; then
	exit 1
fi

echo "site build gate: green"
