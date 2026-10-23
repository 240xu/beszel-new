import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"
import { STATIC_LABEL_KEYS } from "@/lib/plus-metrics"

/** `plus-sheet.tsx` cannot be imported here: `@lingui/core/macro` resolves to a babel
 *  plugin that needs `babel-plugin-macros`, which this project does not install. What is
 *  pinned below is the shape that lets a language switch reach the labels: a table built
 *  at import resolves `t` once, while a table built in the component is resolved again on
 *  every render. Only a source read can see the difference from this side of the macro. */
const SHEET = "../components/routes/system/plus-sheet.tsx"

const read = async () => readFileSync(fileURLToPath(new URL(SHEET, import.meta.url)), "utf8")

const occurrences = (source: string, pattern: RegExp) =>
	source
		.split("\n")
		.map((line, index) => ({ line: index + 1, text: line }))
		.filter(({ text }) => pattern.test(text))
		.map(({ line, text }) => `${line}: ${text.trim()}`)

test("the label table is built at render time, so a language switch refreshes it", async () => {
	const source = await read()
	// The map and the lookup that reads it must both sit in a function body, and the
	// component that draws the rows must call that function. A module-level table
	// would leave every label in the locale that was active at import.
	expect(occurrences(source, /^const LABELS: Record<StaticLabelKey, string>/)).toEqual([])
	expect(occurrences(source, /^function labelCopy\(\): Record<StaticLabelKey, string> \{/)).not.toEqual([])
	expect(occurrences(source, /^\tconst LABELS: Record<StaticLabelKey, string> = \{/)).not.toEqual([])
	const build = source.indexOf("function labelCopy()")
	const draw = source.indexOf("const LABELS = labelCopy()")
	expect(build).toBeGreaterThan(-1)
	expect(draw).toBeGreaterThan(build)
})

test("every label stays a literal t call, so extraction still sees it", async () => {
	const source = await read()
	const body = source.slice(source.indexOf("function labelCopy"), source.indexOf("function MetricGrid"))
	const declared = [...body.matchAll(/^\t+"([a-z]+\.[a-z0-9_]+)": t`/gm)].map((match) => match[1])
	expect([...declared].sort()).toEqual([...STATIC_LABEL_KEYS].sort())
})

// Measured on the real compiled stylesheet: the old uniform grid overflowed its
// container at 280px and 320px. The fix is the grouped definition-list layout, and
// the part that must not regress is that a metric label wraps rather than being
// clipped. A clipped label hides which reading it belongs to, which is worse than
// the extra line it costs.
test("metric labels wrap instead of truncating", async () => {
	const source = await read()
	const metricValue = source.slice(source.indexOf("function MetricValue"), source.indexOf("function MetricGroup"))
	expect(occurrences(metricValue, /truncate/)).toEqual([])
	expect(occurrences(metricValue, /leading-snug/)).not.toEqual([])
})

// The three data shapes must stay distinct: a device list is not a scalar grid,
// and NVMe health must not be folded back into the lifetime counters.
test("the sheet keeps one layout per data shape", async () => {
	const source = await read()
	expect(occurrences(source, /function DeviceList/)).not.toEqual([])
	expect(occurrences(source, /function NvmeBody/)).not.toEqual([])
	expect(occurrences(source, /<DeviceList rows=\{dptf\} \/>/)).not.toEqual([])
	expect(occurrences(source, /<DeviceList rows=\{netall\} \/>/)).not.toEqual([])
	expect(occurrences(source, /<NvmeBody rows=\{nvme\} \/>/)).not.toEqual([])
	expect(occurrences(source, /NVME_HEALTH_KEYS/)).not.toEqual([])
})
