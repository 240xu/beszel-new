#!/usr/bin/env node
/**
 * npm audit policy gate.
 *
 * `npm audit` on this repository reports 15 findings. Four of them reach the
 * *production* dependency set (`npm audit --omit=dev`), and three of those four are
 * build-time packages that the browser bundle does not contain:
 *
 *   browserslist (2 x high), js-yaml (4 x high) -- both are pulled in transitively
 *   by `@lingui/macro`, which sits in `dependencies` but is imported by nothing:
 *   the build compiles the macro with `@lingui/swc-plugin`, and `@lingui/macro`
 *   itself resolves to a babel plugin the project does not install. A `dist/` grep
 *   finds 0 occurrences of browserslist, js-yaml or @babel/core in the built
 *   bundle. They are in the production *install* set and in no production *shipped*
 *   set.
 *
 * The one production finding that is real is `valibot` (moderate,
 * GHSA-5qjj-4xww-7phc): it is imported by three components and is present in
 * `dist/assets/index-*.js`. It is below the policy's threshold, and the fix is the
 * version bump the upstream project already made.
 *
 * The policy this implements:
 *
 *   1. The production dependency set (`npm audit --omit=dev`) must carry no advisory
 *      of severity high or critical, except the ones listed in
 *      `scripts/npm-audit-allowlist.json`, each with the reason it is exempt.
 *   2. The development set is reported and never gates. A build-time advisory does
 *      not reach a browser.
 *
 * The allowlist is keyed by advisory URL, not by package name, so a *new* advisory
 * in an allowlisted package still fails the gate.
 *
 * Usage:
 *   node scripts/audit-policy.mjs [--allowlist scripts/npm-audit-allowlist.json]
 */

import { execFileSync } from "node:child_process"
import { readFileSync } from "node:fs"

const args = process.argv.slice(2)
const opt = (name, fallback) => {
	const index = args.indexOf(`--${name}`)
	return index === -1 ? fallback : args[index + 1]
}

const allowlistPath = opt("allowlist", "scripts/npm-audit-allowlist.json")
const registry = opt("registry", "https://registry.npmjs.org/")
const siteDir = opt("site-dir", "internal/site")

const allowlist = JSON.parse(readFileSync(allowlistPath, "utf8"))
const exempt = new Set(Object.keys(allowlist))

const audit = (extraArgs) => {
	try {
		const stdout = execFileSync(
			"npm",
			["audit", "--json", `--registry=${registry}`, ...extraArgs],
			{ cwd: siteDir, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }
		)
		return JSON.parse(stdout)
	} catch (error) {
		// npm audit exits non-zero whenever it finds anything, which is the normal
		// case here. The report is on stdout either way.
		if (error.stdout) {
			try {
				return JSON.parse(error.stdout)
			} catch {
				// fall through to the error below
			}
		}
		throw error
	}
}

const collect = (report) => {
	const findings = []
	for (const [name, entry] of Object.entries(report.vulnerabilities ?? {})) {
		for (const via of entry.via ?? []) {
			if (typeof via === "string") continue
			findings.push({ package: name, severity: entry.severity, url: via.url, title: via.title })
		}
	}
	return findings
}

const prod = collect(audit(["--omit=dev"]))
const all = collect(audit([]))

const blocking = prod.filter(
	(finding) => (finding.severity === "high" || finding.severity === "critical") && !exempt.has(finding.url)
)

const exemptProd = prod.filter((finding) => exempt.has(finding.url))
const devOnly = all.filter(
	(finding) => !prod.some((p) => p.url === finding.url) && (finding.severity === "high" || finding.severity === "critical")
)

console.log(`audit-policy: production set carries ${prod.length} advisory(s), development set adds ${devOnly.length} more`)
if (exemptProd.length) {
	console.log(`audit-policy: ${exemptProd.length} production advisory(s) are on the documented allowlist:`)
	for (const finding of exemptProd) {
		console.log(`  ${finding.package} ${finding.severity} ${finding.url}`)
		console.log(`    ${allowlist[finding.url]}`)
	}
}
if (devOnly.length) {
	console.log(`audit-policy: ${devOnly.length} development-only advisory(s) do not gate (build-time only):`)
	for (const finding of devOnly) {
		console.log(`  ${finding.package} ${finding.severity} ${finding.url}`)
	}
}

if (blocking.length) {
	console.error(`audit-policy: ${blocking.length} production advisory(s) are not on the allowlist:`)
	for (const finding of blocking) {
		console.error(`  ${finding.package} ${finding.severity} ${finding.url} -- ${finding.title}`)
	}
	process.exit(1)
}

console.log("audit-policy: no production advisory above the threshold")
