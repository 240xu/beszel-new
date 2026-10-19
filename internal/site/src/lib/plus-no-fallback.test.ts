import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"
import { STATIC_LABEL_KEYS } from "./plus-metrics"

/** The sheet cannot be imported here: `@lingui/core/macro` resolves to a babel
 *  plugin that needs `babel-plugin-macros`, which this project does not
 *  install, and `t` is a compile-time macro rather than a function. Rather than
 *  assert against a copy of the module, the render decisions that a pure module
 *  cannot own are pinned against the source text of the file that owns them.
 *  Every rule below is a claim about what the component must still contain. */
const read = async (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const METRICS = "../lib/plus-metrics.ts"
const SHEET = "../components/routes/system/plus-sheet.tsx"

const occurrences = (source: string, pattern: RegExp) =>
	source
		.split("\n")
		.map((line, index) => ({ line: index + 1, text: line }))
		.filter(({ text }) => pattern.test(text))
		.map(({ line, text }) => `${line}: ${text.trim()}`)

/**
 * A field the agent never reported and a field it reported as zero are the same
 * on the wire for anything carrying omitempty, so `x ?? 0` does not fail loudly:
 * it renders a confident zero forever. No such fallback belongs in these files.
 */
test("no plus reading falls back to zero", async () => {
	for (const source of [METRICS, SHEET]) {
		const text = await read(source)
		expect(occurrences(text, /\?\?\s*0\b/)).toEqual([])
		expect(occurrences(text, /\|\|\s*0\b/)).toEqual([])
	}
})

/** The firmware scales the duty byte by a fixed constant to publish noise, so a
 *  noise column restates the duty and invites reading as a measurement. */
test("the sheet never renders the firmware noise column", async () => {
	expect(occurrences(await read(SHEET), /noise/i)).toEqual([])
})

/** plus rides along on the stats record. The retired collection read is not a
 *  data source this sheet may fall back to. */
test("the sheet does not read a plus collection", async () => {
	expect(occurrences(await read(SHEET), /getPlusStats|plus_stats/)).toEqual([])
})

/** The qualifier spans are what tell a reader that a number is a derived or an
 *  aged one. Deleting the block that draws them leaves every other test green:
 *  the notes are still produced by the module, they are just never shown. */
test("the sheet draws the qualifiers the rows carry", async () => {
	const text = await read(SHEET)
	expect(occurrences(text, /row\.notes/)).not.toEqual([])
	expect(occurrences(text, /noteText\(note\)/)).not.toEqual([])
})

/** The one table header whose text changes with a flag. Dropping it removes a
 *  caveat about the firmware rather than a piece of decoration. The speed header
 *  that used to sit beside it is gone because the column is: see the guard test
 *  below, which pins the absence rather than trusting the reader of this file. */
test("the firmware fan table keeps the nominal-power caveat in its header", async () => {
	const text = await read(SHEET)
	expect(occurrences(text, /FAN_POWER_IS_NOMINAL \? t`Power \(firmware nominal\)`/)).not.toEqual([])
})

/** The reverse direction of the same decision. A column re-added for a platform
 *  that does report a tachometer would be a new claim, and every test that asserts
 *  a row is absent passes just as happily when the row is back, so the rendering
 *  itself is pinned: no cell can read the withheld field. */
test("neither the fan card nor the fan table renders a speed", async () => {
	const text = await read(SHEET)
	expect(occurrences(text, /row\.rpm|\.rpm_est\b/)).toEqual([])
	expect(occurrences(text, /<TableHead>[^<]*\bSpeed\b/)).toEqual([])
	expect(occurrences(text, /FAN_STEP_RPM_IS_ESTIMATE/)).toEqual([])
})

/** A label is looked up by key so it can be translated. A component that went
 *  back to rendering `row.label` would still be correct in English. */
test("row labels are resolved through the translation map", async () => {
	const text = await read(SHEET)
	expect(occurrences(text, /const LABELS: Record<StaticLabelKey, string>/)).not.toEqual([])
	expect(occurrences(text, /LABELS\[row\.key as StaticLabelKey\] \?\? row\.label/)).not.toEqual([])
})

/** A sheet whose only content is the collapsed card must not draw that card
 *  over nothing, which is what every other chart on the page avoids. */
test("the collapsed card is withheld when the preview is empty", async () => {
	const text = await read(SHEET)
	expect(occurrences(text, /!plusAvailable\(records\) \|\| rows\.length === 0/)).not.toEqual([])
})

/** Every key the map declares has to be one the module can emit, or the map has
 *  grown an entry for a row that no longer exists and the label silently stops
 *  being used. */
test("the translation map covers exactly the declared static keys", async () => {
	const text = await read(SHEET)
	const declared = [...text.matchAll(/^\t+"([a-z]+\.[a-z0-9_]+)": t`/gm)].map((match) => match[1])
	expect(declared.length).toBeGreaterThan(0)
	expect([...declared].sort()).toEqual([...STATIC_LABEL_KEYS].sort())
})

/** `created` is the only record clock available to the retry row. The qualifier is
 *  named by the module and spoken by the sheet, and this asserts both halves: a
 *  sheet that drew its own span would restate a value the module already decided,
 *  and a module that spelled the words out would put English in a locale. */
test("the retry span is named by the module and worded by the sheet", async () => {
	const metrics = await read(METRICS)
	expect(occurrences(metrics, /wifiRetrySpanNote\(/)).not.toEqual([])
	expect(occurrences(metrics, /over \$\{/)).toEqual([])
	const sheet = await read(SHEET)
	expect(occurrences(sheet, /t`over \$\{formatSpan\(note\.seconds\)\}`/)).not.toEqual([])
})

/** Every qualifier the module can name has a way to be said. A new one without a
 *  branch here would render nothing rather than an error, which is exactly the
 *  silent failure a reader cannot report. */
test("every qualifier the module names is worded by the sheet", async () => {
	const metrics = await read(METRICS)
	const sheet = await read(SHEET)
	const kinds = [...metrics.matchAll(/kind: "([a-z_]+)"/g)].map((m) => m[1])
	expect(new Set(kinds).size).toBeGreaterThan(0)
	for (const kind of kinds) {
		expect(occurrences(sheet, new RegExp(`case "${kind}":`))).not.toEqual([])
	}
})
