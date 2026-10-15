#!/usr/bin/env bash
# Self-tests for test-gate.sh. The gate is the component that decides whether a build may
# be called green, so every outcome is pinned here by running the real script against
# throwaway fixtures. No case reads the host's own bun or the repo's real node_modules:
# each constructs exactly the precondition it is about, so a pass can only have come from
# the script's logic rather than from the machine it ran on.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gate="${here}/test-gate.sh"
repo="$(cd "${here}/../../.." && pwd)"
valve='BESZEL_ALLOW_SITE_TESTS_NOT_RUN'

EXPECTED_ASSERTIONS=38
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
# present only so the stub runners below can be exec'd: a real bun is a standalone binary,
# so listing bash here makes the fixture faithful without putting bun on PATH.
make_bin() {
	local dir="$1" tool resolved
	mkdir -p "$dir"
	for tool in bash dirname awk grep tail make; do
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

# The registry the gate compares against, written per case rather than defaulted: whether an
# entry is present is itself the variable under test in every classification case, so a
# shared default would quietly decide half the assertions.
make_registry() {
	local root="$1"
	shift
	printf '%s\n' "$@" >"${root}/internal/site/scripts/known-failures.txt"
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

# bun's failing-suite shape: the bare path line the gate keys its file off, then the
# (fail) line it turns into a registry key, then a summary. Separate from make_stub_bun
# because the gate classifies on these exact lines, so their shape is part of what is
# under test and not incidental formatting.
make_failing_bun() {
	cat >"$1" <<STUB
#!${BASH}
printf '%s\n' '$2:' '(fail) $3 [1.20ms]' ' 12 pass' ' 1 fail' ' 1 tests failed:' 'Ran 13 tests across 1 files. [3.40ms]'
exit 1
STUB
	chmod +x "$1"
}

# Exits non-zero with nothing the classifier can parse, which is what a broken runner looks
# like and what must never be mistaken for "only the registered failures remain".
make_broken_bun() {
	cat >"$1" <<STUB
#!${BASH}
printf '%s\n' 'error: no tests found' 'oh no'
exit 2
STUB
	chmod +x "$1"
}

# A go that records how it was invoked, so "make test-go runs Go" becomes a fact about the
# invocation instead of a reading of the recipe. The env branch answers the Makefile's own
# GOOS/GOARCH probes, which would otherwise fail against a stub that only knows `go test`.
make_stub_go() {
	cat >"$1" <<STUB
#!${BASH}
if [ "\${1:-}" = env ]; then printf 'linux\namd64\n'; exit 0; fi
printf '%s\n' "\$*" >>'${2}'
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

# 1. bun unavailable. The defect this gate exists for: it used to print SKIP and exit 0,
# letting 204 frontend tests go unrun behind a build that reported success.
root="$(new_root)"
make_fixture "$root"
make_registry "$root"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "bun missing exits non-zero (rc=${rc})"
else
	no "bun missing must not exit 0"
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
if grep -qF 'curl -fsSL https://bun.sh/install' "${root}/err"; then
	ok "the refusal names the bun install remedy"
else
	no "the refusal must name the bun install remedy, got: $(cat "${root}/err")"
fi

# 2. The escape valve is gone, not merely discouraged. Setting the old variable must change
# nothing at all: same non-zero, same NOT RUN, same remedy. A surviving valve -- even one
# that demanded a written reason -- still leaves "the gate ran" and "the gate declined to
# run" collapsed into the same exit code, which is the entire reason it was deleted rather
# than tightened.
root="$(new_root)"
make_fixture "$root"
make_registry "$root"
run_gate "$root" "${root}/out" "${root}/err" "${valve}=1"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "the old valve variable no longer buys exit 0 (rc=${rc})"
else
	no "the old valve variable must not restore exit 0, got rc=0: $(cat "${root}/err")"
fi
if grep -qi 'not run' "${root}/err" && grep -qF 'curl -fsSL https://bun.sh/install' "${root}/err"; then
	ok "the valve-set refusal is identical to the plain refusal"
else
	no "the valve-set refusal must stay a refusal, got: $(cat "${root}/err")"
fi
if grep -qi 'warning' "${root}/err"; then
	no "the valve path must not survive as a warning either, got: $(cat "${root}/err")"
else
	ok "the valve path left no warning branch behind"
fi

# 3. The removal has to be visible in the source, not only in behaviour: a name that
# lingers in a branch, a default or an unused argument is how the second version of the
# valve ships. Whole-line comments are stripped, so documenting *why* it was deleted stays
# allowed.
for f in "$gate" "${repo}/Makefile"; do
	if grep -v '^[[:space:]]*#' "$f" | grep -qF "$valve"; then
		no "$(basename "$f") still references ${valve} outside a comment"
	else
		ok "$(basename "$f") references no ${valve} outside a comment"
	fi
done

# 4. bun installed at ~/.bun/bin but absent from PATH -- the trap that made this gate
# lie on every non-interactive shell. It must be found and genuinely run.
root="$(new_root)"
make_fixture "$root"
make_registry "$root"
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

# 5. bun present but site dependencies never installed: same class of lie, same refusal.
root="$(new_root)"
make_fixture "$root"
make_registry "$root"
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

# 6. Classification: a failure the registry does not know about is a new failure.
root="$(new_root)"
make_fixture "$root"
make_registry "$root" 'src/lib/other.test.ts::an unrelated registered gap'
mkdir -p "${root}/home/.bun/bin"
make_failing_bun "${root}/home/.bun/bin/bun" \
	'src/lib/plus-fan-tab.guard.test.ts' 'a countdown exists only where a deadline produced one'
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "an unregistered failure is red (rc=${rc})"
else
	no "an unregistered failure must be red, got rc=0: $(cat "${root}/out")"
fi
if grep -qi 'new failures' "${root}/err" &&
	grep -qF 'a countdown exists only where a deadline produced one' "${root}/err"; then
	ok "the new failure is named on stderr"
else
	no "the new failure must be named on stderr, got: $(cat "${root}/err")"
fi
if grep -qi 'green' "${root}/out" "${root}/err"; then
	no "a new failure must not be dressed up as green"
else
	ok "a new failure prints no green"
fi

# 7. Classification: a failure the registry does list is tolerated, and saying so is the
# whole point of the registry. Without this the gate would reject every known gap forever.
root="$(new_root)"
make_fixture "$root"
make_registry "$root" \
	'src/lib/plus-fan-tab.guard.test.ts::a countdown exists only where a deadline produced one'
mkdir -p "${root}/home/.bun/bin"
make_failing_bun "${root}/home/.bun/bin/bun" \
	'src/lib/plus-fan-tab.guard.test.ts' 'a countdown exists only where a deadline produced one'
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "a registered failure keeps the gate green (rc=${rc})"
else
	no "a registered failure must stay tolerated, got rc=${rc}: $(cat "${root}/err")"
fi
if grep -qF 'site tests: green (12 pass, 1 registered failures)' "${root}/out"; then
	ok "the green line counts the tolerated failure"
else
	no "expected a green line counting 1 registered failure, got: $(cat "${root}/out")"
fi

# 8. Registry rot, fully-green suite. This is the state every fixed test drives the file
# into, and the sweep used to be unreachable from it -- the gate returned on `status == 0`
# before ever asking. The file then rotted while every run still reported green.
root="$(new_root)"
make_fixture "$root"
make_registry "$root" \
	'src/lib/plus-fan-tab.guard.test.ts::a countdown exists only where a deadline produced one'
mkdir -p "${root}/home/.bun/bin"
make_stub_bun "${root}/home/.bun/bin/bun"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "a green suite with a stale registry entry is red (rc=${rc})"
else
	no "a stale registry entry must be reported, got rc=0: $(cat "${root}/out")"
fi
if grep -qF 'a countdown exists only where a deadline produced one' "${root}/err"; then
	ok "the rotten entry is named so it can be deleted"
else
	no "the rotten entry must be named on stderr, got: $(cat "${root}/err")"
fi
if grep -qi 'green' "${root}/out"; then
	no "a rotten registry must not print green"
else
	ok "a rotten registry prints no green"
fi

# 9. Registry rot while other failures remain -- the path that did run but only printed a
# suggestion and then exited 0, which is a warning no automation reads.
root="$(new_root)"
make_fixture "$root"
make_registry "$root" \
	'src/lib/plus-fan-tab.guard.test.ts::a countdown exists only where a deadline produced one' \
	'src/lib/rot.test.ts::a test that was fixed long ago'
mkdir -p "${root}/home/.bun/bin"
make_failing_bun "${root}/home/.bun/bin/bun" \
	'src/lib/plus-fan-tab.guard.test.ts' 'a countdown exists only where a deadline produced one'
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "a rotten entry beside a live failure is still red (rc=${rc})"
else
	no "a rotten entry must be reported even beside a live failure, got rc=0: $(cat "${root}/out")"
fi
if grep -qF 'a test that was fixed long ago' "${root}/err"; then
	ok "only the rotten entry is reported, not the live failure"
else
	no "the rotten entry must be named, got: $(cat "${root}/err")"
fi
if grep -qi 'green' "${root}/out"; then
	no "a rotten entry must not print green alongside a live failure"
else
	ok "no green is printed beside a live failure when the registry is rotten"
fi

# 10. A runner that dies without saying anything parseable is not the same claim as
# "only known gaps remain", and must not borrow that claim's green line.
root="$(new_root)"
make_fixture "$root"
make_registry "$root" 'src/lib/other.test.ts::an unrelated registered gap'
mkdir -p "${root}/home/.bun/bin"
make_broken_bun "${root}/home/.bun/bin/bun"
run_gate "$root" "${root}/out" "${root}/err"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "a broken runner is red (rc=${rc})"
else
	no "a broken runner must not be green, got rc=0: $(cat "${root}/out")"
fi
if grep -qF 'without a parseable failure' "${root}/err"; then
	ok "the broken runner is distinguished from known gaps"
else
	no "the broken-runner path must say so, got: $(cat "${root}/err")"
fi

# 11. The Makefile used to own the skip itself, which is how the defect shipped. Pin the
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

# 12. A Go-only contributor must not need a 505MB bun install to check a Go change, so the
# Go suite gets its own target -- asserted by watching a stub go record the invocation,
# because a target that merely mentions go in a comment is still an empty target.
go_plan="$(cd "$repo" && make --no-print-directory -n test-go 2>&1)"
case "$go_plan" in
*"go test -tags='testing no_ui' ./..."*) ok "make test-go runs the tagged Go suite" ;;
*) no "make test-go must run go test -tags='testing no_ui' ./..., got: ${go_plan}" ;;
esac
root="$(new_root)"
make_bin "${root}/bin"
make_stub_go "${root}/bin/go" "${root}/go-calls"
env -i HOME="${root}/home" PATH="${root}/bin:/usr/bin:/bin" \
	"${root}/bin/make" -C "$repo" test-go >"${root}/out" 2>"${root}/err"
if grep -qF 'test -tags=testing no_ui ./...' "${root}/go-calls" 2>/dev/null; then
	ok "make test-go really invokes go test (recorded: $(cat "${root}/go-calls"))"
else
	no "make test-go did not invoke go test, calls: $(cat "${root}/go-calls" 2>&1) err: $(cat "${root}/err")"
fi

# 13. `make test` is the conjunction and owns no skip of its own. Reachability is
# asserted on what the plan actually runs, never on a target name: make -n
# prints recipes, not the prerequisites that reached them, so a rule that is
# only named in a comment cannot satisfy this.
test_plan="$(cd "$repo" && make --no-print-directory -n test 2>&1)"
case "$test_plan" in
*"go test -tags='testing no_ui' ./..."*) ok "make test includes the Go suite" ;;
*) no "make test must run the Go suite, got: ${test_plan}" ;;
esac
case "$test_plan" in
*test-gate.sh*) ok "make test runs the site gate script" ;;
*) no "make test must run the site gate script, got: ${test_plan}" ;;
esac
case "$test_plan" in
*test-gate.test.sh*) ok "make test runs the site gate self-test first" ;;
*) no "make test must run the site gate self-test first, got: ${test_plan}" ;;
esac
recipes="$(awk '/^(test|test-go|test-site):/{f=1;next} /^[^\t].*:/{f=0} f' "${repo}/Makefile")"
if printf '%s\n' "$recipes" | grep -qE '^[[:space:]]*-| \|\| '; then
	no "the test recipes must contain no ignored or tolerated failure, got: ${recipes}"
