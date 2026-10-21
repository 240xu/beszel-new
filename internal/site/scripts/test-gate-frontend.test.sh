#!/usr/bin/env bash
# Self-tests for the frontend build gate.
#
# The gate is the component that decides whether a source tree may be called green, so
# every outcome is pinned here. Nothing here reads the host's real node_modules or the
# host's real toolchain: each case constructs exactly the precondition it is about, so a
# pass can only have come from the script's logic rather than from the machine it ran on.
#
# The three node helpers the gate calls (catalog comparison, linter baseline, dependency
# audit) are exercised directly against throwaway fixtures, because that is where the
# decisions are made. The shell gate's own job -- refusing to run when a toolchain is
# missing -- is exercised through the gate.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gate="${here}/test-gate-frontend.sh"
repo="$(cd "${here}/../../.." && pwd)"

passed=0
failed=0
seq_no=0
roots=()

ok() {
	seq_no=$((seq_no + 1))
	printf 'ok %d - %s\n' "$seq_no" "$1"
	passed=$((passed + 1))
}

no() {
	seq_no=$((seq_no + 1))
	printf 'not ok %d - %s\n' "$seq_no" "$1"
	failed=$((failed + 1))
}

cleanup() {
	local r
	for r in ${roots[@]+"${roots[@]}"}; do rm -rf "$r"; done
}
trap cleanup EXIT

new_root() {
	local r
	r="$(mktemp -d)"
	roots+=("$r")
	printf '%s' "$r"
}

# A fixture that mirrors the repo layout the gate expects, with the real helper scripts
# copied in: the helpers ARE what is under test, so stubbing them would test the stub.
make_fixture() {
	local root="$1"
	mkdir -p "${root}/internal/site/scripts" "${root}/internal/site/src/locales/en" \
		"${root}/internal/site/node_modules" "${root}/scripts" "${root}/bin"
	cp "$gate" "${root}/internal/site/scripts/test-gate-frontend.sh"
	for f in check-catalogs.mjs lint-baseline.mjs audit-policy.mjs; do
		cp "${repo}/scripts/${f}" "${root}/scripts/${f}"
	done
	cp "${repo}/scripts/npm-audit-allowlist.json" "${root}/scripts/npm-audit-allowlist.json"
	cp "${repo}/scripts/biome-baseline.txt" "${root}/scripts/biome-baseline.txt"
	printf 'msgid ""\nmsgstr ""\n\nmsgid "Up"\nmsgstr "Up"\n' >"${root}/internal/site/src/locales/en/en.po"
}

# A stub npx whose behaviour is read from the fixture, so each case can make exactly one
# step fail. A real toolchain is never consulted.
make_stub_npx() {
	local dir="$1"
	cat >"${dir}/npx" <<STUB
#!${BASH}
case "\$*" in
*"lingui compile"*) exit \${FIXTURE_LINGUI_COMPILE:-0} ;;
*"lingui extract"*) cp "\${FIXTURE_EXTRACTED_PO:-/dev/null}" "${FIXTURE_TARGET_PO:-/dev/null}" 2>/dev/null; exit \${FIXTURE_LINGUI_EXTRACT:-0} ;;
*"tsc -b"*) exit \${FIXTURE_TSC:-0} ;;
*"biome ci"*) cat "\${FIXTURE_BIOME_JSON:-/dev/null}" 2>/dev/null; exit \${FIXTURE_BIOME_EXIT:-0} ;;
esac
exit 0
STUB
	chmod +x "${dir}/npx"
}

# A stub npm whose audit report says what the case needs it to say.
make_stub_npm() {
	local dir="$1"
	cat >"${dir}/npm" <<STUB
#!${BASH}
cat "\${FIXTURE_AUDIT_JSON:-/dev/null}" 2>/dev/null
exit 1
STUB
	chmod +x "${dir}/npm"
}

