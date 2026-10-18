import { t } from "@lingui/core/macro"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { fmtCelsius } from "@/lib/plus-metrics"
import { getFanState } from "@/lib/plus-api"
import {
	FAN_DUTIES,
	fanLevelLabel,
	fanRegressView,
	fanWriteRefused,
	fanWriteSettled,
	issueFanWrite,
	type FanOutcomeKind,
	type FanRefusalKey,
	type FanWrite,
	type FanWriteOutcome,
	type FanWriteSettled,
} from "@/lib/plus-fan-view"
import type { FanCurve, FanMode, FanState } from "@/types"
import { ChartCard } from "./chart-card"

/** The outcome wording `plus-fan-view.ts` owns, built where the panel renders. As
 *  module-level tables these resolved once at import, so switching language later
 *  left them in the language that was active at load; a function body re-reads the
 *  literals on every render instead. Typed against the outcome keys, so a kind
 *  cannot appear there without this failing to compile, and no arm can reach the
 *  badge of another. */
function outcomeCopy() {
	const OUTCOME_TEXT: Record<FanOutcomeKind, string> = {
		applied: t`In force — the fan is running this now.`,
		pending_restart: t`Written to disk; the curve daemon has not picked it up.`,
		daemon_running: t`The curve daemon is running and is driving the fan from the curve on disk.`,
		daemon_stopped: t`The curve daemon is not running, so it is not driving the fan.`,
	}

	const OUTCOME_BADGE: Record<FanOutcomeKind, string> = {
		applied: t`In force`,
		pending_restart: t`Awaiting restart`,
		daemon_running: t`Curve running`,
		daemon_stopped: t`Curve stopped`,
	}

	return { OUTCOME_TEXT, OUTCOME_BADGE }
}

/** The refusal wording, typed against `FanRefusalKey` for the same reason: a
 *  refusal this build has a key for cannot fall through to a neighbour's sentence.
 *  Built at render like the outcome copy above. */
function refusalCopy() {
	const REFUSAL_TEXT: Record<FanRefusalKey, string> = {
		rollback_failed: t`The previous duty could not be restored: the fan is holding one you did not ask for.`,
		write_unconfirmed: t`The fan is not holding the duty that was requested; the old one was put back.`,
		write_failed: t`The fan control device refused the write.`,
		confirm_required: t`The change was not confirmed, so the fan was left alone.`,
		level_not_allowed: t`That duty is not one this firmware accepts.`,
		curve_rejected: t`The curve failed validation and was rejected before anything was written.`,
		pfand_active: t`The curve daemon owns the fan; stop it before setting a duty by hand.`,
		package_too_hot: t`The package is at or above the emergency edge in the curve, so no duty was written. Let it cool, or raise emerg in the curve, before writing again.`,
		temperature_unknown: t`The package temperature could not be read, so no duty was written and the reading cannot be trusted. Check the sensor and the zone it names rather than cooling the machine.`,
		breaker_open: t`Fan control is latched open after repeated failures; nothing was written.`,
		unsupported: t`Fan control is only implemented on Linux.`,
		internal: t`The write did not complete.`,
		unclassified: t`The server refused this without naming a reason, so nothing can be concluded about the fan.`,
		unrecognised: t`The server refused this for a reason this build does not know.`,
	}

	return REFUSAL_TEXT
}

/** Threshold and duty fields an operator may retune. `t4` is the full-speed
 *  backstop and travels with the curve rather than being offered here. */
const CURVE_FIELDS: Array<{ key: keyof FanCurve; locked?: boolean }> = [
	{ key: "idle" },
	{ key: "l0" },
	{ key: "t1" },
	{ key: "l1" },
	{ key: "t2" },
	{ key: "l2" },
	{ key: "t3" },
	{ key: "l3" },
	{ key: "t4", locked: true },
	{ key: "l4" },
	{ key: "t5" },
	{ key: "l5" },
]

/** The tuning constants the curve also requires. They ride into every write
 *  untouched, so they are listed rather than hidden: an operator reading a curve
 *  should not have to guess how many numbers are about to reach the disk. */
