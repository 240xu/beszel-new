import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"

/** The plus surface shipped 92 `t` calls with no catalog entry behind any of them:
 *  every locale fell back to the source string, which is English in all 36. The
 *  wiring was always right — `t` was always used — so nothing failed and nothing
 *  said so. Extraction is not the thing being asserted here: it is a build step
 *  that runs on every `npm run build` and would fail loudly if it broke. What is
 *  asserted is that the entries extraction *produced* carry a translation, which
 *  is the part that was silently zero. */

import { FAN_REJECT_CODES } from "@/lib/plus-refusals"

const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const LOCALE_DIR = "../locales"
const TAB = "../components/routes/system/plus-fan-tab.tsx"

const occurrences = (source: string, pattern: RegExp) => source.split("\n").filter((line) => pattern.test(line))

/** What a reader in this locale is shown, keyed by nothing: the set of strings the
 *  catalog carries. Read from the tracked `.po` rather than the compiled `.ts`,
 *  which is gitignored — a test that needs a build artifact fails on a fresh
 *  clone, which is the wrong way round — and because `lingui compile` copies
 *  `msgstr` into the bundle verbatim, so these are the same strings on screen. */
function catalog(locale: string): string[] {
	return entries(locale)
		.map((entry) => entry.msgstr)
		.filter((text) => text.length > 0)
}

/** The locale directories the config compiles. `ro`, `th` and `ug` exist on disk
 *  but are not in `lingui.config.ts`, so they are not runtime locales and are
 *  excluded rather than counted as gaps. */
function configured(): string[] {
	const config = read("../../lingui.config.ts")
	return [...config.matchAll(/^\t\t"([a-zA-Z-]+)",$/gm)].map((match) => match[1])
}

interface Entry {
	source: string[]
	msgid: string
	msgstr: string
}

/** A .po entry, parsed from the tracked catalog rather than from the compiled
 *  `.ts`, because the compiled artefact is gitignored: a test that read it would
 *  pass on a built tree and fail on a fresh clone, which is the wrong way round.
 *  Continuation lines are joined the way gettext specifies. */
