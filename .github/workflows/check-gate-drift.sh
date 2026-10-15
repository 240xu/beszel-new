#!/usr/bin/env bash
# Gate-drift guard: proves the CI workflow still runs the Makefile's real gate.
#
# The failure this exists to catch is not a red test. It is a gate that reports green while
# testing nothing, which this repository has now paid for twice. A workflow that re-types
# `go test ./...` by hand instead of calling `make test` silently loses the
# `-tags 'testing no_ui'` that 64 test files need, and loses the entire frontend gate with
# it. So: the workflow calls exactly one make target, and this script proves that target is
# still the conjunction of both suites rather than a name that happens to exist.
#
# Nothing here reads a git tag or any other history. This repository's history has been
# rewritten, so the guard has to survive a clone with no tags at all.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root" || exit 1

makefile="Makefile"
workflow=".github/workflows/tests.yml"

failures=0
fail() { printf 'gate drift: %s\n' "$*" >&2; failures=$((failures + 1)); }

# --- Makefile -> one record per target: TARGET <US> PREREQUISITES <US> RECIPE -------------
# Only column-0 lines may open a target; recipe lines begin with a tab. Variable assignments
# and `ifeq`/`endif` are skipped so they can never be mistaken for a rule.
# <US> is ASCII 31, not a tab: tab is IFS whitespace, so `read` would swallow the empty
# prerequisite field that `test:` legitimately has.
make_inventory() {
	awk '
	BEGIN { US = sprintf("%c", 31) }
	function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
	function emit() {
		if (target != "") {
			gsub(/\n/, " ", recipe)
			gsub(/[ \t]+/, " ", recipe)
			printf "%s%s%s%s%s\n", target, US, prereqs, US, recipe
		}
		target = ""; prereqs = ""; recipe = ""
	}
	{
		line = $0
		if (substr(line, 1, 1) == "\t") {
			if (target != "") recipe = recipe "\n" line
			next
		}
		if (line ~ /^[ \t]*$/ || line ~ /^[ \t]*#/) next
		if (line ~ /^[A-Za-z_.][A-Za-z0-9_.-]*[ \t]*(\?=|:=|\+=|=[^=])/) next
		if (line ~ /^(ifeq|ifneq|ifdef|ifndef|else|endif|include)/) next
		colon = 0
		for (i = 1; i <= length(line); i++) {
			if (substr(line, i, 1) != ":") continue
			if (substr(line, i + 1, 1) == "=") { i++; continue }
			colon = i
			break
		}
		if (colon == 0) next
		emit()
		target = trim(substr(line, 1, colon - 1))
		prereqs = trim(substr(line, colon + 1))
	}
	END { emit() }
	' "$1"
}

# --- Workflow -> only the lines a `run:` step would actually execute ----------------------
# Comments are deliberately excluded: a comment that says "runs make test" must not be able to
# satisfy this check on behalf of a gate step that was deleted or rewritten to something else.
workflow_run_lines() {
	awk '
	function indent(s,   i) { for (i = 1; i <= length(s); i++) if (substr(s, i, 1) == " ") return i - 1; return length(s) }
	{
		line = $0
		cur = indent(line)
		if (inrun) {
			if (line ~ /^[ \t]*$/ || cur > runind) { print line; next }
			inrun = 0
		}
		if (line ~ /^[ ]*-?[ ]*run:/) {
			rest = line
			sub(/^[ ]*-?[ ]*run:[ \t]*/, "", rest)
			if (rest ~ /^[|>][-+0-9]*[ \t]*$/) { inrun = 1; runind = cur; next }
			if (rest != "") { print rest; next }
		}
	}
	' "$1"
}

if [ ! -f "$workflow" ]; then
	fail "workflow ${workflow} is missing, so nothing forces the gate to run"
	printf 'gate drift: %d problem(s)\n' "$failures" >&2
	exit 1
fi

declare -A RECIPE=() PREREQS=()
while IFS=$'\x1f' read -r t p r; do
	[ -n "$t" ] || continue
	RECIPE["$t"]="${RECIPE[$t]:-}${RECIPE[$t]:+ }${r}"
	PREREQS["$t"]="${PREREQS[$t]:-}${PREREQS[$t]:+ }${p}"
done < <(make_inventory "$makefile")

# Does this target do any work, directly or through a prerequisite?
substantive() {
	local target="$1" p
	case "${SEEN[$target]:-}" in
	yes) return 0 ;;
	no) return 1 ;;
	pending) return 1 ;;
	esac
	SEEN[$target]=pending
	if [ -n "${RECIPE[$target]:-}" ]; then SEEN[$target]=yes; return 0; fi
	for p in ${PREREQS[$target]:-}; do
		if [ -n "${RECIPE[$p]:-}" ] || [ -n "${PREREQS[$p]:-}" ]; then
			if substantive "$p"; then SEEN[$target]=yes; return 0; fi
		fi
	done
	SEEN[$target]=no
	return 1
}

