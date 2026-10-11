import { t } from "@lingui/core/macro"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { fmtCelsius } from "@/lib/plus-metrics"
import { getFanState } from "@/lib/plus-api"
import { FanApiError } from "@/lib/plus-refusals"
import {
	FAN_DUTIES,
	fanLevelLabel,
	fanRefusal,
	fanRegressView,
	fanWriteOutcome,
	issueFanWrite,
	type CurveSaveResult,
	type FanOutcomeKind,
	type FanRefusalKey,
	type FanWrite,
	type FanWriteOutcome,
} from "@/lib/plus-fan-view"
import type { FanCurve, FanMode, FanState } from "@/types"
import { ChartCard } from "./chart-card"

/** Translations of the outcome wording `plus-fan-view.ts` owns. Typed against the
 *  outcome keys, so a third kind cannot appear there without this failing to
 *  compile, and no arm can reach the badge of another. */
const OUTCOME_TEXT: Record<FanOutcomeKind, string> = {
	applied: t`In force — the fan is running this now.`,
	pending_restart: t`Written to disk; the curve daemon has not picked it up.`,
}

const OUTCOME_BADGE: Record<FanOutcomeKind, string> = {
	applied: t`In force`,
	pending_restart: t`Awaiting restart`,
}

/** Translations of the refusal wording, typed against `FanRefusalKey` for the
 *  same reason: a refusal this build has a key for cannot fall through to a
 *  neighbour's sentence. */
const REFUSAL_TEXT: Record<FanRefusalKey, string> = {
	rollback_failed: t`The previous duty could not be restored: the fan is holding one you did not ask for.`,
	write_unconfirmed: t`The fan is not holding the duty that was requested; the old one was put back.`,
	write_failed: t`The fan control device refused the write.`,
	confirm_required: t`The change was not confirmed, so the fan was left alone.`,
	level_not_allowed: t`That duty is not one this firmware accepts.`,
	curve_rejected: t`The curve failed validation and was rejected before anything was written.`,
	pfand_active: t`The curve daemon owns the fan; stop it before setting a duty by hand.`,
	package_too_hot: t`The package is at or above the emergency edge in the curve, so no duty was written.`,
	temperature_unknown: t`The package temperature could not be read, so no duty was written and the reading cannot be trusted.`,
	breaker_open: t`Fan control is latched open after repeated failures; nothing was written.`,
	unsupported: t`Fan control is only implemented on Linux.`,
	internal: t`The write did not complete.`,
	unclassified: t`The request was refused before it reached the fan.`,
	unrecognised: t`The server refused this for a reason this build does not know.`,
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

const ARMED_TITLE: Record<FanWrite["kind"], string> = {
	level: t`Write this duty to the firmware`,
	mode: t`Change the curve daemon's state`,
	curve: t`Write this curve to the disk`,
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

function useFanWrite(onWritten: (result: CurveSaveResult | undefined) => void, onFailed: (err: unknown) => void) {
	const [busy, setBusy] = useState(false)
	const run = async (write: FanWrite) => {
		setBusy(true)
		try {
			onWritten(await issueFanWrite(write))
		} catch (err) {
			onFailed(err)
		} finally {
			setBusy(false)
		}
	}
	return { busy, run }
}

function ArmedTarget({ write }: { write: FanWrite }) {
	if (write.kind === "level") return <>{write.level === 0 ? "0 (auto)" : `${write.level}%`}</>
	if (write.kind === "mode") return <>{write.mode}</>
	return <>{t`the edited curve`}</>
}

function OutcomeBanner({ outcome, onRestart }: { outcome: FanWriteOutcome; onRestart: () => void }) {
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
	if (regress.kind === "none") return null
	if (regress.kind === "counting_down") {
		return (
			<p className="mt-2 text-xs text-muted-foreground">
				{t`A hand-set duty is on record and reverts to the curve in ${regress.countdown ?? ""}.`}
			</p>
		)
	}
	return (
		<p className="mt-2 text-xs text-muted-foreground">
			{t`A hand-set duty is on record, but no deadline came with it, so this panel cannot say when it ends.`}
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
	const written = useFanWrite(
		(result) => {
			if (result) setOutcome(fanWriteOutcome(result))
			setArmed(null)
			setRefusal(null)
			setOffline(false)
		},
		(err) => {
			// The armed panel stays open on a refusal so the operator reads what
			// happened before choosing to retry it or cancel it.
			if (err instanceof FanApiError) {
				const view = fanRefusal(err)
				setRefusal({ key: view.key, detail: view.detail })
				setOffline(false)
			} else {
				setRefusal(null)
				setOffline(true)
			}
		}
	)

	const manual = state ? !state.pfand_active : false

	return (
		<ChartCard
			key="fan-level"
			empty={dataEmpty || !state}
			grid={grid}
			title={t`Fan Level`}
			description={t`Duty written to the firmware. Zero hands control back to the curve.`}
		>
			{state && (
				<>
					<div className="flex flex-wrap items-center gap-3">
						<div className="text-sm">
							<span className="text-muted-foreground">{t`Current`}: </span>
							<span className="font-mono">{fanLevelLabel(state)}</span>
							<span className="text-muted-foreground ms-3">{t`Package`}: </span>
							<span className="font-mono">{fmtCelsius(state.pkg_temp_c)}</span>
						</div>

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
										{duty === 0 ? "0 (auto)" : `${duty}%`}
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

					{offline && <p className="mt-2 text-sm text-destructive">{t`The request never reached the server.`}</p>}
					{refusal && (
						<p className="mt-2 text-sm text-destructive">
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
	const written = useFanWrite(
		(result) => {
			if (result) {
				setOutcome(fanWriteOutcome(result))
				setDraft(null)
			}
			setArmed(null)
			setRefusal(null)
		},
		(err) => {
			if (err instanceof FanApiError) {
				const view = fanRefusal(err)
				setRefusal({ key: view.key, detail: view.detail })
			}
		}
	)

	const curve = draft ?? state?.curve ?? null

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
						<p className="mt-2 text-sm text-destructive">
							{REFUSAL_TEXT[refusal.key]}
							{refusal.detail && <span className="ms-2 font-mono text-xs opacity-70">{refusal.detail}</span>}
						</p>
					)}
				</>
			)}
		</ChartCard>
	)
}
