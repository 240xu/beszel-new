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

/** The Chinese catalogs a reader is actually served, taken from `getLocale` in
 *  `i18n.ts` rather than from a list maintained here: every `zh-*` it does not
 *  name explicitly becomes `zh-CN`, so a Simplified reader gets that catalog and
 *  the `zh-TW` / `zh-Hant` / `zh-MO` readers get `zh`. A hole in either is a hole
 *  a reader sees, so neither is exempt -- and asserting only `zh` is what let
 *  `zh-CN` sit empty and green at the same time. */
const CHINESE_LOCALES = ["zh", "zh-CN"] as const

test("the Simplified Chinese catalog is the one a Simplified reader is served", () => {
	// The premise the loop below rests on, pinned against the source rather than
	// against this file: if `i18n.ts` ever stops routing `zh-*` to `zh-CN`, the
	// assertion that `zh-CN` is filled stops describing the product and starts
	// describing a catalog nobody loads. Read from the file, not from an import,
	// because an import would be this test's own claim either way.
	const i18n = read("../lib/i18n.ts")
	expect(i18n).toContain("zhVariantMap: Record<string, string>")
	expect(i18n).toContain('return zhVariantMap[locale] || "zh-CN"')
	// And the catalog those readers land in is one the build actually compiles.
	const config = read("../../lingui.config.ts")
	expect([...config.matchAll(/^\t\t"([a-zA-Z-]+)",$/gm)].map((m) => m[1])).toContain("zh-CN")
})

for (const locale of CHINESE_LOCALES) {
	test(`${locale} carries a translation for every plus string`, () => {
		const en = new Map(plusEntries("en").map((entry) => [entry.msgid, entry.msgstr]))
		const target = new Map(plusEntries(locale).map((entry) => [entry.msgid, entry.msgstr]))
		const untranslated: string[] = []
		for (const [msgid, source] of en) {
			const value = target.get(msgid)
			if (!value) {
				untranslated.push(`${msgid} (absent)`)
				continue
			}
			if (value === source && !KEEP_IN_ENGLISH.has(source)) untranslated.push(source)
		}
		expect(untranslated).toEqual([])
	})
}

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

for (const locale of CHINESE_LOCALES) {
	test(`${locale} is translated rather than merely regenerated`, () => {
		// A re-extract writes `msgstr ""` into the catalog for every new string, so
		// a catalog that exists says nothing. What distinguishes the two is the
		// ratio: a run that filled the catalog in and a run that only regenerated
		// it differ here and nowhere else that a count can see.
		const en = plusEntries("en").length
		const target = plusEntries(locale).length
		expect(en).toBeGreaterThan(0)
		expect(target).toBe(en)
		const filled = plusEntries(locale).filter((entry) => entry.msgstr.length > 0).length
		expect(filled / target).toBeGreaterThan(0.9)
	})
}
/** The prompts an operator reads before writing to the fan, and the wording this
 *  catalog must not be allowed to drift into.
 *
 *  The loop above proves a translation *exists*. That is not enough for this set:
 *  a reader acts on these sentences, so a fluent mistranslation does more damage
 *  than the blank it replaced -- an operator who believes the firmware will hand
 *  the fan back, or who reads a latched-open path as a live one, changes the
 *  machine on the strength of a sentence. So these are asserted on content.
 *
 *  The msgids are literals rather than imports: the lookup that follows reads the
 *  catalog, so a blanked entry fails here, and a renamed button fails the
 *  cross-reference below. A newly added safety prompt is covered generically by
 *  the coverage loop; these are the ones worth naming. */
const SAFETY_PROMPTS = [
	// The destructive write, and the half of it that is the whole warning.
	"Confirm to write this to the fan control path. There is no undo from this panel.",
	// The _FSL 0 sentence: the firmware reads zero as zero percent, which is why
	// nothing writes it back and why handing the fan over is the operator's job.
	"Duty written to the firmware. Nothing writes it back afterwards: the firmware reads zero as zero percent, so returning the fan to its curve is yours to do.",
	// "It stays in force" is a claim about firmware retention, not about a policy
	// having taken effect.
	"A hand-set duty is on record with no deadline on file. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
	"A hand-set duty is on record. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
	"No hand-set duty is on record. One set before an agent restarted would still be driving the fan, and this panel cannot see it.",
	"In force",
	"In force — the fan is running this now.",
	// fanapi.ErrBreakerOpen: the path is latched OPEN and refuses every caller
	// until an agent restart. Reading it as "control is on" sends an operator to
	// retry a write that cannot succeed.
	"Fan control is latched open after repeated failures; nothing was written.",
	// The pair that must not converge; cooling a host with a dead sensor is the
	// mistake they exist to prevent.
	"The package is at or above the emergency edge in the curve, so no duty was written. Let it cool, or raise emerg in the curve, before writing again.",
	"The package temperature could not be read, so no duty was written and the reading cannot be trusted. Check the sensor and the zone it names rather than cooling the machine.",
	"The server refused this without naming a reason, so nothing can be concluded about the fan.",
	// The button the "stays in force" prompts send the operator to by name.
	"Return to the curve",
	// The guard badge is an affirmative safety claim of its own: it tells the
	// operator the fan is being held while the daemon is gone. A rendering that
	// drifts into hand-back or "automatic" wording would claim a policy the
	// guard exists to deny.
	"Thermal guard holding",
] as const

