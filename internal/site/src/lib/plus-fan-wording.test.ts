import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { expect, test } from "bun:test"
import { cpufreqRows, dptfRows, fanRows, fanStepRows, netRows, nvmeRows, previewRows, wifiRows } from "@/lib/plus-metrics"
import { fanLevelLabel } from "@/lib/plus-fan-view"
import type { FanState, PlusStats, SystemStatsRecord } from "@/types"

/** Two rulings, and nothing was watching either of them.
 *
 * `_FSL 0` is the firmware's zero-percent performance state. It is not `auto`,
 * and there is no tachometer behind this platform's speed, so the panel shows
 * neither. `plus-fan-duties.drift.test.ts` pins the TypeScript duty list to the
 * Go one -- and two lists that agree can both be wrong, so drift says nothing
 * about whether the shape they agree on is allowed. That is the gap.
 *
 * Every rule below reads a literal, a catalog block or the string a function
 * returns. None of them imports a constant and then asks the constant about
 * itself, which is the shape of assertion that cannot fail. Each reader also
 * proves it found something, so a reader that quietly stopped matching reports
 * a failure rather than passing on nothing.
 */

const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8")

const VIEW = "./plus-fan-view.ts"
const TAB = "../components/routes/system/plus-fan-tab.tsx"
const SHEET = "../components/routes/system/plus-sheet.tsx"
const TYPES = "../types.d.ts"
const LEVELS = "../../../../agent/plus/levels.go"
const PLUS_GO = "../../../../agent/plus/plus.go"
const LOCALE_DIR = "../locales"

const occurrences = (source: string, pattern: RegExp) => source.split("\n").filter((line) => pattern.test(line))

/** A speed, as a screen writes one. Matching the noun and not just the unit is
 *  deliberate: the panel once spent a sentence explaining that "the fan speed
 *  this platform reports is derived from the duty byte", which named the reading
 *  without writing `rpm` anywhere, and a unit-only rule let it straight back in.
 *  A sentence may not name the quantity at all -- saying it is withheld is still
 *  putting it in front of an operator who never asked. */
const SPEED =
	/\brpm\b|\br\/min\b|\bu\/min\b|fan speeds?|rotational speed|转速|轉速|回转|回転|ファン速度|팬\s*속도|회전|об\/мин|скорость\s+вентилятора/i

const ZERO = /\b0\b|\bzero\b/i
const MODE = /\bauto(?:matic|atically)?\b|自动|自動/i

/** The two words in one sentence is the whole rule. Banning `auto` on its own
 *  would forbid the curve daemon's own mode, which is a real value off the wire
 *  and is rendered verbatim rather than translated. */
const callsZeroAMode = (text: string) => ZERO.test(text) && MODE.test(text)

const configured = (): string[] => [...read("../../lingui.config.ts").matchAll(/^\t\t"([a-zA-Z-]+)",$/gm)].map((m) => m[1])

/** The locale directories the build compiles. `ro`, `th` and `ug` sit on disk but
 *  are not in the config and are not runtime locales, so the config is the filter
 *  and a locale that is configured but absent from disk is a failure rather than
 *  a silently skipped scan. */

/** One catalog split into gettext blocks. A block is scanned whole rather than
 *  through a parsed `msgid`, because a long string may be split over
 *  continuation lines and a reader that only read the first would drop exactly
 *  the text it was sent to find. The `#:` reference extraction wrote is what
 *  marks a block as the plus panel's rather than somebody else's -- upstream has
 *  its own `System fan speeds (RPM)` chart that must not be mistaken for this. */
