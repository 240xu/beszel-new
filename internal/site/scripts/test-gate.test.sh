#!/usr/bin/env bash
# Self-tests for test-gate.sh. The gate is the component that decides whether a build
# is allowed to be called green, so every preflight outcome is pinned here by running
# the real script against throwaway fixtures. No case reads the host's own bun or the
# repo's real node_modules: each constructs exactly the precondition it is about, so a
# pass can only have come from the script's logic.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gate="${here}/test-gate.sh"
repo="$(cd "${here}/../../.." && pwd)"
valve='BESZEL_ALLOW_SITE_TESTS_NOT_RUN'

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

# A PATH carrying only the external tools the gate shells out to, so anything else --
# bun included -- reads as absent, which is the exact condition under test. bash is
# present only so the stub bun below can be exec'd: a real bun is a standalone binary,
# so listing bash here makes the fixture faithful without putting bun on PATH.
make_bin() {
	local dir="$1" tool resolved
	mkdir -p "$dir"
	for tool in bash dirname awk grep tail; do
		if resolved="$(command -v "$tool" 2>/dev/null)"; then
			ln -sf "$resolved" "$dir/${tool}"
		fi
	done
}

# Mirrors the repo layout so the gate resolves site_dir inside the fixture and can
# never reach the real checkout's node_modules or bun.
make_fixture() {
	local root="$1"
	mkdir -p "${root}/internal/site/scripts" "${root}/home" \
		"${root}/internal/site/node_modules"
	cp "$gate" "${root}/internal/site/scripts/test-gate.sh"
	chmod +x "${root}/internal/site/scripts/test-gate.sh"
	make_bin "${root}/bin"
}

# Reports a distinctive pass count, so "the gate ran the binary it discovered" becomes a
# question about the gate's own stdout rather than an assumption about host state. Built
# from builtins under an absolute shebang, so the stub depends on nothing in the
# restricted PATH -- otherwise the fixture would test the stub's shell, not the gate.
make_stub_bun() {
	cat >"$1" <<STUB
#!${BASH}
printf '%s\n' ' 7 pass' ' 0 fail' ' 3 expect() calls' 'Ran 7 tests across 1 files. [1.00ms]'
exit 0
STUB
	chmod +x "$1"
}

# $BASH is passed as the interpreter by absolute path because the fixture PATH has no
# bash of its own; `env -i` keeps the run hermetic instead of inheriting a real PATH.
run_gate() {
	local root="$1" out="$2" err="$3"
	shift 3
	env -i HOME="${root}/home" PATH="${root}/bin" "$@" \
		"$BASH" "${root}/internal/site/scripts/test-gate.sh" >"$out" 2>"$err"
}

# 1. The defect itself: bun unavailable and no valve. This used to print SKIP and exit
# 0, letting 204 frontend tests go unrun behind a green build.
root="$(new_root)"
make_fixture "$root"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "bun missing without the valve exits non-zero (rc=${rc})"
else
	no "bun missing without the valve must not exit 0"
fi
if grep -q 'bun' "${root}/err"; then
	ok "the refusal names bun on stderr"
else
	no "the refusal must name bun on stderr, got: $(cat "${root}/err")"
fi
# Asserting only rc would also accept the old behaviour, where a missing bun made the
# runner die with 127 and the gate reported a broken runner. The refusal has to be a
# recognised "could not run", not an accident that happens to land non-zero.
if grep -qi 'not run' "${root}/err"; then
	ok "the refusal states the suite did not run"
else
	no "the refusal must say the suite did not run, got: $(cat "${root}/err")"
fi

# 2. The explicit valve: still exit 0, but loudly, and never dressed up as a pass.
root="$(new_root)"
make_fixture "$root"
run_gate "$root" "${root}/out" "${root}/err" "${valve}=1"
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "the valve lets an unrun suite exit 0 (rc=${rc})"
else
	no "the valve must exit 0, got rc=${rc}: $(cat "${root}/err")"
fi
if grep -qF "$valve" "${root}/err" && grep -qi 'warning' "${root}/err"; then
	ok "the valve prints a warning to stderr naming the valve"
else
	no "the valve must warn on stderr and name ${valve}, got: $(cat "${root}/err")"
fi
if grep -qi 'green' "${root}/out" "${root}/err"; then
	no "nothing on the unrun path may claim green"
else
	ok "nothing on the unrun path claims green"
fi

# 3. bun installed at ~/.bun/bin but absent from PATH -- the trap that made this gate
# lie on every non-interactive shell. It must be found and genuinely run.
root="$(new_root)"
make_fixture "$root"
mkdir -p "${root}/home/.bun/bin"
make_stub_bun "${root}/home/.bun/bin/bun"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "bun outside PATH is discovered and run (rc=${rc})"
else
	no "bun at ~/.bun/bin must be discovered, got rc=${rc}: $(cat "${root}/err")"
fi
if grep -qF 'site tests: green (7 pass, 0 registered failures)' "${root}/out"; then
	ok "the discovered binary really ran -- its own counts came back through the gate"
else
	no "expected the stub's own summary on stdout, got: $(cat "${root}/out")"
fi

# 4. bun present but site dependencies never installed: same class of lie, same refusal.
root="$(new_root)"
make_fixture "$root"
rmdir "${root}/internal/site/node_modules"
mkdir -p "${root}/home/.bun/bin"
make_stub_bun "${root}/home/.bun/bin/bun"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "missing node_modules exits non-zero (rc=${rc})"
else
	no "missing node_modules must not exit 0"
fi
if grep -q 'bun install' "${root}/err"; then
	ok "the refusal names the install command"
else
	no "the refusal must name 'bun install', got: $(cat "${root}/err")"
fi
if grep -qi 'not run' "${root}/err"; then
	ok "the node_modules refusal states the suite did not run"
else
	no "the node_modules refusal must say the suite did not run, got: $(cat "${root}/err")"
fi

# 5. The Makefile used to own the skip itself, which is how the defect shipped. Pin the
# delegation so a future edit cannot quietly reintroduce an inline skip ahead of the gate.
recipe="$(cd "$repo" && make -n test-site 2>&1)"
case "$recipe" in
*internal/site/scripts/test-gate.sh*) ok "make test-site delegates to the gate script" ;;
*) no "make test-site must delegate to the gate script, got: ${recipe}" ;;
esac
case "$recipe" in
*'SKIP internal/site'*) no "make test-site must not print a skip of its own" ;;
*) ok "make test-site prints no skip of its own" ;;
esac

# A stray file named `test` or `test-site` would shadow the target and let `make test`
# do something other than run tests: the same lie arriving from a new direction.
phony="$(grep -m1 '^\.PHONY:' "${repo}/Makefile")"
for target in test test-site; do
	case " $phony " in
	*" ${target} "*)
		ok "Makefile declares ${target} .PHONY, so a file cannot shadow it"
		;;
	*)
		no "Makefile must declare ${target} .PHONY, got: ${phony}"
		;;
	esac
done

printf '\n# gate self-test: %d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]