# The fixture variables have to survive `env -i`, which is what makes the run hermetic:
# a variable that leaked in from the host would be a precondition the case did not build.
run_gate() {
	local root="$1" out="$2" err="$3"
	shift 3
	env -i HOME="${root}/home" PATH="${root}/bin:/usr/bin:/bin" "$@" \
		bash "${root}/internal/site/scripts/test-gate-frontend.sh" >"$out" 2>"$err"
}

# 1. No node toolchain: the gate must refuse, loudly and non-zero, and must not print a
# green line. A gate that cannot run its tools must never look like a gate that did.
root="$(new_root)"
make_fixture "$root"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "a missing toolchain exits non-zero (rc=${rc})"
else
	no "a missing toolchain must not exit 0"
fi
if grep -qi 'not run' "${root}/err"; then
	ok "the refusal states the gate did not run"
else
	no "the refusal must say the gate did not run, got: $(cat "${root}/err")"
fi
if grep -qi 'green' "${root}/out"; then
	no "a refusal must not print green"
else
	ok "a refusal prints no green"
fi

# 2. Site dependencies not installed: the same class of refusal.
root="$(new_root)"
make_fixture "$root"
rmdir "${root}/internal/site/node_modules"
mkdir -p "${root}/bin"
make_stub_npx "${root}/bin"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ] && grep -qi 'not run' "${root}/err"; then
	ok "missing node_modules is a refusal (rc=${rc})"
else
	no "missing node_modules must be a refusal, got rc=${rc}: $(cat "${root}/err")"
fi

# 3. Every step green. The stub toolchain reports success and the real helpers agree
# with the fixture, so the gate must finish green.
root="$(new_root)"
make_fixture "$root"
mkdir -p "${root}/bin"
make_stub_npx "${root}/bin"
make_stub_npm "${root}/bin"
printf '{"vulnerabilities":{},"metadata":{"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}}}' >"${root}/audit.json"
printf '{"diagnostics":[]}' >"${root}/biome.json"
cp "${root}/internal/site/src/locales/en/en.po" "${root}/extracted.po"
run_gate "$root" "${root}/out" "${root}/err" \
	FIXTURE_EXTRACTED_PO="${root}/extracted.po" \
	FIXTURE_TARGET_PO="${root}/internal/site/src/locales/en/en.po" \
	FIXTURE_AUDIT_JSON="${root}/audit.json" \
	FIXTURE_BIOME_JSON="${root}/biome.json"
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "an all-green fixture is green (rc=0)"
else
	no "an all-green fixture must be green, got rc=${rc}: $(cat "${root}/err")"
fi
if grep -qF 'site build gate: green' "${root}/out"; then
	ok "the green line is printed"
else
	no "expected the green line, got: $(cat "${root}/out")"
fi

# 4. A type error is red. The type check is the gate the repository did not have; a
# failure in it has to reach the build.
root="$(new_root)"
make_fixture "$root"
mkdir -p "${root}/bin"
make_stub_npx "${root}/bin"
make_stub_npm "${root}/bin"
printf '{"vulnerabilities":{},"metadata":{"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}}}' >"${root}/audit.json"
printf '{"diagnostics":[]}' >"${root}/biome.json"
cp "${root}/internal/site/src/locales/en/en.po" "${root}/extracted.po"
run_gate "$root" "${root}/out" "${root}/err" \
	FIXTURE_TSC=1 \
	FIXTURE_EXTRACTED_PO="${root}/extracted.po" \
	FIXTURE_TARGET_PO="${root}/internal/site/src/locales/en/en.po" \
	FIXTURE_AUDIT_JSON="${root}/audit.json" \
	FIXTURE_BIOME_JSON="${root}/biome.json"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "a type error is red (rc=${rc})"
else
	no "a type error must be red, got rc=0"
fi
if grep -qF 'tsc -b' "${root}/err"; then
	ok "the failing step is named"
else
	no "the failing step must be named, got: $(cat "${root}/err")"
fi