# Every target reachable from the given one, including itself.
closure_of() {
	local stack=("$1") cur p
	local -a seen=()
	while [ ${#stack[@]} -gt 0 ]; do
		cur="${stack[0]}"
		stack=("${stack[@]:1}")
		case " ${seen[*]:-} " in *" $cur "*) continue ;; esac
		seen+=("$cur")
		for p in ${PREREQS[$cur]:-}; do stack+=("$p"); done
	done
	printf '%s\n' "${seen[@]}"
}

declare -A SEEN=()

# A gate nothing triggers is a gate nothing enforces. This fork's default branch is
# beszel-new, so a workflow watching only main has already shipped this exact defect
# once: every push to the branch under test skipped the suite and stayed green.
default_branch="$(git symbolic-ref --short HEAD 2>/dev/null || echo beszel-new)"

# The branch list is read with awk, the same tool the Makefile inventory above uses:
# no YAML dependency is guaranteed on a runner and the block shape is fixed.
push_branches=$(awk '
		/^[ \t]*push:/ { in_push = 1; next }
		in_push && /^[ \t]*branches:/ { in_branches = 1; next }
		in_push && in_branches && /^[ \t]*-/ { print $2; next }
		in_push && /^[ \t]*[A-Za-z]/ { in_push = 0 }
	' "$workflow")

if ! printf '%s\n' "$push_branches" | grep -qx -- "$default_branch"; then
	fail "the push trigger does not cover the default branch ${default_branch} (covers: ${push_branches:-none}), so pushes to the branch under test run no gate"
fi
if ! grep -qE '^[ \t]*pull_request:' "$workflow"; then
	fail "the workflow has no pull_request trigger, so review is gated by nothing"
fi

if grep -q 'continue-on-error' "$workflow"; then
	fail "${workflow} sets continue-on-error; a step allowed to fail is not a gate"
fi
if grep -q 'BESZEL_ALLOW_SITE_TESTS_NOT_RUN' "$workflow"; then
	fail "${workflow} references BESZEL_ALLOW_SITE_TESTS_NOT_RUN; that valve is deleted on purpose"
fi

invoked=()
while IFS= read -r target; do
	[ -n "$target" ] || continue
	invoked+=("$target")
done < <(workflow_run_lines "$workflow" | grep -oE 'make[[:space:]]+[A-Za-z0-9_./-]+' | awk '{print $2}' | sort -u)

if [ ${#invoked[@]} -eq 0 ]; then
	fail "${workflow} executes no make target at all, so it runs no gate"
fi

gate_target=''
for target in "${invoked[@]}"; do
	if [ -z "${RECIPE[$target]:-}" ] && [ -z "${PREREQS[$target]:-}" ]; then
		fail "workflow runs 'make ${target}' but ${makefile} has no such target"
		continue
	fi
	if ! substantive "$target"; then
		fail "workflow runs 'make ${target}' but that target is empty: no recipe and no prerequisites"
		continue
	fi
	# A gate is the conjunction of both suites. Anything that reaches only one is a half gate.
	reached="$(closure_of "$target")"
	if printf '%s\n' "$reached" | grep -qx 'test-go' && printf '%s\n' "$reached" | grep -qx 'test-site'; then
		if [ -z "$gate_target" ]; then gate_target="$target"; fi
	fi
done

if [ -z "$gate_target" ]; then
	fail "no make target the workflow runs reaches both test-go and test-site, so no gate is being enforced"
fi

if [ -n "$gate_target" ]; then
	reached="$(closure_of "$gate_target")"
	go_recipe="${RECIPE[test-go]:-}"
	site_recipe="${RECIPE[test-site]:-}"

	case " $(printf '%s\n' "$reached" | tr '\n' ' ') " in
	*" test-go "*) ;;
	*) fail "'make ${gate_target}' no longer reaches test-go" ;;
	esac
	case " $(printf '%s\n' "$reached" | tr '\n' ' ') " in
	*" test-site "*) ;;
	*) fail "'make ${gate_target}' no longer reaches test-site" ;;
	esac

	# The tag is upstream convention, not a nicety: 64 test files carry //go:build testing and
	# internal/tests/hub.go does too, so `go test ./...` without it does not compile.
	if ! printf '%s\n' "$go_recipe" | grep -qE 'go[[:space:]]+test'; then
		fail "test-go does not invoke 'go test' (recipe: ${go_recipe:-<none>})"
	fi
	if ! printf '%s\n' "$go_recipe" | grep -q -- '-tags' ||
		! printf '%s\n' "$go_recipe" | grep -q -- 'testing' ||
		! printf '%s\n' "$go_recipe" | grep -q -- 'no_ui'; then
		fail "test-go lost the 'testing no_ui' build tags (recipe: ${go_recipe:-<none>})"
	fi
	if ! printf '%s\n' "$site_recipe" | grep -q 'test-gate\.sh'; then
		fail "test-site no longer runs internal/site/scripts/test-gate.sh (recipe: ${site_recipe:-<none>})"
	fi
fi

if [ "$failures" -ne 0 ]; then
	printf 'gate drift: %d problem(s); the workflow is not running the gate as written\n' "$failures" >&2
	exit 1
fi

printf "gate drift: none -- %s runs 'make %s', which reaches both suites\n" \
	"$workflow" "${gate_target:-<none>}"
exit 0