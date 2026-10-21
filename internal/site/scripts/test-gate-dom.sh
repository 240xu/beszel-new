#!/usr/bin/env bash
# The DOM component suite.
#
# The frontend suite that exists next to this one runs under bun, and bun cannot apply
# the Lingui macro transform: the macro is compile-time, and `@lingui/core/macro`
# resolves to a babel plugin whose every export throws "the macro you imported ...
# is being executed outside the context of compilation" when it is read at runtime.
# That is why the fan panel's guard test greps the *source text* of the component
# instead of rendering it -- a grep proves the file contains a sentence, never that
# the sentence reaches an operator.
#
# This suite runs under vitest, which uses the same Vite pipeline `vite build` uses,
# and `vite.config.ts` already wires `@lingui/swc-plugin` into
# `@vitejs/plugin-react-swc`. Reusing that config is the whole trick: the component
# under test is transformed exactly as it is for the production bundle, so the macro
# compiles away and the component really renders.
#
# It is a separate script and a separate runner because the two suites answer
# different questions: this one asks what the operator sees, the other asks what the
# pure logic computes. Neither replaces the other, and `make test-site` runs both.
#
# vitest must be installed. A missing toolchain is a refusal, not a skip: the whole
# reason this file exists is that a suite which declines to run and reports success
# is the failure mode this repository has already paid for once.
set -uo pipefail

site_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$site_dir" || exit 1

not_run() {
	local reason="$1"
	shift
	echo "site dom tests: NOT RUN -- ${reason}" >&2
	printf '  remedy: %s\n' "$@" >&2
	exit 1
}

if [ ! -d "${site_dir}/node_modules" ]; then
	not_run "internal/site/node_modules is missing" \
		'install site dependencies:  bun install --cwd internal/site'
fi

if [ ! -x "${site_dir}/node_modules/.bin/vitest" ]; then
	not_run "vitest is not installed" \
		'install the DOM suite dependencies:  npm install --prefix internal/site'
fi

# The compiled catalogs are imported by the components under test (src/lib/i18n.ts
# imports the English catalog), so they have to exist before the suite runs. The
# Makefile compiles them first; this is the belt to that braces, because a suite
# that fails to import is not the same claim as a suite that ran.
if [ ! -f "${site_dir}/src/locales/en/en.ts" ]; then
	npx --no-install lingui compile >/dev/null 2>&1 || {
		not_run "the catalogs are not compiled and could not be compiled" \
			'cd internal/site && npx --no-install lingui compile'
	}
fi

"${site_dir}/node_modules/.bin/vitest" run --config "${site_dir}/vitest.config.ts"
status=$?

if [ "$status" -ne 0 ]; then
	echo "site dom tests: FAILED (exit ${status})" >&2
	exit "$status"
fi

echo "site dom tests: green"