function plusBlocks(locale: string): string[] {
	const po = read(`${LOCALE_DIR}/${locale}/${locale}.po`)
	return po.split(/\n\n+/).filter((block) => /^#: src\/(lib\/plus-|components\/routes\/system\/plus-)/m.test(block))
}

const offenders = (blocks: string[], match: (text: string) => boolean, label: (text: string) => string): string[] =>
	blocks.filter(match).map(label)

/** The translatable strings the components declare, read out of the source and
 *  not out of the catalog: a `t` that nobody has extracted yet is a string the
 *  next build puts on a screen, and until then no catalog can be holding it. */
const declaredMessages = (): string[] =>
	[read(TAB), read(SHEET)].flatMap((source) => [...source.matchAll(/(?:^|[^\w])t`([^`]*)`/g)].map((m) => m[1]))

const RECORD = [
	{
		system: "s",
		created: 1,
		stats: {
			plus: {
				dptf: { zones: { ACPI: 47.5 } },
				cpufreq: { cores: [{ idx: 0, freq_mhz: 2294 }], governor: "powersave", epp: "balance_performance" },
				netall: { eno1: { rx_bytes: 9007199254740991, tx_bytes: 0, speed_mbit: 1000 } },
				nvme: { model: "CFQV8-1", temp_c: 41, percentage_used_pct: 3 },
				wifi: { signal_dbm: -47, tx_mbit: 433, freq_mhz: 5180, retry: 1146, iface: "wlp0s20f3" },
				fan: { set_level_pct: 0, ec76_pct: 30, rpm_est: 1180, rpm_est_is_estimate: true, gov: "curve", temp_c: 44 },
				fan_steps: [{ percent: 65, rpm_est: 2450, noise_mdb: 32, power_mw: 4100 }],
			} as unknown as PlusStats,
		},
	} as unknown as SystemStatsRecord,
]

function fanState(over: Partial<FanState> = {}): FanState {
	return {
		pfand_active: false,
		level_read: true,
		level: 70,
		level_label: "70%",
		pkg_temp_read: true,
		pkg_temp_c: 50,
		regress_pending: false,
		...over,
	} as FanState
}

/** The duty list as the file declares it. Importing `FAN_DUTIES` and asking
 *  whether it holds a zero is the assertion that cannot fail -- both halves come
 *  from the same line -- and reading the literal back puts the question somewhere
 *  the export cannot reach, so it keeps holding if the constant is renamed,
 *  moved, aliased or split. */
function declaredDuties(): number[] {
	const source = read(VIEW)
	const literal = source.match(/FAN_DUTIES\s*(?::[^=\n]+)?=\s*\[([^\]]*)\]/)
	if (!literal) throw new Error("the duty list literal is not where this reader looks for it")
	return [...literal[1].matchAll(/-?\d+/g)].map((match) => Number(match[0]))
}

test("this reader sees the whole duty list, so the rule below is about all of it", () => {
	const source = read(VIEW)
	const duties = declaredDuties()
	expect(source).toMatch(/export const FAN_DUTIES\s*(?::[^=\n]+)?=\s*\[/)
	expect(duties.length).toBeGreaterThan(0)
	// Rebuilt and looked for in the file. A reader that matched only part of the
	// array would still report "no zero here", and a short list would still
	// round-trip, so the parse has to be shown to cover the array it claims.
	const rebuilt = `[${duties.join(", ")}]`
	expect(source.replace(/\s+/g, " ")).toContain(rebuilt)
	expect(read(VIEW)).toContain(" as const")
})

test("the duty list offers no zero and nothing below it", () => {
	const duties = declaredDuties()
	expect(duties.filter((duty) => duty === 0)).toEqual([])
	expect(duties.filter((duty) => duty < 0)).toEqual([])
	expect(duties.filter((duty) => duty > 100)).toEqual([])
	expect(duties.every(Number.isInteger)).toBe(true)
})

test("a duty is written as a percentage, so a zero would read 0% rather than a mode", () => {
	// The other half of the previous rule. The list being clean does not stop a
	// label that special-cases a zero into a mode name, and that is exactly where
	// `_FSL 0` would start reading as the handover it is not.
	const source = read(TAB)
	const item = source.match(/<SelectItem[^>]*>([\s\S]*?)<\/SelectItem>/)?.[1] ?? ""
	expect(item).not.toBe("")
	expect(item).toMatch(/`\$\{duty\}%`/)
	expect(item).not.toMatch(/auto|自动|\?/)
	expect(occurrences(source, /FAN_DUTIES\.map\(/)).not.toEqual([])
})

test("a zero duty off the wire is read as zero percent", () => {
	// Behaviour rather than a constant: the state arrives from the server and the
	// string that leaves is what the operator sees. `0%` is the only answer `_FSL
	// 0` may have, and `LevelLabel` in agent/plus/levels.go already refuses to
	// call it anything else.
	expect(fanLevelLabel(fanState({ level: 0, level_label: "0%" }))).toBe("0%")
	expect(fanLevelLabel(fanState({ level: 0, level_label: "0%" }))).not.toMatch(MODE)
	// A level nobody could read is still unknown rather than a zero.
	const unreadable = fanLevelLabel(fanState({ level_read: false, level: null, level_label: undefined }))
	expect(unreadable).not.toMatch(ZERO)
	expect(unreadable).not.toMatch(MODE)
})