const CURVE_CARRIED: Array<keyof FanCurve> = [
	"emerg",
	"e",
	"emerg_rel",
	"cap",
	"trim",
	"gain",
	"hyst",
	"dwell",
	"dead",
	"iv",
]

function armedCopy() {
	const ARMED_TITLE: Record<FanWrite["kind"], string> = {
		level: t`Write this duty to the firmware`,
		mode: t`Change the curve daemon's state`,
		curve: t`Write this curve to the disk`,
	}

	return ARMED_TITLE
}

/** The plus fan surface is a plain route rather than a collection, so this polls
 *  the way the rest of the page reads: once on mount, on a timer, both torn down
 *  when the card goes away. An unreadable read leaves the card empty rather than
 *  holding a duty it can no longer vouch for. */
function useFanState(intervalMs = 10_000): FanState | null {
	const [state, setState] = useState<FanState | null>(null)
	useEffect(() => {
		let live = true
		const read = () => {
			getFanState()
				.then((next) => live && setState(next))
				.catch(() => live && setState(null))
		}
		read()
		const timer = setInterval(read, intervalMs)
		return () => {
			live = false
			clearInterval(timer)
		}
	}, [intervalMs])
	return state
}

/** The banner and the refusal line are written from one settled value, so the two
 *  can never describe the same write at once. */
function applySettled(
	settled: FanWriteSettled,
	setOutcome: (outcome: FanWriteOutcome | null) => void,
	setRefusal: (refusal: { key: FanRefusalKey; detail?: string } | null) => void,
	setOffline: (offline: boolean) => void
) {
	if (settled.ok) {
		setOutcome(settled.outcome)
		setRefusal(null)
		setOffline(false)
		return
	}
	if (settled.dropBanner) setOutcome(null)
	setOffline(settled.offline)
	setRefusal(settled.refusal && { key: settled.refusal.key, detail: settled.refusal.detail })
}

function useFanWrite(onSettled: (settled: FanWriteSettled) => void) {
	const [busy, setBusy] = useState(false)
	const run = async (write: FanWrite) => {
		setBusy(true)
		try {
			onSettled(fanWriteSettled(await issueFanWrite(write)))
		} catch (err) {
			onSettled(fanWriteRefused(write, err))
		} finally {
			setBusy(false)
		}
	}
	return { busy, run }
}

function ArmedTarget({ write }: { write: FanWrite }) {
	if (write.kind === "level") return <>{`${write.level}%`}</>
	if (write.kind === "mode") return <>{write.mode}</>
	return <>{t`the edited curve`}</>
}

function OutcomeBanner({ outcome, onRestart }: { outcome: FanWriteOutcome; onRestart: () => void }) {
	const { OUTCOME_TEXT, OUTCOME_BADGE } = outcomeCopy()
	return (
		<div className="mt-3 flex flex-wrap items-center gap-3 rounded-md border p-3 text-sm">
			<span className="font-medium">{OUTCOME_BADGE[outcome.kind]}</span>
			<span className="text-muted-foreground">{OUTCOME_TEXT[outcome.kind]}</span>
			{outcome.needsRestart && (
				<Button size="sm" variant="outline" onClick={onRestart}>
					{t`Restart the curve daemon`}
				</Button>
			)}
		</div>
	)
}

function ConfirmPanel({
	write,
	busy,
	onFire,
	onCancel,
}: {
	write: FanWrite
	busy: boolean
	onFire: () => void
	onCancel: () => void
}) {
	const ARMED_TITLE = armedCopy()
	return (
		<div className="mt-3 flex flex-wrap items-center gap-3 rounded-md border p-3">
			<span className="text-sm font-medium">{ARMED_TITLE[write.kind]}</span>
			<span className="font-mono text-sm">
				<ArmedTarget write={write} />
			</span>
			<span className="w-full text-xs text-muted-foreground">
				{t`Confirm to write this to the fan control path. There is no undo from this panel.`}
			</span>
			<Button size="sm" variant="destructive" disabled={busy} onClick={onFire}>
				{t`Confirm`}
			</Button>
			<Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
				{t`Cancel`}
			</Button>
		</div>
	)
}