function entries(locale: string): Entry[] {
	const po = read(`${LOCALE_DIR}/${locale}/${locale}.po`)
	return po.split(/\n\n+/).flatMap((block) => {
		const msgid = field(block, "msgid")
		const msgstr = field(block, "msgstr")
		if (msgid === undefined || msgstr === undefined) return []
		const source = [...block.matchAll(/^#: (src\/.*)$/gm)].map((match) => match[1].trim())
		return [{ source, msgid, msgstr }]
	})
}

function field(block: string, name: string): string | undefined {
	const direct = block.match(new RegExp(`^${name} "(.*)"$`, "m"))
	if (direct) return direct[1]
	if (!block.includes(`\n${name} ""`)) return undefined
	return [...block.matchAll(/^"((?:[^"\\]|\\.)*)"$/gm)].map((match) => match[1]).join("")
}

/** Every message the plus files own. The `#:` reference is what extraction writes
 *  and what Crowdin round-trips, so a string that moved files stops counting and
 *  one that was added starts, without a list here to keep in step with the code. */
function plusEntries(locale: string): Entry[] {
	return entries(locale).filter((entry) =>
		entry.source.some((file) => /^src\/(lib\/plus-|components\/routes\/system\/plus-)/.test(file))
	)
}

test("the plus strings reached the catalog at all", () => {
	// Zero here means extraction has not run since the plus code landed, which is
	// the state this file exists to make impossible to ship.
	expect(plusEntries("en").length).toBeGreaterThan(50)
})

test("every plus string has a non-empty source entry", () => {
	for (const entry of plusEntries("en")) {
		expect(`${entry.msgid}: ${entry.msgstr}`).not.toBe(`${entry.msgid}: `)
	}
})

test("zh carries a translation for every plus string", () => {
	const en = new Map(plusEntries("en").map((entry) => [entry.msgid, entry.msgstr]))
	const zh = new Map(plusEntries("zh").map((entry) => [entry.msgid, entry.msgstr]))
	const untranslated: string[] = []
	for (const [msgid, source] of en) {
		const target = zh.get(msgid)
		if (!target) {
			untranslated.push(`${msgid} (absent)`)
			continue
		}
		if (target === source && !KEEP_IN_ENGLISH.has(source)) untranslated.push(source)
	}
	expect(untranslated).toEqual([])
})

/** Terms a Chinese reader needs in the original, because a translation would name
 *  something they cannot look up: the product name, the kernel's own names for the
 *  tuning policy and the P-state, and Wi-Fi. The curve field names (`emerg`, `hyst`
 *  and the rest) are not here because they reach the catalog through the
 *  `CURVE_CARRIED` list rather than as literals of their own. */
const KEEP_IN_ENGLISH = new Set(["beszel-new", "EPP", "WiFi", "P-State", "HWP Dynamic Boost"])

test("every configured locale has a catalog of its own", () => {
	// The gap this reports is the point. The locales we do not own fall back to
	// English, which is correct behaviour but was invisible; naming it is what
	// turns "nobody will find out" into a statement that changes when it stops
	// being true.
	for (const locale of configured()) {
		expect(`${locale}: ${entries(locale).length}`).not.toBe(`${locale}: 0`)
	}
})

test("zh is translated rather than merely regenerated", () => {
	// A re-extract writes `msgstr ""` into zh for every new string, so a catalog
	// that exists says nothing. What distinguishes the two is the ratio: a run
	// that filled zh in and a run that only regenerated it differ here and nowhere
	// else that a count can see.
	const en = plusEntries("en").length
	const zh = plusEntries("zh").length
	expect(en).toBeGreaterThan(0)
	expect(zh).toBe(en)
	const filled = plusEntries("zh").filter((entry) => entry.msgstr.length > 0).length
	expect(filled / zh).toBeGreaterThan(0.9)
})
/** The guard test that follows is not about counts. `plus-fan-view.ts` used to own
 *  a second, English copy of every sentence `plus-fan-tab.tsx` renders, and the
 *  suite pinned that copy instead of the one on screen: mutating the component
 *  left every test green. The copy is gone, so the pins below are the ones that
 *  survive, and they ask the catalog what a reader is actually shown. */

/** The `en` catalog, which is what an untranslated build renders. */
const rendered = catalog("en")

/** The text a reader is shown for the sentence containing this fragment. The point
 *  of asking the catalog rather than the source file is that it sits downstream of
 *  extraction: a sentence that drifts in the component cannot keep a stale
 *  assertion green, because the assertion asks what the catalog will render. */
function sentence(needle: string): string {
	// An empty needle would match the first catalog entry and hand back an
	// unrelated sentence, so a blanked entry would read as a passing lookup. The
	// catalog is the last place this can be caught, so it is caught here.
	if (needle.trim().length === 0) throw new Error("looked up an empty sentence; a rendered string is missing")
	for (const text of rendered) {
		if (text.includes(needle)) return text
	}
	throw new Error(`nothing in the en catalog renders a sentence containing ${JSON.stringify(needle)}`)
}

/** Two sentences that must never be confusable, checked on the strings a reader
 *  is shown rather than on the component's table entries. This is the assertion
 *  that replaces the English copy `plus-fan-view.ts` used to carry. */
test("the two refusals that send an operator after different problems stay apart when rendered", () => {
	const hot = sentence("emergency edge in the curve")
	const sensor = sentence("package temperature could not be read")
	expect(hot).not.toBe(sensor)
	expect(sensor).not.toMatch(/too hot|at or above|emergency edge/i)
	expect(hot).not.toMatch(/check the sensor/i)
	// Each names the other's absent remedy: cooling a host with a dead sensor is
	// the mistake this pair exists to prevent.
	expect(hot).toMatch(/let it cool/i)
	expect(sensor).toMatch(/check the sensor/i)
})

test("a write nothing is running is never rendered with wording that says it is running", () => {
	const pending = sentence("the curve daemon has not picked it up")
	expect(pending).not.toMatch(/in force|running now|生效|已生效/i)
	// The reverse, or the pair proves nothing.
	expect(sentence("the fan is running this now")).toMatch(/in force|running/i)
})

test("the refusal shown when no reason is named asserts nothing about the fan", () => {
	// A server that names no reason may have refused before it read the request or
	// failed after it acted; the wire cannot tell those apart and neither can this
	// panel, so the sentence may not either.
	const bare = sentence("without naming a reason")
	expect(bare).not.toMatch(/reached the fan|nothing was written|left alone|in force/i)
	expect(bare).not.toBe(sentence("for a reason this build does not know"))
})

test("every refusal the server can raise reaches a rendered sentence of its own", () => {
	// The key is the discriminator on the wire; the sentence is what the operator
	// reads. Each code is paired with the sentence the component looks up under it,
	// then both are resolved through the catalog, so a code that lost its entry —
	// or gained a neighbour's — fails here rather than on a screen.
	const source = read(TAB)
	const sentences = FAN_REJECT_CODES.map((code) => {
		const entry = source.match(new RegExp(`\\t${code}: t\`([^\`]*)\``))?.[1]
		expect(`${code}: ${entry ?? "no entry in the panel"}`).not.toBe(`${code}: no entry in the panel`)
		// Spelled out rather than folded into the lookup, so a sentence emptied out
		// is reported as the key that lost it and not as an unrelated match.
		expect(`${code}: ${entry}`).not.toBe(`${code}: `)
		return sentence(entry as string)
	})
	expect(new Set(sentences).size).toBe(FAN_REJECT_CODES.length)
})

test("every outcome the panel can report reaches a rendered sentence of its own", () => {
	const source = read(TAB)
	const kinds = ["applied", "pending_restart", "daemon_running", "daemon_stopped"]
	const sentences = kinds.map((kind) => {
		const entry = source.match(new RegExp(`\\t${kind}: t\`([^\`]*)\``))?.[1]
		expect(`${kind}: ${entry ?? "no entry in the panel"}`).not.toBe(`${kind}: no entry in the panel`)
		expect(`${kind}: ${entry}`).not.toBe(`${kind}: `)
		return sentence(entry as string)
	})
	expect(new Set(sentences).size).toBe(kinds.length)
})

test("the fan tab asks for the CPU package by context, so it cannot claim the updates table's", () => {
	// `Package` names a software package in package-updates-table.tsx, where it
	// reads 套件, and the CPU package here, where it reads 封装. One msgid cannot
	// carry both, so the tab disambiguates and the catalog holds two entries. Drop
	// the context and the two collide again -- but only at the next extract, which
	// is exactly the kind of drift nothing would report, so the call site is pinned.
	const source = read(TAB)
	expect(occurrences(source, /t\(\{ message: "Package", context: "CPU package" \}\)/).length).toBe(1)
	expect(occurrences(source, /t`Package`/)).toEqual([])
	// Both readings exist as separate catalog entries, which is what makes the
	// context load-bearing rather than decorative.
	const zh = read(`${LOCALE_DIR}/zh/zh.po`)
	expect(zh).toContain('msgctxt "CPU package"')
	expect(zh).toContain('msgstr "封装"')
	expect(zh).toContain('msgstr "套件"')
})