else
	ok "the test recipes contain no ignored or tolerated failure"
fi

# A stray file named `test`, `test-go` or `test-site` would shadow the target and let
# `make test` do something other than run tests: the same lie from a new direction.
phony="$(grep -m1 '^\.PHONY:' "${repo}/Makefile")"
for target in test test-go test-site; do
	case " $phony " in
	*" ${target} "*)
		ok "Makefile declares ${target} .PHONY, so a file cannot shadow it"
		;;
	*)
		no "Makefile must declare ${target} .PHONY, got: ${phony}"
		;;
	esac
done


# The count is part of the contract. A gate whose own suite can silently shrink to
# a bare `true` prints the same green line while asserting nothing, which is the
# failure mode this arrangement exists to make impossible.
printf '\n# gate self-test: %d passed, %d failed\n' "$passed" "$failed"
if [ "$passed" -ne "$EXPECTED_ASSERTIONS" ]; then
	echo "gate self-test: expected ${EXPECTED_ASSERTIONS} assertions, ran ${passed}" >&2
	exit 1
fi

# A gate that has never been seen to fail has not been tested, only run. This block
# makes one assertion fail on purpose and requires the run to exit non-zero because
# of it, so a green gate is evidence about failures and not only about successes.
if [ "${BESZEL_GATE_SELFFAIL:-}" = 1 ]; then
	no "deliberate failure, to prove the verdict line still decides green"
fi

# The verdict line is pinned. Replacing it with `true` is the one mutation every other
# assertion survives, because nothing failed and so nothing was counted to contradict
# it; a gate that stops deciding by its failures has stopped deciding anything.
if [ "$failed" -ne 0 ]; then
	exit 1
fi
if ! grep -qF '[ "$failed" -ne 0 ]; then' "$0"; then
	echo "gate self-test: the failure verdict was replaced; green is no longer decided by a failure" >&2
	exit 1
fi
exit 0
