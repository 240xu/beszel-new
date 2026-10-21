const formatterCache = new Map<string, Intl.RelativeTimeFormat>()

function formatter(locale: string): Intl.RelativeTimeFormat {
	let instance = formatterCache.get(locale)
	if (!instance) {
		instance = new Intl.RelativeTimeFormat(locale, { numeric: "auto" })
		formatterCache.set(locale, instance)
	}
	return instance
}

/** Format a timestamp as a short relative time, e.g. "3 minutes ago" / "now". */
export function formatRelativeTime(value: string | number | Date, now = Date.now(), locale = "en"): string {
	const date = value instanceof Date ? value.getTime() : typeof value === "string" ? Date.parse(value) : value
	if (!Number.isFinite(date)) {
		return ""
	}
	const seconds = Math.round((date - now) / 1000)
	const abs = Math.abs(seconds)
	const format = formatter(locale)
	if (abs < 45) {
		return format.format(0, "second")
	}
	if (abs < 3600) {
		return format.format(Math.round(seconds / 60), "minute")
	}
	if (abs < 86400) {
		return format.format(Math.round(seconds / 3600), "hour")
	}
	if (abs < 604800) {
		return format.format(Math.round(seconds / 86400), "day")
	}
	if (abs < 2629800) {
		return format.format(Math.round(seconds / 604800), "week")
	}
	if (abs < 31557600) {
		return format.format(Math.round(seconds / 2629800), "month")
	}
	return format.format(Math.round(seconds / 31557600), "year")
}
