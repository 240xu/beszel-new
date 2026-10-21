#!/usr/bin/env node
/**
 * Catalog consistency: does the committed translation set still match the source?
 *
 * The obvious gate -- `lingui extract --overwrite && git diff --exit-code src/locales`
 * -- cannot be used here, and the reason is worth writing down so nobody "fixes" it
 * later by adding the diff back:
 *
 *   The committed catalogs are not byte-stable. `lingui extract --overwrite` rewrites
 *   every `.po` file with the entries in the order the source scan reached them, and
 *   the order changed when the UI branch reordered components. On `abf0bdb8` that
 *   produces a 260-insertion / 260-deletion diff whose two sides are the *same*
 *   entries in a different order -- 36 msgids move, none change. A byte diff
 *   therefore fails on a tree whose catalogs are perfectly consistent.
 *
 *   (`lingui extract --check` does not exist either: @lingui/cli 5.4.1's `extract`
 *   accepts --config --locale --overwrite --clean --debounce --verbose
 *   --convert-from --watch and nothing else.)
 *
 * So this compares the *sets*, not the bytes: for every locale, the set of
 * (msgctxt, msgid) pairs the source needs must equal the set the committed catalog
 * carries. A message the source added and the catalog does not have is a missing
 * translation; a message the catalog has and the source no longer has is an orphan.
 *
 * Usage:
 *   node scripts/check-catalogs.mjs <committed-locales-dir> <extracted-locales-dir>
 */

import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

/** Pull the (msgctxt, msgid) pairs out of a gettext catalog. */
const parsePo = (text) => {
	const entries = []
	let msgctxt = null
	let msgid = null
	let target = null
	const flush = () => {
		if (msgid !== null) entries.push(`${msgctxt ?? ""}\u0000${msgid}`)
		msgctxt = null
		msgid = null
		target = null
	}
	for (const raw of text.split("\n")) {
		const line = raw.trim()
		if (line === "") {
			flush()
			continue
		}
		if (line.startsWith("#")) continue
		const unescape = (raw2) => raw2.replace(/\\"/g, '"').replace(/\\n/g, "\n").replace(/\\\\/g, "\\")
		// A keyword line: `msgid "..."`, `msgctxt "..."`, `msgid_plural "..."`, `msgstr "..."`.
		const keyword = /^(msgctxt|msgid_plural|msgid|msgstr)\s+"(.*)"$/.exec(line)
		if (keyword) {
			const value = unescape(keyword[2])
			if (keyword[1] === "msgctxt") {
				msgctxt = value
				target = "msgctxt"
			} else if (keyword[1] === "msgid_plural") {
				// A plural entry is identified by its singular msgid; the plural form is
				// part of the message, not a separate entry.
				target = null
			} else if (keyword[1] === "msgid") {
				msgid = value
				target = "msgid"
			} else {
				target = null
			}
			continue
		}
		// A continuation line: a quoted string that belongs to the keyword above it.
		const quoted = /^"(.*)"$/.exec(line)
		if (!quoted) continue
		const value = unescape(quoted[1])
		if (target === "msgctxt") {
			msgctxt = (msgctxt ?? "") + value
		} else if (target === "msgid") {
			msgid = (msgid ?? "") + value
		}
	}
	flush()
	// The header entry (empty msgid) is metadata, not a message.
	return new Set(entries.filter((entry) => !entry.endsWith("\u0000")))
}

const catalogFiles = (dir) => {
	const found = []
	for (const locale of readdirSync(dir)) {
		const localeDir = join(dir, locale)
		let stat
		try {
			stat = statSync(localeDir)
		} catch {
			continue
		}
		if (!stat.isDirectory()) continue
		for (const file of readdirSync(localeDir)) {
			if (!file.endsWith(".po")) continue
			found.push({ locale, file: join(localeDir, file) })
		}
	}
	return found
}

const [committedDir, extractedDir] = process.argv.slice(2)
if (!committedDir || !extractedDir) {
	console.error("usage: check-catalogs.mjs <committed-locales-dir> <extracted-locales-dir>")
	process.exit(2)
}

const committed = new Map()
for (const { locale, file } of catalogFiles(committedDir)) {
	committed.set(`${locale}/${file.split("/").pop()}`, parsePo(readFileSync(file, "utf8")))
}
const extracted = new Map()
for (const { locale, file } of catalogFiles(extractedDir)) {
	extracted.set(`${locale}/${file.split("/").pop()}`, parsePo(readFileSync(file, "utf8")))
}

let missingTotal = 0
let orphanTotal = 0
for (const [name, committedSet] of [...committed.entries()].sort()) {
	const extractedSet = extracted.get(name)
	if (!extractedSet) {
		console.error(`check-catalogs: ${name} has no extracted counterpart`)
		process.exit(1)
	}
	const missing = [...extractedSet].filter((entry) => !committedSet.has(entry))
	const orphans = [...committedSet].filter((entry) => !extractedSet.has(entry))
	missingTotal += missing.length
	orphanTotal += orphans.length
	if (missing.length) {
		console.error(`check-catalogs: ${name} is missing ${missing.length} message(s) the source needs:`)
		for (const entry of missing.slice(0, 10)) {
			console.error(`  ${JSON.stringify(entry.split("\u0000")[1])}`)
		}
	}
	if (orphans.length) {
		console.error(`check-catalogs: ${name} carries ${orphans.length} message(s) the source no longer uses:`)
		for (const entry of orphans.slice(0, 10)) {
			console.error(`  ${JSON.stringify(entry.split("\u0000")[1])}`)
		}
	}
}

if (missingTotal || orphanTotal) {
	console.error(`check-catalogs: ${missingTotal} missing, ${orphanTotal} orphaned`)
	process.exit(1)
}
console.log(`check-catalogs: ${committed.size} catalog(s) match the source message set`)
