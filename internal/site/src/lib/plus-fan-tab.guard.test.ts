import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"
import { FAN_REJECT_CODES } from "@/lib/plus-refusals"

/** `plus-fan-tab.tsx` cannot be imported here: `@lingui/core/macro` resolves to a babel
 *  plugin needing `babel-plugin-macros`, which this project does not install. What this
 *  panel decides was pushed into `plus-fan-view.ts` and is tested there; what is left is
 *  wiring, pinned against the source text of the file owning it. Each rule proves the
 *  component *contains* something -- never that a string reaches a screen. */
const read = async (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const TAB = "../components/routes/system/plus-fan-tab.tsx"

/** The regress line's body: the only place allowed to decide what an operator is told
 *  about a hand-set duty. Split on the kind test, not the prose, so a wording change
 *  cannot slide a claim into a neighbour's slice. */
const regressArm = (source: string) =>
	source.slice(source.indexOf("function RegressLine"), source.indexOf("function CurveEditor"))

const regressArms = (source: string): { kind: string; text: string }[] => {
	const body = regressArm(source)
	const marks = [...body.matchAll(/if \(regress\.kind === "([a-z_]+)"/g)]
	return marks.map((mark, index) => ({
		kind: mark[1] ?? "",
		text: body.slice(mark.index ?? 0, marks[index + 1]?.index ?? body.length),
	}))
}

const occurrences = (source: string, pattern: RegExp) =>
	source
		.split("\n")
		.map((line, index) => ({ line: index + 1, text: line }))
		.filter(({ text }) => pattern.test(text))
		.map(({ line, text }) => `${line}: ${text.trim()}`)

/** Wording asserting this panel's change is already cooling the machine. `in force`
 *  counts only where a predicate credits a write for the state, or heads a bare value:
 *  the bare substring also matched "it stays in force", a true sentence about a duty the
 *  firmware already holds, which promises no return and claims no write of ours. */
const CLAIMS_IN_FORCE =
	/\bsaved\b|\bapplied\b|running\s+(?:this\s+)?now\b|in progress|生效|已保存|已生效|\b(?:is|are|was|were|now)\s+in\s+force\b|(?:^|[({?,\s])(?:t`|["'`])\s*in\s+force\b/i

/** Promises the fan comes back by itself: only an arm a live deadline reached may say it. */
const PROMISES_A_RETURN = /countdown|reverts?\s|comes?\s+back|starts?\s+a\s+fresh/i

/** The two outcome tables: the only place a claim that something is running may be
 *  written down, and only for the applied arm. */
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
	// The reverse, or the pair above proves nothing. The sentence carries the claim;
	// the badge is a bare value, which this rule reads off a predicate rather than off
	// the head of a literal, so it is pinned by existing instead of by the pattern.
	expect(tableEntry(source, "OUTCOME_TEXT", "applied")).toMatch(CLAIMS_IN_FORCE)
	expect(tableEntry(source, "OUTCOME_BADGE", "applied")).not.toBe("")
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
	const keys = new Set([...source.matchAll(/^\t+([a-z_]+):\s*t`/gm)].map((m) => m[1]))
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
	// Nothing on record, and a deadline lost to an agent restart, are the same bytes --
	// and a duty set before that restart is still in force, because it lives in the
	// firmware and not in the agent. The arm that rendered nothing therefore left a
	// possibly-held duty invisible, which is the whole failure.
	expect(occurrences(regressArm(source), /return null/)).toEqual([])
	expect(occurrences(source, /cannot see it/)).not.toEqual([])
	expect(occurrences(source, /no deadline/)).not.toEqual([])
	expect(occurrences(source, /cannot say when it ends/)).toEqual([])
	// Only an arm a live deadline reached may promise the fan returns by itself. This
	// replaces a pin on the phrase `a7680f0` deleted, and is not a request to put it back:
	// it promised a countdown ending in a return to the curve, but the regress now
	// declines to write, so it promised a revert no code performs -- zero was never an
	// automatic handover, and "automatic" is the physical error in it. The invariant
	// outlives the sentence; the sentence does not come back.
	const arms = regressArms(source)
	const kinds = arms.map((arm) => arm.kind)
	expect(kinds).toContain("counting_down")
	expect(kinds).toContain("no_deadline")
	const undated = arms.filter((arm) => arm.kind !== "counting_down")
	expect(occurrences(undated.map((arm) => arm.text).join("\n"), PROMISES_A_RETURN)).toEqual([])
})

test("no fan write is dispatched from this panel by hand", async () => {
	// The confirm flag is asserted against the request in plus-fan-view.test.ts. What
	// holds here is that no second route out exists, where a flag could be chosen.
	const source = await read(TAB)
	expect(occurrences(source, /\bsetFanLevel\(|\bsetFanMode\(|\bsaveCurve\(/)).toEqual([])
	expect(occurrences(source, /\bfetch\(/)).toEqual([])
	expect(occurrences(source, /,\s*false\b/)).toEqual([])
	expect(occurrences(source, /confirm:\s*false/)).toEqual([])
})

test("the duty shown as current is the one the server published", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /fanLevelLabel\(state\)/).length).toBeGreaterThanOrEqual(2)
	// A second label implementation would let a retuned firmware table disagree with this panel.
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
	// Without this a ternary can hand the in-force badge to the pending arm unread.
	expect(occurrences(stripOutcomeTables(await read(TAB)), CLAIMS_IN_FORCE)).toEqual([])
})

test("no two refusals share a sentence in this panel either", async () => {
	const values = [...tableBody(await read(TAB), "REFUSAL_TEXT").matchAll(/t`([^`]*)`/g)].map((m) => m[1])
	expect(values).toHaveLength(15)
	expect(new Set(values).size).toBe(values.length)
})

test("the panel never names a write route, so a confirm flag cannot be chosen at a call site", async () => {
	expect(occurrences(await read(TAB), /\bsetFanLevel\b|\bsetFanMode\b|\bsaveCurve\b/)).toEqual([])
})

test("the thermal floor the agent reports renders its own badge", async () => {
	// The hold arrives on the fan state as its own boolean, so the badge is not
	// bound to a write outcome and can surface while no write is in the air.
	expect(occurrences(await read(TAB), /state\.thermal_floor_armed\s*===\s*true/)).not.toEqual([])
	expect(occurrences(await read(TAB), /t`Thermal guard holding`/)).not.toEqual([])
})

test("the countdown the module computed reaches the arm that renders it", async () => {
	// The counting arm alone may carry a countdown: a live deadline produced one,
	// and the pin above keeps the word out of every other arm.
	const source = await read(TAB)
	expect(occurrences(source, /\{regress\.countdown\}/)).not.toEqual([])
})

test("the offline and refusal lines reach a reader as alerts, not as silent text", async () => {
	// A failed write is announced: the offline line, a duty refusal and a curve
	// refusal are the three places a screen reader has to hear the failure.
	const source = await read(TAB)
	expect(occurrences(source, /role="alert"/).length).toBeGreaterThanOrEqual(3)
})

test("a duty that could not be read says so where the reading is shown", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /!state\.level_read\s*&&/)).not.toEqual([])
	expect(occurrences(source, /could not be read; the fan's present duty is unknown/)).not.toEqual([])
})

// An "unreadable answer" banner says the write may be in force. A later successful
// read replaces that guess with knowledge, so the banner has to go without waiting
// for another write. It also has to key on the real success state ("ready"), which
// this asserts alongside the effect's presence.
test("the unreadable banner is cleared by a successful read", async () => {
	const source = await read(TAB)
	expect(occurrences(source, /status === "ready"\) setUnreadable\(false\)/)).toHaveLength(2)
	expect(occurrences(source, /status === "ok"\) setUnreadable/)).toEqual([])
})
