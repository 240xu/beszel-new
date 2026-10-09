#!/usr/bin/env python3
"""Mutation harness for the agent/plus curve and platform readers.

It exists because a mutation number in a report is only evidence if the next
reviewer can re-derive it. Everything this file claims is derived here, from
this checkout, and it can be re-run with one command (see --help).

Design rules, each one a scar:

  * It runs in a throwaway `git worktree`, so the checkout under review is
    never edited, not even transiently. A harness that mutates the tree it is
    reviewing cannot report its own restore, because it would be reporting on
    the state it just left.
  * Every mutation declares what it is supposed to do (expect). A run whose
    outcome disagrees is a failed run, not a footnote: the point of the log is
    that a reviewer can trust a mismatch when they see one.
  * A mutation that does not build is recorded as COMPILE and never counted as
    a kill. A compiler error is not a test result, and scoring it as one
    inflates coverage with nothing behind it.
  * Every patch is checked against a pristine copy before anything runs, so a
    stale pattern aborts the run instead of silently mutating nothing and
    reporting a green suite.
  * The baseline is asserted green before the first mutation and again after the
    last. A restore that quietly fails turns every later result into noise, and
    a suite can be green for the wrong reason; that is the failure mode this
    file was written after.

Usage:
    python3 agent/plus/testdata/mutate.py --log /tmp/mutation-log.txt
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

# ---------------------------------------------------------------- patch model


def fuzzy(literal: str) -> str:
    """Regex for a literal where any run of spaces/tabs matches any other run.

    Hand-transcribed Go fragments drift by one tab and then silently match
    nothing, which reads downstream as a test that stopped killing anything.
    The separators are kept rather than dropped, so a leading indent stays part
    of the pattern: dropping it would turn every line-initial fragment into a
    substring match anchored nowhere.
    """

    def line_pattern(line: str) -> str:
        parts = re.split(r"([ \t]+)", line)
        return "".join(p if i % 2 else re.escape(p) for i, p in enumerate(parts))

    return "\n".join(line_pattern(line) for line in literal.split("\n"))


class Edit:
    def __init__(self, old: str, new: str) -> None:
        self.old = old
        self.new = new

    def apply(self, text: str, where: str) -> str:
        # The replacement goes through a lambda, so a backslash in it stays
        # literal instead of being read as a group reference.
        out, count = re.subn(fuzzy(self.old), lambda _m: self.new, text)
        if count == 0:
            raise PatchMiss(where, self.old)
        return out


class PatchMiss(Exception):
    def __init__(self, where: str, old: str) -> None:
        super().__init__(f"pattern not found in {where}: {old.splitlines()[0]!r}")
        self.where = where


class Mutation:
    def __init__(
        self,
        ident: str,
        claim: str,
        expect: str,
        files: dict[str, list[Edit]],
        creates: dict[str, str] | None = None,
    ) -> None:
        self.ident = ident
        self.claim = claim
        self.expect = expect
        self.files = files
        self.creates = creates or {}


CURVE = "agent/plus/curve.go"
PLAT = "agent/plus/platformconf.go"
LEVELS = "agent/plus/levels.go"
FANCTL = "agent/plus/fanctl_linux.go"

# The multi-line blocks below are the ones whose exact text matters most, so
# each is quoted as it stands in the tree. run --preflight to prove it.
JUNK_BLOCK = """		if eq < 0 {
			return nil, fmt.Errorf("line %d: %q is not an assignment, and the daemon sources this file",
				n+1, strings.TrimSpace(raw))
		}"""

UNKNOWN_BLOCK = """		field, ok := dst[key]
		if !ok {
			return nil, fmt.Errorf("line %d: unknown key %q, which would leave that parameter on its compiled-in default",
				n+1, key)
		}"""

ATOI_BLOCK = """		v, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s=%q is out of range", n+1, key, val)
		}"""

STRIP_COMMENT_BLOCK = """		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}"""

RENDER_LOOP = "	for _, f := range curveFields(c) {"
ACPI_CHECK = '	for _, acpi := range []struct{ name, val string }{{"EC_ROOT", p.ECRoot}, {"FAN_DEVICE", p.FANDevice}} {'

MUTATIONS: list[Mutation] = [
    Mutation(
        "M01",
        "band-duty table opened up to any duty",
        "killed",
        {CURVE: [Edit("		if !curveBandDuty[l.val] {", "		if false {")]},
    ),
    Mutation(
        "M02",
        "drop the per-band [0,CAP] range check",
        "killed",
        {
            CURVE: [
                Edit("		if l.val < 0 || l.val > c.Cap {", "		if false {")
            ]
        },
    ),
    Mutation(
        "M03",
        "strictly increasing edges relaxed to non-strict",
        "killed",
        {
            CURVE: [
                Edit(
                    "		if edges[i].val <= edges[i-1].val {",
                    "		if edges[i].val < edges[i-1].val {",
                )
            ]
        },
    ),
    Mutation(
        "M04",
        "lowest band floor dropped to 0",
        "killed",
        {CURVE: [Edit("	if c.L0 < curveMinDuty {", "	if c.L0 < 0 {")]},
    ),
    Mutation(
        "M05",
        "duty 0 labelled off instead of auto",
        "killed",
        {LEVELS: [Edit('		return "auto"', '		return "off"')]},
    ),
    Mutation(
        "M06",
        "Render emits keys in lexicographic order",
        "killed",
        {
            CURVE: [
                Edit('	"os"\n	"strconv"', '	"os"\n	"sort"\n	"strconv"'),
                Edit(
                    RENDER_LOOP,
                    "	fields := curveFields(c)\n"
                    "	sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })\n"
                    "	for _, f := range fields {",
                ),
            ]
        },
    ),
    Mutation(
        "M07",
        "Render emits keys in reverse lexicographic order",
        "killed",
        {
            CURVE: [
                Edit('	"os"\n	"strconv"', '	"os"\n	"sort"\n	"strconv"'),
                Edit(
                    RENDER_LOOP,
                    "	fields := curveFields(c)\n"
                    "	sort.Slice(fields, func(i, j int) bool { return fields[i].key > fields[j].key })\n"
                    "	for _, f := range fields {",
                ),
            ]
        },
    ),
    Mutation(
        "M08",
        "a missing key is no longer an error",
        "killed",
        {CURVE: [Edit("	if len(missing) > 0 {", "	if false {")]},
    ),
    Mutation(
        "M09",
        "a line that is not an assignment is skipped",
        "killed",
        {
            CURVE: [
                Edit(JUNK_BLOCK, "		if eq < 0 {\n			continue\n		}")
            ]
        },
    ),
    Mutation(
        "M10a",
        "an unknown key is skipped",
        "killed",
        {
            CURVE: [
                Edit(
                    UNKNOWN_BLOCK,
                    "		field, ok := dst[key]\n		if !ok {\n			continue\n		}",
                )
            ]
        },
    ),
    Mutation(
        "M10b",
        "an unknown key is accepted as duty 0",
        "killed",
        {
            CURVE: [
                Edit(
                    UNKNOWN_BLOCK,
                    "		field, ok := dst[key]\n"
                    "		if !ok {\n"
                    "			var zero int\n"
                    "			field = &zero\n"
                    "		}",
                )
            ]
        },
    ),
    Mutation(
        "M11",
        "the value is neither shape-checked nor range-checked",
        "killed",
        {
            CURVE: [
                Edit("		if !curveDigits(val) {", "		if false {"),
                Edit(
                    ATOI_BLOCK,
                    "		v, err := strconv.Atoi(val)\n		if err != nil {\n			v = 0\n		}",
                ),
            ]
        },
    ),
    Mutation(
        "M12",
        "FAN_DEVICE derived from EC_ROOT unconditionally",
        "killed",
        {
            PLAT: [
                Edit(ACPI_CHECK, '	p.FANDevice = p.ECRoot + ".TFN1"\n' + ACPI_CHECK)
            ]
        },
    ),
    Mutation(
        "M12b",
        "FAN_DEVICE normalised to EC_ROOT only when it already has that prefix",
        "survived",
        {
            PLAT: [
                Edit(
                    ACPI_CHECK,
                    "	if strings.HasPrefix(p.FANDevice, p.ECRoot) {\n"
                    '		p.FANDevice = p.ECRoot + ".TFN1"\n'
                    "	}\n" + ACPI_CHECK,
                )
            ]
        },
    ),
    Mutation(
        "M13",
        "THERMAL_ZONE reduced to its basename",
        "killed",
        {
            PLAT: [
                Edit(
                    '	if !strings.HasPrefix(p.ThermalZone, "/") ||',
                    '	p.ThermalZone = p.ThermalZone[strings.LastIndex(p.ThermalZone, "/")+1:]\n'
                    '	if !strings.HasPrefix(p.ThermalZone, "/") ||',
                )
            ]
        },
    ),
    Mutation(
        "M14",
        "THERMAL_ZONE accepts a relative path or a bare name",
        "killed",
        {
            PLAT: [
                Edit(
                    '	if !strings.HasPrefix(p.ThermalZone, "/") ||',
                    '	if false && !strings.HasPrefix(p.ThermalZone, "/") ||',
                )
            ]
        },
    ),
    Mutation(
        "M15",
        "a missing platform key is no longer an error",
        "killed",
        {PLAT: [Edit("	if len(missing) > 0 {", "	if false {")]},
    ),
    Mutation(
        "M16",
        "the candidate list accepts a third column",
        "killed",
        {PLAT: [Edit("		if len(fields) != 2 {", "		if len(fields) < 2 {")]},
    ),
    Mutation(
        "M17",
        "the platform line is split on the last equals sign",
        "killed",
        {
            PLAT: [
                Edit(
                    "		eq := strings.IndexByte(line, '=')",
                    "		eq := strings.LastIndexByte(line, '=')",
                )
            ]
        },
    ),
    Mutation(
        "M18",
        "the curve parser stops trimming around the equals sign",
        "killed",
        {
            CURVE: [
                Edit(
                    "		key, val := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])",
                    "		key, val := line[:eq], line[eq+1:]",
                )
            ]
        },
    ),
    Mutation(
        "M19",
        "the candidate list stops stripping comments",
        "killed",
        {PLAT: [Edit(STRIP_COMMENT_BLOCK, "		line := raw")]},
    ),
    Mutation(
        "M20",
        "the write stops replacing the target by rename",
        "killed",
        {
            CURVE: [
                Edit(
                    "	if err := os.Rename(tmp, path); err != nil {",
                    "	if err := os.WriteFile(path, []byte(c.Render()), curveConfMode); err != nil {",
                )
            ]
        },
    ),
    Mutation(
        "M21",
        "the write stops refusing an invalid curve",
        "killed",
        {
            CURVE: [
                Edit(
                    "	if err := c.Validate(); err != nil {",
                    "	if err := c.Validate(); false && err != nil {",
                )
            ]
        },
    ),
    Mutation(
        "M22",
        "WantFor pairs each edge with the band below it",
        "killed",
        {
            CURVE: [
                Edit(
                    "	case tempC <= c.T1:\n		return c.L1",
                    "	case tempC <= c.T1:\n		return c.L0",
                )
            ]
        },
    ),
    Mutation(
        "M23",
        "WantFor band edges become exclusive",
        "killed",
        {CURVE: [Edit("	case tempC <= c.T1:", "	case tempC < c.T1:")]},
    ),
    Mutation(
        "M24",
        "the gap above the top band idles instead of going full speed",
        "killed",
        {CURVE: [Edit("\n	return c.E\n}", "\n	return c.L5\n}")]},
    ),
    # The two survivors the independent audit reported, re-run here so the
    # classification on record is one this harness reproduces rather than one
    # only the audit's own run supports.
    Mutation(
        "Z05",
        "the target is removed before the rename, so a reader can see it missing",
        "survived",
        {
            CURVE: [
                Edit(
                    "	if err := os.Rename(tmp, path); err != nil {",
                    "	os.Remove(path)\n	if err := os.Rename(tmp, path); err != nil {",
                )
            ]
        },
    ),
    # R-62. Each of the three must still bite, and each is mutated through an
    # import the package genuinely uses: a mutation that fails to compile proves
    # nothing about the rule it was meant to test.
    Mutation(
        "R62a",
        "a governed file imports syscall directly",
        "killed",
        {
            CURVE: [
                Edit(
                    '	"os"\n	"strconv"', '	"os"\n	"strconv"\n	"syscall"'
                ),
                Edit(
                    "\n	return c.E\n}",
                    "\n	return c.E\n}\n\nvar _ = syscall.Getpid\n",
                ),
            ]
        },
    ),
    Mutation(
        "R62b",
        "a governed file calls an x/sys/unix symbol outside the named list",
        "killed",
        {
            FANCTL: [
                Edit(
                    '	"golang.org/x/sys/unix"\n)',
                    '	"golang.org/x/sys/unix"\n)\n\n'
                    "var _ = func() error { return unix.Kill(0, 0) }",
                )
            ]
        },
    ),
    Mutation(
        "R62c",
        "an in-module sibling collector reaches os/exec, which attribution must refuse",
        "killed",
        {},
        {
            "agent/plusmut/plusmut.go": """package plusmut