# 5. A new linter finding is red, and a finding the baseline already records is not.
# That is the whole contract of the recorded baseline.
root="$(new_root)"
make_fixture "$root"
new_finding='{"diagnostics":[{"category":"lint/a11y/noSvgWithoutTitle","severity":"error","description":"d","location":{"path":{"file":"src/new.tsx"}},"message":[{"content":"d"}]}]}'
printf '%s' "$new_finding" >"${root}/biome-new.json"
if node "${repo}/scripts/lint-baseline.mjs" --tool biome --report "${root}/biome-new.json" --baseline "${repo}/scripts/biome-baseline.txt" >/dev/null 2>&1; then
	no "a finding outside the baseline must be red"
else
	ok "a finding outside the baseline is red"
fi
printf '{"diagnostics":[]}' >"${root}/biome-empty.json"
if node "${repo}/scripts/lint-baseline.mjs" --tool biome --report "${root}/biome-empty.json" --baseline "${repo}/scripts/biome-baseline.txt" >/dev/null 2>&1; then
	ok "a report with nothing new is green"
else
	no "a report with nothing new must be green"
fi

# 6. A catalog that is missing a message the source needs is red; a catalog that carries
# a message the source dropped is red too. Both are ways a translation goes stale.
root="$(new_root)"
make_fixture "$root"
mkdir -p "${root}/committed/en" "${root}/extracted/en"
printf 'msgid ""\nmsgstr ""\n\nmsgid "Up"\nmsgstr "Up"\n' >"${root}/committed/en/en.po"
printf 'msgid ""\nmsgstr ""\n\nmsgid "Up"\nmsgstr "Up"\n\nmsgid "Down"\nmsgstr "Down"\n' >"${root}/extracted/en/en.po"
if node "${repo}/scripts/check-catalogs.mjs" "${root}/committed" "${root}/extracted" >/dev/null 2>&1; then
	no "a catalog missing a source message must be red"
else
	ok "a catalog missing a source message is red"
fi
printf 'msgid ""\nmsgstr ""\n\nmsgid "Up"\nmsgstr "Up"\n' >"${root}/extracted/en/en.po"
printf 'msgid ""\nmsgstr ""\n\nmsgid "Up"\nmsgstr "Up"\n\nmsgid "Gone"\nmsgstr "Gone"\n' >"${root}/committed/en/en.po"
if node "${repo}/scripts/check-catalogs.mjs" "${root}/committed" "${root}/extracted" >/dev/null 2>&1; then
	no "a catalog carrying a dropped message must be red"
else
	ok "a catalog carrying a dropped message is red"
fi

# 7. A production advisory that is not on the allowlist is red; one that is on it is not.
root="$(new_root)"
make_fixture "$root"
mkdir -p "${root}/bin"
make_stub_npm "${root}/bin"
cat >"${root}/allow.json" <<'JSON'
{}
JSON
cat >"${root}/audit-bad.json" <<'JSON'
{"vulnerabilities":{"left-pad":{"severity":"high","via":[{"url":"https://github.com/advisories/GHSA-xxxx","title":"t"}]}},"metadata":{"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":1,"critical":0,"total":1}}}
JSON
if PATH="${root}/bin:${PATH}" FIXTURE_AUDIT_JSON="${root}/audit-bad.json" \
	node "${repo}/scripts/audit-policy.mjs" --site-dir "$root" --allowlist "${root}/allow.json" >/dev/null 2>&1; then
	no "an unlisted production advisory must be red"
else
	ok "an unlisted production advisory is red"
fi
cat >"${root}/allow.json" <<'JSON'
{"https://github.com/advisories/GHSA-xxxx":"because"}
JSON
if PATH="${root}/bin:${PATH}" FIXTURE_AUDIT_JSON="${root}/audit-bad.json" \
	node "${repo}/scripts/audit-policy.mjs" --site-dir "$root" --allowlist "${root}/allow.json" >/dev/null 2>&1; then
	ok "an allowlisted production advisory is tolerated"
else
	no "an allowlisted production advisory must be tolerated"
fi

printf '\n# frontend build gate self-test: %d passed, %d failed\n' "$passed" "$failed"
if [ "$failed" -ne 0 ]; then
	exit 1
fi
exit 0