/** One locale's rendering of a msgid, straight from the catalog. `undefined` when
 *  the entry is absent, which is what the presence assertions below check for --
 *  an absent key and a blank value are different failures and are reported as
 *  different things. */
function translated(locale: string, msgid: string): string | undefined {
	return entries(locale).find((entry) => entry.msgid === msgid)?.msgstr
}

for (const locale of CHINESE_LOCALES) {
	test(`${locale} carries every operator safety prompt, and carries it non-empty`, () => {
		const absent: string[] = []
		for (const msgid of SAFETY_PROMPTS) {
			const value = translated(locale, msgid)
			// Spelled out rather than folded into one aggregate, so a blanked prompt is
			// reported as the sentence that lost it and not as a count.
			expect(`${msgid}: ${value ?? "absent"}`).not.toBe(`${msgid}: `)
			if (value === undefined || value.length === 0) absent.push(msgid)
		}
		expect(absent).toEqual([])
	})

	test(`${locale} never tells an operator the firmware will hand the fan back for them`, () => {
		// The rule the _FSL 0 finding turns on: writing 0 is 0%, not a hand-back, so
		// no prompt may imply that the firmware, the agent, or some automatic mode
		// will do it. `自动` / `自動` is the word that does the damage in Chinese, and
		// the source words are matched so the check survives an English leak.
		// Stated for every prompt above plus the curve-mode button, because a
		// hand-back word on any one of them is the hazard.
		const claims: string[] = []
		for (const msgid of [...SAFETY_PROMPTS, "Return to the curve", "Take manual control"]) {
			const value = translated(locale, msgid)
			if (value === undefined || value.length === 0) continue
			if (/自动|自動|\bautomatic\b|\bauto\b/i.test(value)) claims.push(`${msgid} -> ${value}`)
		}
		expect(claims).toEqual([])
	})

	test(`${locale} does not render a latched-open write path as one that is on`, () => {
		// Pins the defect this catalog actually shipped: the refusal was rendered as
		// "latched to the ON state", which tells an operator the path is live when
		// fanapi has latched it open and will refuse every write until restart.
		const value = translated(locale, "Fan control is latched open after repeated failures; nothing was written.")
		expect(`${value}`).not.toBe("undefined")
		expect(`${value}`).not.toMatch(/开启|開啟|已打开|已打開|on state|開啟狀態/)
		// And the reverse, or the assertion proves nothing: the sentence does have to
		// say the write did not happen.
		expect(`${value}`).toMatch(/没有写入|沒有寫入/)
	})

	test(`${locale} names the same button the prompts that reference it use`, () => {
		// Two of the prompts tell the operator which button to press by name. A
		// translator who renders the button one way and the reference another way
		// produces a prompt pointing at a control that does not exist on screen,
		// and nothing else in the suite would notice: both strings are present and
		// both are non-empty.
		const button = translated(locale, "Return to the curve")
		expect(`${button}`).not.toBe("undefined")
		for (const msgid of [
			"A hand-set duty is on record with no deadline on file. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
			"A hand-set duty is on record. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
		]) {
			const prompt = translated(locale, msgid)
			expect(`${prompt}`).not.toBe("undefined")
			expect(prompt as string).toContain(button as string)
		}
	})

	test(`${locale} keeps the two temperature refusals apart on screen`, () => {
		// The same pair the English assertions below keep apart, checked where a
		// Chinese reader actually reads it. Cooling a host whose sensor is dead is
		// the mistake, so the hot refusal must not name the sensor and the sensor
		// refusal must not tell them to cool it.
		const hot = translated(locale, SAFETY_PROMPTS[8] as string)
		const sensor = translated(locale, SAFETY_PROMPTS[9] as string)
		expect(hot).not.toBe(sensor)
		// Each names the other's absent remedy, in both directions. Asserting only
		// that the sensor refusal names the sensor left the gap a mutation slipped
		// through: appending "please cool the machine first" to it kept every
		// assertion above satisfied while telling an operator to do the one thing
		// this pair exists to stop them doing.
		expect(`${hot}`).not.toMatch(/检查传感器|檢查感測器|check the sensor/i)
		expect(`${sensor}`).toMatch(/检查传感器|檢查感測器|check the sensor/i)
		// Cooling advice belongs to the hot refusal and to it only. Matched as an
		// instruction (please/first/should + cool) rather than as the bare word,
		// because the correct sensor refusal has to be able to say "rather than
		// cool the machine" in order to rule the advice out.
		expect(`${hot}`).toMatch(/请先降温|請先降溫|let it cool/i)
		expect(`${sensor}`).not.toMatch(
			/(请先|请|應先|应先|应该|應該|需要)[^，。]*降温|cooling the machine|cool the machine/i
		)
	})
	/** The clause each safety prompt must actually SAY, as opposed to the words it
	 *  must avoid. A forbidden list is the wrong instrument and this suite has the
	 *  scar to prove it: the hand-back check below only ever looked for
	 *  自动 / 自動 / auto, and seven synonymous renderings claiming the firmware will
	 *  return the fan all passed it -- 自行归还, 无需人工干预, 已启用, 冷却,
	 *  守护进程会接管, 到达时限后即恢复, 已确认并写入. A gate that only forbids a
	 *  vocabulary is one reword away from green again.
	 *
	 *  So each prompt here names the clause that carries its meaning, and dropping the
	 *  clause fails instead of a choice of words. The markers are the ones the shipped
	 *  catalog already uses -- zh-CN reads 需要由你自己来操作, 本代理不会写入,
	 *  返回曲线, 保持中 -- so nothing here asks a translator for a vocabulary the
	 *  product has never used. */
	const REQUIRED_CLAUSES: ReadonlyArray<readonly [string, RegExp, string]> = [
		[
			"Duty written to the firmware. Nothing writes it back afterwards: the firmware reads zero as zero percent, so returning the fan to its curve is yours to do.",
			/你自己|由你(来)?操作|你的(操作|事)/,
			"the operator, not the firmware, owns the hand-back",
		],
		[
			"A hand-set duty is on record with no deadline on file. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
			/本代理不会|不会写入任何/,
			"the sentence that nothing will hand the fan back is the warning itself",
		],
		[
			"A hand-set duty is on record. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.",
			/本代理不会|不会写入任何/,
			"the sentence that nothing will hand the fan back is the warning itself",
		],
		[
			"The package is at or above the emergency edge in the curve, so no duty was written. Let it cool, or raise emerg in the curve, before writing again.",
			/没有写入|沒有寫入/,
			"the write did not happen, and an operator who thinks it did will not cool the machine",
		],
		[
			"The package temperature could not be read, so no duty was written and the reading cannot be trusted. Check the sensor and the zone it names rather than cooling the machine.",
			/没有写入|沒有寫入/,
			"the write did not happen, and the reading is not to be trusted",
		],
		[
			"In force",
			/保持|仍在/,
			"the duty is retained by firmware; 生效 would say a policy took effect instead",
		],
		[
			"In force — the fan is running this now.",
			/保持|仍在/,
			"the duty is retained by firmware; 生效 would say a policy took effect instead",
		],
		[
			"Thermal guard holding",
			/持|守护|托底/,
			"the badge says the guard is holding the fan, not that some mode took over",
		],
	]

	/** Renderings that carry the wrong claim. Kept beside the required clauses rather
	 *  than folded into the hand-back word list, because each of these is the safe
	 *  sentence rendered as an unsafe one rather than a stray danger word. */
	const FORBIDDEN_CLAUSES: ReadonlyArray<readonly [string, RegExp, string]> = [
		["In force", /生效/, "已生效 reads as a policy having taken effect; nothing is applying this duty"],
		["In force — the fan is running this now.", /生效/, "已生效 reads as a policy having taken effect"],
		[
			"Duty written to the firmware. Nothing writes it back afterwards: the firmware reads zero as zero percent, so returning the fan to its curve is yours to do.",
			/自行归还|会自动|會自動/,
			"the hand-back belongs to the operator",
		],
	]

	test(`${locale} keeps the clause each safety prompt exists for`, () => {
		const missing: string[] = []
		for (const [msgid, marker, why] of REQUIRED_CLAUSES) {
			const value = translated(locale, msgid)
			if (value === undefined || value.length === 0) {
				missing.push(`${msgid.slice(0, 40)}... (absent) -- ${why}`)
				continue
			}
			if (!marker.test(value)) missing.push(`${msgid.slice(0, 40)}... -> ${value} -- ${why}`)
		}
		expect(missing).toEqual([])
	})

	test(`${locale} does not render a safety prompt as its unsafe twin`, () => {
		const wrong: string[] = []
		for (const [msgid, marker, why] of FORBIDDEN_CLAUSES) {
			const value = translated(locale, msgid)
			if (value === undefined || value.length === 0) continue
			if (marker.test(value)) wrong.push(`${msgid.slice(0, 40)}... -> ${value} -- ${why}`)
		}
		expect(wrong).toEqual([])
	})


}

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

// zh-HK is the one reader-visible catalog that is not complete. It must borrow
// the missing entries from zh rather than fall through to English, or a Hong Kong
// reader gets English in exactly the panel this project added.
test("zh-HK falls back to zh for untranslated entries", () => {
	const hk = entries("zh-HK")
	const zh = new Map(entries("zh").map((e) => [e.msgid, e.msgstr]))
	const missing = hk.filter((e) => e.msgstr === "" && e.msgid !== "")
	expect(missing.length).toBeGreaterThan(0)
	for (const entry of missing) {
		expect(zh.get(entry.msgid) ?? "").not.toBe("")
	}
})
