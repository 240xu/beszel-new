import type { Messages } from "@lingui/core"
import { i18n } from "@lingui/core"
import { t } from "@lingui/core/macro"
import { detect, fromNavigator, fromStorage } from "@lingui/detect-locale"
import languages from "@/lib/languages"
import { messages as enMessages } from "@/locales/en/en"
import { BatteryState } from "./enums"
import { $direction } from "./stores"

const rtlLanguages = new Set(["ar", "fa", "he"])

// activates locale
function activateLocale(locale: string, messages: Messages = enMessages) {
	i18n.load(locale, messages)
	i18n.activate(locale)
	document.documentElement.lang = locale
	localStorage.setItem("lang", locale)
	$direction.set(rtlLanguages.has(locale) ? "rtl" : "ltr")
}

/** Locales whose catalog is incomplete and borrow from a fuller parent.
 *
 *  zh-HK is the only reader-visible catalog that is not complete: it covers the
 *  interface but not every plus string. Lingui resolves a missing message by
 *  falling back to the source locale (English), so without this a Hong Kong
 *  reader would see Chinese everywhere except the fan panel. Merging `zh` under
 *  zh-HK keeps the parent's strings as the default and lets zh-HK's own entries
 *  win, which is the same precedence Crowdin uses. */
const partialLocaleParents: Record<string, string> = {
	"zh-HK": "zh",
}

async function loadMessages(locale: string): Promise<Messages> {
	const { messages }: { messages: Messages } = await import(`../locales/${locale}/${locale}.ts`)
	return messages
}

// dynamically loads translations for the given locale
export async function dynamicActivate(locale: string) {
	if (locale === "en") {
		activateLocale(locale)
		return
	}
	try {
		const parent = partialLocaleParents[locale]
		if (parent) {
			const [base, own] = await Promise.all([loadMessages(parent), loadMessages(locale)])
			// An untranslated entry compiles to an empty string, and spreading it
			// would overwrite the parent with "". Keep the parent's text for those
			// and let only non-empty zh-HK entries win.
			const merged: Messages = { ...base }
			for (const [id, text] of Object.entries(own)) {
				if (text !== "") merged[id] = text
			}
			activateLocale(locale, merged)
			return
		}
		activateLocale(locale, await loadMessages(locale))
	} catch (error) {
		console.error(`Error loading ${locale}`, error)
		activateLocale("en")
	}
}

export function getLocale() {
	// let locale = detect(fromUrl("lang"), fromStorage("lang"), fromNavigator(), "en")
	let locale = detect(fromStorage("lang"), fromNavigator(), "en")
	// log if dev
	if (import.meta.env.DEV) {
		console.log("detected locale", locale)
	}
	// handle zh variants
	if (locale?.startsWith("zh-")) {
		// map zh variants to zh-CN
		const zhVariantMap: Record<string, string> = {
			"zh-HK": "zh-HK",
			"zh-TW": "zh",
			"zh-MO": "zh",
			"zh-Hant": "zh",
		}
		return zhVariantMap[locale] || "zh-CN"
	}
	locale = (locale || "en").split("-")[0]
	// use en if locale is not in languages
	if (!languages.some((l) => l[0] === locale)) {
		locale = "en"
	}
	return locale
}

////////////////////////////////////////////////////////

export const batteryStateTranslations = {
	[BatteryState.Unknown]: () => t({ message: "Unknown", comment: "Context: Battery state" }),
	[BatteryState.Empty]: () => t({ message: "Empty", comment: "Context: Battery state" }),
	[BatteryState.Full]: () => t({ message: "Full", comment: "Context: Battery state" }),
	[BatteryState.Charging]: () => t({ message: "Charging", comment: "Context: Battery state" }),
	[BatteryState.Discharging]: () => t({ message: "Discharging", comment: "Context: Battery state" }),
	[BatteryState.Idle]: () => t({ message: "Idle", comment: "Context: Battery state" }),
} as const
