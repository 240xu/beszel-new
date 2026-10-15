import { expect, test } from "bun:test"
import { readFanAuditCode } from "@/lib/plus-refusals"

/** The audit trail carries, on top of the refusal codes a refused write arrives on,
 *  the regress decline codes a stopped-short regress is audited under. The parser has
 *  to keep both: dropping a decline code left `code` undefined and lost its message, so
 *  a panel reading the trail could not tell which gate held the fan. The codes are the
 *  literal wire strings the guard publishes (internal/fanapi/guard.go), asserted as
 *  literals rather than read back from this module's own list, so the check follows the
 *  server and not this file. */
test("a regress decline on the audit trail survives parsing", () => {
	expect(readFanAuditCode("regress_at_emergency_edge")).toBe("regress_at_emergency_edge")
	expect(readFanAuditCode("regress_nothing_pending")).toBe("regress_nothing_pending")
	expect(readFanAuditCode("regress_daemon_owns_fan")).toBe("regress_daemon_owns_fan")
	expect(readFanAuditCode("regress_temperature_unreadable")).toBe("regress_temperature_unreadable")
	expect(readFanAuditCode("regress_no_handover_value")).toBe("regress_no_handover_value")
})

/** The hold is the one that matters most: it wrote the emergency duty, so a panel that
 *  dropped it would show a held fan as if nothing had been recorded at all. */
test("the emergency hold's decline code is not dropped", () => {
	expect(readFanAuditCode("regress_at_emergency_edge")).toBeDefined()
})

/** Widening the parser to accept declines must not cost a refusal code its meaning. */
test("a refusal code still parses as itself", () => {
	expect(readFanAuditCode("pfand_active")).toBe("pfand_active")
	expect(readFanAuditCode("package_too_hot")).toBe("package_too_hot")
	expect(readFanAuditCode("level_not_allowed")).toBe("level_not_allowed")
})

/** A code neither set publishes stays undefined -- the reader invents nothing and
 *  buckets nothing by status. */
test("a code neither set publishes parses as undefined", () => {
	expect(readFanAuditCode("regress_did_not_publish_this")).toBeUndefined()
	expect(readFanAuditCode("totally_unknown")).toBeUndefined()
	expect(readFanAuditCode(undefined)).toBeUndefined()
})
