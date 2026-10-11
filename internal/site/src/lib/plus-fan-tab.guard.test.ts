import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"
import { FAN_REJECT_CODES } from "@/lib/plus-refusals"

/** `plus-fan-tab.tsx` cannot be imported here: `@lingui/core/macro` resolves to a
 *  babel plugin that needs `babel-plugin-macros`, which this project does not
 *  install. Everything this panel decides was pushed down into `plus-fan-view.ts`
 *  and is tested there against real behaviour. What is left in the component is
 *  wiring, and wiring is pinned against the source text of the file that owns it.
 *
 *  Say plainly what this is: each rule below proves the component still *contains*
 *  something. None of them proves a string reaches a screen. */
const read = async (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const TAB = "../components/routes/system/plus-fan-tab.tsx"

/** The body of the regress line, which is the only place allowed to decide what
 *  an operator is told about a hand-set duty. */
const regressArm = (source: string) =>
	source.slice(source.indexOf("function RegressLine"), source.indexOf("function CurveEditor"))

const occurrences = (source: string, pattern: RegExp) =>
	source
		.split("\n")
		.map((line, index) => ({ line: index + 1, text: line }))
		.filter(({ text }) => pattern.test(text))
		.map(({ line, text }) => `${line}: ${text.trim()}`)

/** Wording that asserts the change is already cooling the machine. */
const CLAIMS_IN_FORCE = /saved|applied|in force|running now|生效|已保存|已生效/i

/** The two outcome tables are the only place a claim that something is running
 *  may be written down, and only for the applied arm. */
const stripOutcomeTables = (source: string) =>
	source.replace(/const OUTCOME_TEXT[^=]*= \{[^}]*\}/, "").replace(/const OUTCOME_BADGE[^=]*= \{[^}]*\}/, "")

const tableBody = (source: string, table: string): string =>
	source.match(new RegExp(`const ${table}[^=]*= \\{([^}]*)\\}`))?.[1] ?? ""

const tableEntry = (source: string, table: string, key: string): string => {
	const block = source.match(new RegExp(`const ${table}[^=]*= \\{([^}]*)\\}`))?.[1] ?? ""
	const entry = block.match(new RegExp(`${key}:\\s*(t\`[^\`]*\`|"[^"]*")`))?.[1] ?? ""
	return entry.replace(/^t`|`$/g, "")
}

test("the badge and the sentence are looked up by outcome kind, so no arm can reach another's", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /OUTCOME_BADGE\[outcome\.kind\]/)).not.toEqual([])
	expect(occurrences(source, /OUTCOME_TEXT\[outcome\.kind\]/)).not.toEqual([])
})

test("a write no daemon is running is never described with wording that means it is running", async () => {
	const source = await read(TAB)
	expect(tableEntry(source, "OUTCOME_BADGE", "pending_restart")).not.toMatch(CLAIMS_IN_FORCE)
	expect(tableEntry(source, "OUTCOME_TEXT", "pending_restart")).not.toMatch(CLAIMS_IN_FORCE)
	// The reverse: the applied arm does say so, or the pair proves nothing.
	expect(tableEntry(source, "OUTCOME_BADGE", "applied")).toMatch(CLAIMS_IN_FORCE)
})

test("a pending write leaves the operator an outlet, and the outlet takes the confirm step", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /outcome\.needsRestart\s*&&/)).not.toEqual([])
	expect(occurrences(source, /Restart the curve daemon/)).not.toEqual([])
	expect(occurrences(source, /onRestart=\{\(\) => setArmed\(modeIntent\("auto"\)\)\}/)).not.toEqual([])
})

test("a daemon change that succeeded and one that failed both reach a sentence", async () => {
	// The banner is written from one settled value, so an outlet that was taken and
	// worked leaves "Awaiting restart" nowhere on the screen.
	const source = await read(TAB)
	for (const table of ["OUTCOME_TEXT", "OUTCOME_BADGE"]) {
		expect(tableEntry(source, table, "daemon_running")).not.toBe("")
		expect(tableEntry(source, table, "daemon_stopped")).not.toBe("")
	}
	expect(tableEntry(source, "OUTCOME_TEXT", "daemon_running")).not.toBe(
		tableEntry(source, "OUTCOME_TEXT", "daemon_stopped")
	)
	expect(tableEntry(source, "OUTCOME_TEXT", "daemon_stopped")).not.toMatch(CLAIMS_IN_FORCE)
	expect(tableBody(await read(TAB), "OUTCOME_BADGE")).toContain("pending_restart")
})