function RegressLine({ state }: { state: FanState }) {
	const regress = fanRegressView(state, Date.now())
	if (regress.kind === "counting_down") {
		return (
			<p className="mt-2 text-xs text-muted-foreground">
				{t`A hand-set duty is on record. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.`}{" "}
				<span className="font-mono">{regress.countdown}</span>
			</p>
		)
	}
	if (regress.kind === "no_deadline") {
		return (
			<p className="mt-2 text-xs text-muted-foreground">
				{t`A hand-set duty is on record with no deadline on file. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.`}
			</p>
		)
	}
	// Nothing on record and a deadline lost to an agent restart are the same bytes,
	// and a duty set before that restart keeps driving the fan, because it lives
	// in the firmware and not in the agent. This arm used to render nothing, which
	// left that possibility invisible. It claims neither way on purpose: it says
	// the two cannot be told apart rather than guessing which one is true.
	return (
		<p className="mt-2 text-xs text-muted-foreground">
			{t`No hand-set duty is on record. One set before an agent restarted would still be driving the fan, and this panel cannot see it.`}
		</p>
	)
}

function CurveEditor({ curve, onChange }: { curve: FanCurve; onChange: (curve: FanCurve) => void }) {
	return (
		<div className="flex flex-col gap-3">
			<div className="grid grid-cols-3 gap-3 sm:grid-cols-4">
				{CURVE_FIELDS.map((field) => (
					<div key={String(field.key)} className="flex flex-col gap-1">
						<span className="text-xs text-muted-foreground">{String(field.key)}</span>
						<Input
							aria-label={String(field.key)}
							type="number"
							disabled={field.locked}
							value={curve[field.key]}
							onChange={(e) => onChange({ ...curve, [field.key]: Number(e.target.value) })}
						/>
					</div>
				))}
			</div>
			<p className="text-xs text-muted-foreground">
				{t`Also written unchanged:`} {CURVE_CARRIED.map((key) => `${key}=${curve[key]}`).join(", ")}
			</p>
		</div>
	)
}

/** The restart outlet arms the same confirm step as a write, because handing the
 *  fan back to a curve is a change to what is cooling the machine. */
function modeIntent(mode: FanMode): FanWrite {
	return { kind: "mode", mode }
}

