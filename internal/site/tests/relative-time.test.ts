import { describe, expect, test } from "bun:test"
import { formatRelativeTime } from "../src/components/systems-table/relative-time"

const now = Date.UTC(2026, 9, 20, 12, 0, 0)

describe("formatRelativeTime", () => {
	test("formats recent timestamps as now", () => {
		expect(formatRelativeTime(now - 5_000, now, "en")).toBe("now")
	})

	test("formats minutes, hours and days", () => {
		expect(formatRelativeTime(now - 2 * 60_000, now, "en")).toBe("2 minutes ago")
		expect(formatRelativeTime(now - 3 * 3_600_000, now, "en")).toBe("3 hours ago")
		expect(formatRelativeTime(now - 2 * 86_400_000, now, "en")).toBe("2 days ago")
	})

	test("formats future timestamps and accepts ISO strings", () => {
		expect(formatRelativeTime(new Date(now + 3_600_000), now, "en")).toBe("in 1 hour")
		expect(formatRelativeTime(new Date(now - 60_000).toISOString(), now, "en")).toBe("1 minute ago")
	})

	test("returns an empty string for an invalid date", () => {
		expect(formatRelativeTime("not-a-date", now, "en")).toBe("")
	})
})