test("the zero rule is about the two words together, not about the word auto", () => {
	// Without this the guard could be satisfied by deleting the curve daemon's own
	// mode, which is a value the server sends and the panel renders verbatim.
	expect(callsZeroAMode("0 (auto)")).toBe(true)
	expect(callsZeroAMode("duty 0 is 自动")).toBe(true)
	expect(callsZeroAMode("Automatic")).toBe(false)
	expect(callsZeroAMode("Return to the curve")).toBe(false)
	expect(callsZeroAMode("the firmware reads zero as zero percent")).toBe(false)
})

test("no string the plus panel declares names a fan speed", () => {
	const messages = declaredMessages()
	expect(messages.length).toBeGreaterThan(20)
	expect(offenders(messages, (m) => SPEED.test(m), (m) => m)).toEqual([])
})

/** Every configured locale, each one proved non-empty before it is scanned, so a
 *  reader that stopped matching a catalog fails instead of skipping it. */
function everyLocale(): Array<{ locale: string; blocks: string[] }> {
	return configured().map((locale) => {
		const blocks = plusBlocks(locale)
		expect(`${locale}: ${blocks.length}`).not.toBe(`${locale}: 0`)
		return { locale, blocks }
	})
}

const msgidOf = (block: string) => block.match(/^msgid "(.*)"$/m)?.[1] ?? block.slice(0, 80)

test("no rendered sentence names a fan speed in any locale the build compiles", () => {
	const found: string[] = []
	for (const { locale, blocks } of everyLocale()) {
		found.push(...offenders(blocks, (block) => SPEED.test(block), (block) => `${locale}: ${msgidOf(block)}`))
	}
	expect(found).toEqual([])
})

test("no rendered sentence calls a zero an auto, in any locale", () => {
	const found: string[] = []
	for (const { locale, blocks } of everyLocale()) {
		found.push(...offenders(blocks, callsZeroAMode, (block) => `${locale}: ${msgidOf(block)}`))
	}
	expect(found).toEqual([])
})

test("no row the sheet draws carries a speed in its label or its value", () => {
	// `plus-metrics.ts` emits labels that bypass the catalog whenever a key is
	// missing from the sheet's map, so the builders are asked directly. They are
	// pure, which is the only reason this can be called at all -- the components
	// themselves need `@lingui/core/macro` and cannot be imported.
	const metrics = [
		previewRows(RECORD, 1),
		fanRows(RECORD, 1),
		dptfRows(RECORD),
		cpufreqRows(RECORD),
		nvmeRows(RECORD),
		wifiRows(RECORD),
		netRows(RECORD),
	].flat()
	const steps = fanStepRows(RECORD)
	expect(metrics.length).toBeGreaterThan(5)
	expect(steps.length).toBeGreaterThan(0)
	const texts = [...metrics.flatMap((row) => [row.label, row.value]), ...steps.flatMap((row) => [row.duty, row.power])]
	expect(offenders(texts, (t) => SPEED.test(t), (t) => t)).toEqual([])
})

test("a duty the firmware reports is never qualified as a mode", () => {
	// The dropdown and the current-level label are the two places a duty reaches
	// a reader as a number, so both are checked as the strings they produce.
	expect(fanLevelLabel(fanState({ level: 0, level_label: "0%" }))).not.toMatch(/\(\s*(auto|automatic)/i)
	expect(read(LEVELS)).toMatch(/func LevelLabel\(level int\) string \{\s*return strconv\.Itoa\(level\) \+ "%"/)
	expect(read(TAB)).not.toMatch(/\b0\b[^\n]{0,24}\bauto\b/i)
})

test("the speed is withheld from the panel and still carried on the wire", () => {
	// The same ruling seen from the other side. A panel rule outgrowing its field
	// would break every consumer of the wire, so the field is pinned as present
	// here rather than left to whoever goes looking for residue.
	expect(read(TYPES)).toContain("rpm_est?: number")
	expect(read(TYPES)).toContain("rpm_est_is_estimate: boolean")
	expect(read(PLUS_GO)).toContain('json:"rpm_est,omitempty"')
	expect(read(PLUS_GO)).toContain('json:"rpm_est_is_estimate"')
	expect(read(PLUS_GO)).toMatch(/RPMEst\s+uint16 `json:"rpm_est"`/)
})