test("the banner is never written behind a test on a result that may be missing", async () => {
	// A gate like `if (result)` is how a completed restart leaves the previous
	// banner claiming a restart is still owed: nothing arrives to replace it.
	const source = await read(TAB)
	expect(occurrences(source, /if \(result\)|result\s*&&|result\?\./)).toEqual([])
	expect(occurrences(source, /fanWriteSettled\(/)).not.toEqual([])
	expect(occurrences(source, /setOutcome\(settled\.outcome\)/)).not.toEqual([])
	expect(occurrences(source, /fanWriteRefused\(/)).not.toEqual([])
})

test("a hot package and an unreadable sensor do not share a sentence", async () => {
	const source = await read(TAB)
	const hot = tableEntry(source, "REFUSAL_TEXT", "package_too_hot")
	const sensor = tableEntry(source, "REFUSAL_TEXT", "temperature_unknown")
	expect(hot).not.toBe(sensor)
	expect(sensor).not.toMatch(/too hot|at or above|emergency edge|let it cool/i)
	expect(hot).not.toMatch(/check the sensor/i)
	expect(hot).not.toBe("")
	// Both now name a remedy, and the remedies are not each other's.
	expect(hot).toMatch(/cool|emerg/i)
	expect(sensor).toMatch(/sensor/i)
})

test("every refusal the server can raise has a sentence of its own", async () => {
	const source = await read(TAB)
	const keys = new Set([...source.matchAll(/^\t([a-z_]+):\s*t`/gm)].map((m) => m[1]))
	for (const code of FAN_REJECT_CODES) {
		expect(keys.has(code)).toBe(true)
	}
	expect(keys.has("unclassified")).toBe(true)
	expect(keys.has("unrecognised")).toBe(true)
})

test("the sentence shown when no reason is named asserts nothing about the fan", async () => {
	const source = await read(TAB)
	const bare = tableEntry(source, "REFUSAL_TEXT", "unclassified")
	expect(bare).not.toBe("")
	expect(bare).not.toMatch(/reached the fan|nothing was written|left alone|in force/i)
	expect(bare).not.toBe(tableEntry(source, "REFUSAL_TEXT", "unrecognised"))
})

test("a refusal is classified once, by code, and never by the status it arrived on", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /fanWriteRefused\(|fanRefusal\(/)).not.toEqual([])
	expect(occurrences(source, /err\.status|view\.status|\.status\s*[<>=]/)).toEqual([])
})

test("a countdown exists only where a deadline produced one", async () => {
	const source = await read(TAB)
	// The raw field is the panel's to misread; the decision belongs to the module.
	expect(occurrences(source, /regress_at/)).toEqual([])
	expect(occurrences(source, /fanRegressView\(/)).not.toEqual([])
	expect(occurrences(source, /regress\.kind === "counting_down"/)).not.toEqual([])
	// Nothing on record, and a deadline lost to an agent restart, are the same
	// bytes -- and a duty set before that restart is still in force, because it
	// lives in the firmware and not in the agent. The arm that rendered nothing
	// therefore left a possibly-held duty invisible, which is the whole failure.
	expect(occurrences(regressArm(source), /return null/)).toEqual([])
	expect(occurrences(source, /cannot see it/)).not.toEqual([])
	// A regress with no deadline is not a thing this panel cannot act on: writing
	// the duty again starts a fresh countdown, which guard.go schedules on every
	// accepted level write.
	expect(occurrences(source, /no deadline/)).not.toEqual([])
	expect(occurrences(source, /cannot say when it ends/)).toEqual([])
	expect(occurrences(source, /starts a fresh countdown/)).not.toEqual([])
})

test("no fan write is dispatched from this panel by hand", async () => {
	// The confirm flag is asserted against the request that goes out in
	// plus-fan-view.test.ts. What has to hold here is that the panel has no second
	// way to reach a write route, where a flag could be chosen at the call site.
	const source = await read(TAB)
	expect(occurrences(source, /\bsetFanLevel\(|\bsetFanMode\(|\bsaveCurve\(/)).toEqual([])
	expect(occurrences(source, /\bfetch\(/)).toEqual([])
	expect(occurrences(source, /,\s*false\b/)).toEqual([])
	expect(occurrences(source, /confirm:\s*false/)).toEqual([])
})

test("the duty shown as current is the one the server published", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /fanLevelLabel\(state\)/).length).toBeGreaterThanOrEqual(2)
	// A second implementation of the label would let a retuned firmware table
	// and this panel disagree about what the fan is doing.
	expect(occurrences(source, /\blevelLabel\(/)).toEqual([])
	expect(occurrences(source, /(?:const|function)\s+\w*label/i)).toEqual([])
})

test("the panel reads the outcome, not the raw write result", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /fanWriteSettled\(|fanWriteOutcome\(/)).not.toEqual([])
	expect(occurrences(source, /\.applied\b/)).toEqual([])
	expect(occurrences(source, /needs_pfand_restart/)).toEqual([])
})

test("the armed panel always has a way out", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /onCancel=\{\(\) => setArmed\(null\)\}/).length).toBeGreaterThanOrEqual(2)
	expect(occurrences(source, /onCancel:\s*\(\) => void/)).not.toEqual([])
})

test("no wording that claims a write is running exists outside the two outcome tables", async () => {
	// Without this, a ternary can hand the in-force badge to the pending arm
	// while both tables still read correctly.
	expect(occurrences(stripOutcomeTables(await read(TAB)), CLAIMS_IN_FORCE)).toEqual([])
})

test("no two refusals share a sentence in this panel either", async () => {
	const values = [...tableBody(await read(TAB), "REFUSAL_TEXT").matchAll(/t`([^`]*)`/g)].map((m) => m[1])
	expect(values).toHaveLength(14)
	expect(new Set(values).size).toBe(values.length)
})

test("the panel never names a write route, so a confirm flag cannot be chosen at a call site", async () => {
	expect(occurrences(await read(TAB), /\bsetFanLevel\b|\bsetFanMode\b|\bsaveCurve\b/)).toEqual([])
})