import (
	"os/exec"

	"github.com/henrygd/beszel/agent/plus"
)

var _ = plus.Curve{}
var _ = exec.Command
"""
        },
    ),
]

# --------------------------------------------------------------------- runner


class Log:
    def __init__(self, path: str) -> None:
        self.fh = open(path, "w", encoding="utf-8", newline="\n")

    def __call__(self, line: str = "") -> None:
        # NUL and control bytes never survive: a log that a reviewer has to
        # open in a hex viewer to read is not evidence of anything.
        clean = "".join(
            ch for ch in line if ch == "\t" or 0x20 <= ord(ch) < 0x7F or ord(ch) > 0x9F
        )
        self.fh.write(clean + "\n")
        self.fh.flush()
        print(clean)


def run(cmd: list[str], cwd: str, env: dict[str, str]) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)


def digest(path: str) -> str:
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--log", required=True, help="where to write the report this run produces"
    )
    ap.add_argument(
        "--repo",
        default=None,
        help="checkout to mutate (default: derived from this file)",
    )
    ap.add_argument("--package", default="./agent/plus/", help="package under test")
    ap.add_argument(
        "--keep", action="store_true", help="leave the worktree in place for inspection"
    )
    ap.add_argument(
        "--preflight",
        action="store_true",
        help="only check that every patch applies and still builds, then exit",
    )
    args = ap.parse_args()

    repo = os.path.abspath(
        args.repo
        or os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..")
    )
    env = dict(os.environ)
    env.setdefault("GOPROXY", "https://goproxy.cn,direct")

    log = Log(args.log)
    log("mutation harness for agent/plus curve and platform readers")
    log(f"repo    {repo}")
    log(f"package {args.package}")
    log(f"started {time.strftime('%Y-%m-%dT%H:%M:%S%z')}")

    dirty = run(["git", "-C", repo, "status", "--porcelain"], repo, env)
    if dirty.stdout.strip():
        log("FATAL: the checkout under review is not clean; refusing to run.")
        return 2
    head = run(["git", "-C", repo, "rev-parse", "HEAD"], repo, env).stdout.strip()

    tmp = tempfile.mkdtemp(prefix="plus-mutate-")
    wt = os.path.join(tmp, "wt")
    add = run(["git", "-C", repo, "worktree", "add", "--detach", wt, "HEAD"], repo, env)
    if add.returncode != 0:
        log("FATAL: cannot create the worktree: " + add.stderr.strip()[:200])
        shutil.rmtree(tmp, ignore_errors=True)
        return 2
    log(f"worktree {wt} at {head[:12]}")

    pristine = {
        rel: digest(os.path.join(wt, rel))
        for rel in ("agent/plus/curve.go", PLAT, LEVELS, FANCTL)
    }
    created: list[str] = []

    def restore() -> None:
        for rel in created:
            os.remove(os.path.join(wt, rel))
        created.clear()
        run(["git", "-C", wt, "checkout", "--", "."], wt, env)
        for rel, want in pristine.items():
            got = digest(os.path.join(wt, rel))
            if got != want:
                raise RuntimeError(
                    f"restore of {rel} did not reproduce the pristine file"
                )

    def green() -> tuple[bool, str, list[str]]:
        proc = run(
            ["go", "test", "-tags", "testing", "-count=1", args.package], wt, env
        )
        fails = sorted(
            set(re.findall(r"^\s*--- FAIL:\s+(\S+)", proc.stdout + proc.stderr, re.M))
        )
        return proc.returncode == 0, proc.stdout + proc.stderr, fails

    def builds() -> tuple[bool, str]:
        proc = run(["go", "build", "./agent/..."], wt, env)
        return proc.returncode == 0, proc.stdout + proc.stderr

    def mutate(m: Mutation) -> None:
        for rel, edits in m.files.items():
            path = os.path.join(wt, rel)
            with open(path, encoding="utf-8") as fh:
                text = fh.read()
            for edit in edits:
                text = edit.apply(text, rel)
            with open(path, "w", encoding="utf-8", newline="") as fh:
                fh.write(text)
        for rel, body in m.creates.items():
            path = os.path.join(wt, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8", newline="") as fh:
                fh.write(body)
            created.append(rel)

    def preflight() -> bool:
        ok = True
        for m in MUTATIONS:
            restore()
            try:
                mutate(m)
            except PatchMiss as exc:
                log(f"PREFLIGHT FAIL {m.ident}: {exc}")
                ok = False
                continue
            good, out = builds()
            if not good:
                log(f"PREFLIGHT FAIL {m.ident}: the mutation does not compile")
                log("    " + "\n    ".join(out.strip().splitlines()[:3]))
                ok = False
                continue
            log(f"PREFLIGHT ok {m.ident}")
        restore()
        return ok

    log("")
    log("preflight: every patch is applied to a pristine tree and must still build")
    if not preflight():
        log("FATAL: preflight failed; no mutation was scored.")
        _teardown(wt, repo, tmp, args.keep, log, head)
        return 3
    log(f"preflight green; {len(MUTATIONS)} patches verified")

    log("")
    ok, out, _ = green()
    if not ok:
        log(
            "ABORT: baseline is not green on the pristine tree, so nothing here would mean anything."
        )
        log("\n".join(out.strip().splitlines()[-15:]))
        _teardown(wt, repo, tmp, args.keep, log, head)
        return 2
    log("baseline green; snapshotting originals")

    tally = {"killed": 0, "survived": 0, "compile": 0}
    mismatches: list[str] = []
    for m in MUTATIONS:
        restore()
        try:
            mutate(m)
        except PatchMiss as exc:
            log(f"[{m.ident}] FATAL {exc}")
            _teardown(wt, repo, tmp, args.keep, log, head)
            return 3
        good, bout = builds()
        if not good:
            tally["compile"] += 1
            log(f"[{m.ident}] compile (not scored) {m.claim}")
            log("    " + "\n    ".join(bout.strip().splitlines()[:3]))
            continue
        ok, out, fails = green()
        if "[build failed]" in out:
            tally["compile"] += 1
            log(f"[{m.ident}] compile (not scored) {m.claim}")
            continue
        verdict = "survived" if ok else "killed"
        tally[verdict] += 1
        log(f"[{m.ident}] {verdict} {m.claim}")
        for name in fails:
            log(f"    killed by: {name}")
        if verdict != m.expect:
            mismatches.append(m.ident)

    restore()
    log("")
    ok, out, _ = green()
    if not ok:
        log(
            "ABORT: the tree is not green again after restoring, so the results above are not trustworthy."
        )
        _teardown(wt, repo, tmp, args.keep, log, head)
        return 2
    log("restored: baseline green again")
    log("")
    log(
        f"killed={tally['killed']} survived={tally['survived']} compile_not_scored={tally['compile']}"
    )
    if mismatches:
        log(
            f"EXPECTATION MISMATCH on {', '.join(mismatches)} -- the declared expectation in the harness "
            f"disagrees with what the tree did. Treat the numbers above as unverified until that is explained."
        )
    else:
        log("every mutation behaved as the harness declared")

    _teardown(wt, repo, tmp, args.keep, log, head)
    return 4 if mismatches else 0


def _teardown(wt, repo, tmp, keep, log, head):
    log("")
    if keep:
        log(f"worktree kept at {wt}")
        return
    run(["git", "-C", repo, "worktree", "remove", "--force", wt], repo, os.environ)
    run(["git", "-C", repo, "worktree", "prune"], repo, os.environ)
    shutil.rmtree(tmp, ignore_errors=True)
    now = run(["git", "-C", repo, "rev-parse", "HEAD"], repo, os.environ).stdout.strip()
    clean = run(
        ["git", "-C", repo, "status", "--porcelain"], repo, os.environ
    ).stdout.strip()
    log(
        f"checkout under review: HEAD {now[:12]} (started from {head[:12]}, "
        f"{'unchanged' if now == head else 'MOVED'}), "
        f"working tree {'clean' if not clean else 'DIRTY -- ' + clean[:120]}"
    )


if __name__ == "__main__":
    sys.exit(main())