export function PlusFanTab({ grid, dataEmpty }: { grid: boolean; dataEmpty: boolean }) {
	const [armed, setArmed] = useState<FanWrite | null>(null)
	const [outcome, setOutcome] = useState<FanWriteOutcome | null>(null)
	const [refusal, setRefusal] = useState<{ key: FanRefusalKey; detail?: string } | null>(null)
	const [offline, setOffline] = useState(false)

	const state = useFanState()
	const written = useFanWrite((settled) => {
		applySettled(settled, setOutcome, setRefusal, setOffline)
		if (settled.ok) setArmed(null)
	})

	const manual = state ? !state.pfand_active : false
	const REFUSAL_TEXT = refusalCopy()

	return (
		<ChartCard
			key="fan-level"
			empty={dataEmpty || !state}
			grid={grid}
			title={t`Fan Level`}
			description={t`Duty written to the firmware. Nothing writes it back afterwards: the firmware reads zero as zero percent, so returning the fan to its curve is yours to do.`}
		>
			{state && (
				<>
					<div className="flex flex-wrap items-center gap-3">
						<div className="text-sm">
							<span className="text-muted-foreground">{t`Current`}: </span>
							<span className="font-mono">{fanLevelLabel(state)}</span>
							{/* "Package" also names a software package in the updates table, where it
							 *  reads 套件. Context keeps the CPU package out of that. */}
							<span className="text-muted-foreground ms-3">{t({ message: "Package", context: "CPU package" })}: </span>
							<span className="font-mono">{fmtCelsius(state.pkg_temp_c)}</span>
						</div>

						{!state.level_read && (
							<p className="mt-2 text-xs text-muted-foreground">
								{t`The current duty could not be read; the fan's present duty is unknown.`}
							</p>
						)}

						{/* The agent reports the thermal floor on its own boolean, so a hold the daemon
						 *  enforces is not one of this panel's writes: the badge rises from the state alone
						 *  and skips when an older agent answers without the field. */}
						{state.thermal_floor_armed === true && (
							<span className="font-medium">{t`Thermal guard holding`}</span>
						)}

						<Button
							variant={manual ? "default" : "outline"}
							size="sm"
							disabled={written.busy}
							onClick={() => setArmed(modeIntent(manual ? "auto" : "manual"))}
						>
							{manual ? t`Return to the curve` : t`Take manual control`}
						</Button>

						<Select
							value={String(state.level ?? "")}
							disabled={!manual || written.busy}
							onValueChange={(v) => setArmed({ kind: "level", level: Number(v) })}
						>
							<SelectTrigger className="w-32">
								<SelectValue>{fanLevelLabel(state)}</SelectValue>
							</SelectTrigger>
							<SelectContent>
								{FAN_DUTIES.map((duty) => (
									<SelectItem key={duty} value={String(duty)}>
										{`${duty}%`}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					{armed && (
						<ConfirmPanel
							write={armed}
							busy={written.busy}
							onFire={() => written.run(armed)}
							onCancel={() => setArmed(null)}
						/>
					)}

					{outcome && <OutcomeBanner outcome={outcome} onRestart={() => setArmed(modeIntent("auto"))} />}

					{offline && <p role="alert" className="mt-2 text-sm text-destructive">{t`The request never reached the server.`}</p>}
					{refusal && (
						<p role="alert" className="mt-2 text-sm text-destructive">
							{REFUSAL_TEXT[refusal.key]}
							{refusal.detail && <span className="ms-2 font-mono text-xs opacity-70">{refusal.detail}</span>}
						</p>
					)}

					{!manual && (
						<p className="mt-2 text-xs text-muted-foreground">
							{t`The curve daemon is in control. Switch to manual before setting a duty by hand.`}
						</p>
					)}

					<RegressLine state={state} />
				</>
			)}
		</ChartCard>
	)
}

export function PlusFanCurveTab({ grid, dataEmpty }: { grid: boolean; dataEmpty: boolean }) {
	const [draft, setDraft] = useState<FanCurve | null>(null)
	const [armed, setArmed] = useState<FanWrite | null>(null)
	const [outcome, setOutcome] = useState<FanWriteOutcome | null>(null)
	const [refusal, setRefusal] = useState<{ key: FanRefusalKey; detail?: string } | null>(null)

	const state = useFanState()
	const written = useFanWrite((settled) => {
		applySettled(settled, setOutcome, setRefusal, () => {})
		if (settled.ok) setArmed(null)
		if (settled.ok) setDraft(null)
	})

	const curve = draft ?? state?.curve ?? null
	const REFUSAL_TEXT = refusalCopy()

	return (
		<ChartCard
			key="fan-curve"
			empty={dataEmpty || !curve}
			grid={grid}
			title={t`Temperature Curve`}
			description={t`pfand steps the duty by package temperature. Every field is written, including the ones this form does not offer.`}
		>
			{curve && (
				<>
					<CurveEditor curve={curve} onChange={setDraft} />

					<div className="mt-3 flex gap-2">
						<Button
							size="sm"
							variant="outline"
							disabled={written.busy || draft === null}
							onClick={() => setArmed({ kind: "curve", curve })}
						>
							{t`Write this curve`}
						</Button>
						{draft !== null && (
							<Button size="sm" variant="ghost" disabled={written.busy} onClick={() => setDraft(null)}>
								{t`Discard changes`}
							</Button>
						)}
					</div>

					{armed && (
						<ConfirmPanel
							write={armed}
							busy={written.busy}
							onFire={() => written.run(armed)}
							onCancel={() => setArmed(null)}
						/>
					)}

					{outcome && <OutcomeBanner outcome={outcome} onRestart={() => setArmed(modeIntent("auto"))} />}

					{refusal && (
						<p role="alert" className="mt-2 text-sm text-destructive">
							{REFUSAL_TEXT[refusal.key]}
							{refusal.detail && <span className="ms-2 font-mono text-xs opacity-70">{refusal.detail}</span>}
						</p>
					)}
				</>
			)}
		</ChartCard>
	)
}
