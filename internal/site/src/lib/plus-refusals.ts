/** The only reliable discriminator on a fan refusal: PocketBase's envelope is
 *  `{status, message, data}`, carries no `error` field, and capitalises `message`, so
 *  neither of the others can branch or key a translation. A hand-written mirror of
 *  fanapi's closed set, not a second source: both languages check its literal text
 *  against internal/fanapi/wire.go. */
export const FAN_REJECT_CODES = [
	"rollback_failed",
	"write_unconfirmed",
	"write_failed",
	"confirm_required",
	"level_not_allowed",
	"curve_rejected",
	"pfand_active",
	"package_too_hot",
	"temperature_unknown",
	"breaker_open",
	"unsupported",
	"internal",
] as const

export type FanRejectCode = (typeof FAN_REJECT_CODES)[number]

const REJECT_CODES: ReadonlySet<string> = new Set(FAN_REJECT_CODES)

/** A refusal. `message` is a constant, not the server's text, so reaching for
 *  it as a translation key fails visibly instead of silently rendering a
 *  capitalised variant; the strings stay on rawCode and rawMessage for logs.
 *  `code` is undefined when the route refused before classification — a missing
 *  confirm, an unreadable body, a missing curve field, an unknown mode, a failed
 *  systemctl — so a consumer falls back to the status rather than invent one. */
export class FanApiError extends Error {
	readonly status: number
	readonly code: FanRejectCode | undefined
	readonly rawCode: string | undefined
	readonly rawMessage: string | undefined

	constructor(
		status: number,
		code: FanRejectCode | undefined,
		rawCode: string | undefined,
		rawMessage: string | undefined
	) {
		super("fan request refused")
		this.name = "FanApiError"
		this.status = status
		this.code = code
		this.rawCode = rawCode
		this.rawMessage = rawMessage
	}
}

/** PocketBase's error envelope: the code lives at `data.code`. */
type PocketBaseErrorBody = { message?: unknown; data?: { code?: unknown } | unknown }

function asString(v: unknown): string | undefined {
	return typeof v === "string" ? v : undefined
}

/** An unknown code is dropped, not bucketed by status: that would report a sensor fault as a thermal one. */
export function readFanRefusal(status: number, text: string): FanApiError {
	let body: PocketBaseErrorBody | undefined
	try {
		body = JSON.parse(text) as PocketBaseErrorBody
	} catch {
		return new FanApiError(status, undefined, undefined, undefined)
	}
	const data = body?.data
	const rawCode = data && typeof data === "object" ? asString((data as { code?: unknown }).code) : undefined
	const rawMessage = asString(body?.message)
	const code = rawCode !== undefined && REJECT_CODES.has(rawCode) ? (rawCode as FanRejectCode) : undefined
	return new FanApiError(status, code, rawCode, rawMessage)
}
