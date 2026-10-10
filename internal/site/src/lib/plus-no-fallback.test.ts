import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"

const read = async (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const SOURCES = ["../lib/plus-metrics.ts", "../components/routes/system/plus-sheet.tsx"]

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
	for (const source of SOURCES) {
		const text = await read(source)
		expect(occurrences(text, /\?\?\s*0\b/)).toEqual([])
		expect(occurrences(text, /\|\|\s*0\b/)).toEqual([])
	}
})

/** The firmware scales the duty byte by a fixed constant to publish noise, so a
 *  noise column restates the duty and invites reading as a measurement. */
test("the sheet never renders the firmware noise column", async () => {
	expect(occurrences(await read(SOURCES[1]), /noise/i)).toEqual([])
})

/** plus rides along on the stats record. The retired collection read is not a
 *  data source this sheet may fall back to. */
test("the sheet does not read a plus collection", async () => {
	expect(occurrences(await read(SOURCES[1]), /getPlusStats|plus_stats/)).toEqual([])
})
