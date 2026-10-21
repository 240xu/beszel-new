#!/usr/bin/env node
/**
 * Known-findings baseline for a linter.
 *
 * The problem this solves: every linter in this repository reports findings on the
 * tree as it stands today. `biome ci` reports 86 errors, `staticcheck` reports 14,
 * `golangci-lint` reports 237. A gate that says "the linter must report nothing"
 * is therefore red on a green tree, and a red gate that is red for a reason nobody
 * can act on trains everyone to ignore it -- which is how the repository ended up
 * with no type check, no linter and no bundle build in CI at all.
 *
 * The gate this implements is the one the repository already uses for its frontend
 * suite: `internal/site/scripts/known-failures.txt`. A finding that is already
 * recorded does not fail the build; a finding that is *new* does. The registry is
 * checked in, so the debt is visible and countable, and the gate turns green the
 * moment the debt is paid rather than staying red forever.
 *
 * Usage:
 *   node scripts/lint-baseline.mjs --tool biome      --report r.json --baseline b.txt
 *   node scripts/lint-baseline.mjs --tool staticcheck --report r.txt  --baseline b.txt
 *   node scripts/lint-baseline.mjs --tool golangci   --report r.json --baseline b.txt
 *   node scripts/lint-baseline.mjs --tool biome      --report r.json --baseline b.txt --update
 *
 * A baseline line is `<path>\t<rule>\t<count>`. Paths are relative to the repository
 * root. The count keeps a rule that fires ten times in one file from being recorded
 * ten times, and keeps the registry readable when it grows.
 */

import { readFileSync, writeFileSync } from "node:fs"

const args = process.argv.slice(2)
const opt = (name, fallback = undefined) => {
	const index = args.indexOf(`--${name}`)
	if (index === -1) return fallback
	const value = args[index + 1]
	return value === undefined || value.startsWith("--") ? true : value
}

const tool = opt("tool")
const reportPath = opt("report")
const baselinePath = opt("baseline")
const update = args.includes("--update")
const severity = opt("severity", "error")
// `file` records one entry per (path, rule). `linter` records one entry per rule
// across the whole tree, with the total.
//
// golangci-lint 2.14.0 is not deterministic at file granularity on this tree: five
// runs over the same sources all reported 237 findings with an identical per-linter
// breakdown, but the file each finding landed in moved between runs (147 to 153
// distinct (file, linter) pairs, cold cache and warm cache alike). A per-file
// baseline for that tool would therefore fail at random, which is worse than no
// gate: a gate that fails at random is a gate people turn off. The per-linter total
// is stable across every run observed, so that is what is recorded, and staticcheck
// -- which is deterministic -- carries the per-finding coverage.
const granularity = opt("granularity", tool === "golangci" ? "linter" : "file")

if (!tool || !reportPath || !baselinePath) {
	console.error("usage: lint-baseline.mjs --tool <biome|staticcheck|golangci> --report <file> --baseline <file> [--update]")
	process.exit(2)
}

/** Collapse a finding to the key the baseline records. */
const key = (path, rule) => `${path}\t${rule}`

const parseBiome = (text) => {
	const report = JSON.parse(text)
	const findings = new Map()
	for (const diagnostic of report.diagnostics ?? []) {
		if (severity !== "all" && diagnostic.severity !== severity) continue
		const path = diagnostic.location?.path?.file
		if (!path) continue
		const k = key(path, diagnostic.category)
		findings.set(k, (findings.get(k) ?? 0) + 1)
	}
	return findings
}

const parseStaticcheck = (text) => {
	const findings = new Map()
	// file:line:col: message (CHECK)
	for (const line of text.split("\n")) {
		const match = /^([^:]+):\d+:\d+:.*\(([A-Z]+\d+)\)\s*$/.exec(line.trim())
		if (!match) continue
		const k = key(match[1], match[2])
		findings.set(k, (findings.get(k) ?? 0) + 1)
	}
	return findings
}

const parseGolangci = (text) => {
	const report = JSON.parse(text)
	const findings = new Map()
	for (const issue of report.Issues ?? []) {
		const path = issue.Pos?.Filename
		if (!path) continue
		const k = granularity === "linter" ? issue.FromLinter : key(path, issue.FromLinter)
		findings.set(k, (findings.get(k) ?? 0) + 1)
	}
	return findings
}

const parsers = { biome: parseBiome, staticcheck: parseStaticcheck, golangci: parseGolangci }
const parser = parsers[tool]
if (!parser) {
	console.error(`lint-baseline: unknown tool ${tool}`)
	process.exit(2)
}

const report = parser(readFileSync(reportPath, "utf8"))

if (update) {
	const lines = [...report.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([k, n]) => `${k}\t${n}`)
	writeFileSync(baselinePath, lines.length ? `${lines.join("\n")}\n` : "")
	console.log(`lint-baseline: wrote ${lines.length} baseline entries to ${baselinePath}`)
	process.exit(0)
}

const baseline = new Map()
for (const line of readFileSync(baselinePath, "utf8").split("\n")) {
	if (!line.trim() || line.trim().startsWith("#")) continue
	const parts = line.split("\t")
	// A key may itself contain a tab (the file granularity joins path and rule with
	// one), so the count is the last field and the key is everything before it.
	const count = Number(parts[parts.length - 1])
	const k = parts.slice(0, parts.length - 1).join("\t")
	if (!k || !Number.isFinite(count)) continue
	baseline.set(k, count)
}

const added = []
for (const [k, count] of report) {
	const known = baseline.get(k) ?? 0
	if (count > known) added.push(`  ${k} (${count} now, ${known} recorded)`)
}

const resolved = []
for (const [k, count] of baseline) {
	const now = report.get(k) ?? 0
	if (now < count) resolved.push(`  ${k} (${now} now, ${count} recorded)`)
}

if (resolved.length) {
	console.log(`lint-baseline: ${resolved.length} recorded finding(s) no longer reported; refresh the baseline when you have paid them down:`)
	console.log(resolved.join("\n"))
}

if (added.length) {
	console.error(`lint-baseline: ${added.length} NEW finding(s) not in ${baselinePath}:`)
	console.error(added.join("\n"))
	process.exit(1)
}

console.log(`lint-baseline: ${tool} reports no finding outside ${baselinePath}`)
