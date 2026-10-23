import { t } from "@lingui/core/macro"
import { useStore } from "@nanostores/react"
import { LoaderCircleIcon, LockIcon } from "lucide-react"
import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react"
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { $router } from "@/components/router"
import { $allSystemsById } from "@/lib/stores"
import { UNKNOWN, fmtCelsius } from "@/lib/plus-metrics"
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
import { cn, hourWithSeconds } from "@/lib/utils"
import type { FanCurve, FanMode, FanState } from "@/types"

function outcomeCopy() {
	const OUTCOME_TEXT: Record<FanOutcomeKind, string> & { appliedDuty: string } = {
		applied: t`In force — the fan is running this now.`,
		pending_restart: t`Written to disk; the curve daemon has not picked it up.`,
		daemon_running: t`The curve daemon is running and is driving the fan from the curve on disk.`,
		daemon_stopped: t`The curve daemon is not running, so it is not driving the fan.`,
		appliedDuty: t`Applied Duty`,
	}

	const OUTCOME_BADGE: Record<FanOutcomeKind, string> & { held: string } = {
		applied: t`In force`,
		pending_restart: t`Awaiting restart`,
		daemon_running: t`Curve running`,
		daemon_stopped: t`Curve stopped`,
		held: t`In force`,
	}

	return { OUTCOME_TEXT, OUTCOME_BADGE }
}

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
		// A body the hub could not read is not a fan refusal. The panel build and the
		// hub disagree about the request shape, so the operator needs to update one of
		// them rather than inspect the fan.
		version_skew: t`This panel and the hub disagree about the request format, so nothing was sent to the fan. Update the hub or reload the page.`,
		unrecognised: t`The server refused this for a reason this build does not know.`,
	}

	return REFUSAL_TEXT
}

const CURVE_FIELDS: Array<{ key: keyof FanCurve; group: "temperature" | "duty"; locked?: boolean }> = [
	{ key: "idle", group: "duty" },
	{ key: "l0", group: "duty" },
	{ key: "t1", group: "temperature" },
	{ key: "l1", group: "duty" },
	{ key: "t2", group: "temperature" },
	{ key: "l2", group: "duty" },
	{ key: "t3", group: "temperature" },
	{ key: "l3", group: "duty" },
	{ key: "t4", group: "temperature", locked: true },
	{ key: "l4", group: "duty" },
	{ key: "t5", group: "temperature" },
	{ key: "l5", group: "duty" },
]

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

const STATUS_TONE = {
	success: "border-s-success bg-success-muted/60 text-success-text",
	warning: "border-s-warning bg-warning-muted/60 text-warning-text",
	danger: "border-s-destructive bg-destructive/10 text-destructive-text",
	info: "border-s-info bg-info-muted/60 text-info-text",
} as const

const CHIP_TONE = {
	success: "border-transparent bg-success-muted text-success-text",
	warning: "border-transparent bg-warning-muted text-warning-text",
	danger: "border-transparent bg-destructive/10 text-destructive-text",
	info: "border-transparent bg-info-muted text-info-text",
	muted: "border-transparent bg-muted text-muted-foreground",
} as const

type ReadState = "loading" | "ready" | "error"

function useFanState(intervalMs = 10_000) {
	const [state, setState] = useState<FanState | null>(null)
	const [status, setStatus] = useState<ReadState>("loading")
	const [error, setError] = useState<unknown>(null)
	const [lastUpdated, setLastUpdated] = useState<number | null>(null)
	const [nonce, setNonce] = useState(0)

	useEffect(() => {
		let live = true
		const read = () => {
			getFanState()
				.then((next) => {
					if (!live) return
					setState(next)
					setStatus("ready")
					setError(null)
					setLastUpdated(Date.now())
				})
				.catch((err) => {
					if (!live) return
					setStatus("error")
					setError(err)
				})
		}
		read()
		const timer = setInterval(read, intervalMs)
		return () => {
			live = false
			clearInterval(timer)
		}
	}, [intervalMs, nonce])

	const retry = useCallback(() => {
		setStatus("loading")
		setNonce((n) => n + 1)
	}, [])

	return { state, status, error, retry, lastUpdated }
}

function applySettled(
	settled: FanWriteSettled,
	setOutcome: (outcome: FanWriteOutcome | null) => void,
	setRefusal: (refusal: { key: FanRefusalKey; detail?: string } | null) => void,
	setOffline: (offline: boolean) => void,
	setUnreadable: (unreadable: boolean) => void
) {
	if (settled.ok) {
		setOutcome(settled.outcome)
		setRefusal(null)
		setOffline(false)
		setUnreadable(false)
		return
	}
	if (settled.dropBanner) setOutcome(null)
	setOffline(settled.offline)
	setUnreadable(settled.unknown)
	setRefusal(settled.refusal && { key: settled.refusal.key, detail: settled.refusal.detail })
}

function useFanWrite(onSettled: (settled: FanWriteSettled, write: FanWrite) => void) {
	const [busy, setBusy] = useState(false)
	const run = async (write: FanWrite) => {
		setBusy(true)
		try {
			onSettled(fanWriteSettled(await issueFanWrite(write)), write)
		} catch (err) {
			onSettled(fanWriteRefused(write, err), write)
		} finally {
			setBusy(false)
		}
	}
	return { busy, run }
}

function ArmedTarget({ write }: { write: FanWrite }) {
	if (write.kind === "level") return <>{`${write.level}%`}</>
	if (write.kind === "mode") return <>{write.mode === "auto" ? t`Return to the curve` : t`Take manual control`}</>
	return <>{t`the edited curve`}</>
}

function StatusChip({ tone, children }: { tone: keyof typeof CHIP_TONE; children: ReactNode }) {
	return (
		<Badge variant="outline" className={cn("rounded-full px-2.5 py-0.5 text-xs font-semibold", CHIP_TONE[tone])}>
			{children}
		</Badge>
	)
}

function StatusBar({
	tone,
	title,
	children,
	action,
}: {
	tone: keyof typeof STATUS_TONE
	title: ReactNode
	children: ReactNode
	action?: ReactNode
}) {
	return (
		<div
			role="status"
			aria-live="polite"
			className={cn(
				"flex flex-col gap-3 rounded-md border-s-4 p-4 sm:flex-row sm:items-center sm:justify-between",
				STATUS_TONE[tone]
			)}
		>
			<div className="min-w-0 space-y-1">
				<p className="text-sm font-semibold">{title}</p>
				<div className="text-sm opacity-90">{children}</div>
			</div>
			{action}
		</div>
	)
}

function ReadoutCell({ title, value, detail }: { title: ReactNode; value: ReactNode; detail?: ReactNode }) {
	return (
		<div className="rounded-lg border border-border bg-muted/30 p-4">
			<p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{title}</p>
			<p className="mt-2 font-mono text-2xl font-semibold tabular-nums text-foreground">{value}</p>
			{detail && <div className="mt-2 text-xs text-muted-foreground">{detail}</div>}
		</div>
	)
}

function ReadProblem({ stale, onRetry }: { stale: boolean; onRetry: () => void }) {
	// Retrying unmounts this alert, and the browser drops focus to <body> when the
	// focused button disappears. A keyboard user then has to tab from the top of the
	// page to reach the panel again, so hand focus to the retry control's replacement
	// rather than letting it fall on the floor.
	const retryRef = useRef<HTMLButtonElement>(null)
	const handleRetry = useCallback(() => {
		const restoreTo = retryRef.current
		onRetry()
		requestAnimationFrame(() => {
			if (restoreTo?.isConnected) restoreTo.focus()
		})
	}, [onRetry])
	return (
		<div
			role="alert"
			className={cn(
				"flex flex-col gap-3 rounded-md border-s-4 p-4 sm:flex-row sm:items-center sm:justify-between",
				stale ? STATUS_TONE.warning : STATUS_TONE.danger
			)}
		>
			<div className="min-w-0 space-y-1">
				<p className="text-sm font-semibold">{stale ? t`Stale` : t`Unavailable`}</p>
				<p className="text-sm opacity-90">
					{stale
						? t`The fan state could not be refreshed. Showing the last reading.`
						: t`The fan state could not be read.`}
				</p>
			</div>
			<Button ref={retryRef} className="w-full sm:w-auto" size="sm" variant="outline" onClick={handleRetry}>
				{t`Retry`}
			</Button>
		</div>
	)
}

function ReadingState() {
	return (
		<div
			role="status"
			aria-live="polite"
			className="flex items-center gap-3 rounded-md border border-border bg-muted/30 p-4 text-sm text-muted-foreground"
		>
			<LoaderCircleIcon className="size-4 animate-spin" aria-hidden="true" />
			{t`Reading the fan state…`}
		</div>
	)
}

function PanelHeader({
	title,
	description,
	host,
	right,
}: {
	title: string
	description: string
	host?: string
	right?: ReactNode
}) {
	return (
		<header className="flex flex-col gap-4 border-b border-border pb-5 sm:flex-row sm:items-start sm:justify-between">
			<div className="min-w-0 space-y-1.5">
				<div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
					<h3 className="text-card-title font-semibold tracking-tight">{title}</h3>
					{host && <span className="truncate text-sm text-muted-foreground">{host}</span>}
				</div>
				<p className="max-w-prose text-sm text-muted-foreground">{description}</p>
			</div>
			{right && <div className="flex shrink-0 flex-col items-start gap-2 sm:items-end">{right}</div>}
		</header>
	)
}

function RegressLine({ state }: { state: FanState }) {
	const regress = fanRegressView(state, Date.now())
	if (regress.kind === "counting_down") {
		return (
			<p className="text-sm text-muted-foreground">
				{t`A hand-set duty is on record. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.`}{" "}
				<span className="font-mono tabular-nums text-foreground">{regress.countdown}</span>
			</p>
		)
	}
	if (regress.kind === "no_deadline") {
		return (
			<p className="text-sm text-muted-foreground">
				{t`A hand-set duty is on record with no deadline on file. It stays in force: this agent writes no duty to hand it back, so use Return to the curve.`}
			</p>
		)
	}
	return (
		<p className="text-sm text-muted-foreground">
			{t`No hand-set duty is on record. One set before an agent restarted would still be driving the fan, and this panel cannot see it.`}
		</p>
	)
}

function CurveEditor({ curve, onChange }: { curve: FanCurve; onChange: (curve: FanCurve) => void }) {
	const LOCK_REASON = t`Full-speed backstop: the firmware carries this value, so it is fixed here.`

	const field = (key: keyof FanCurve, locked: boolean) => {
		const name = String(key)
		const temperature = key.startsWith("t")
		const unit = temperature ? "°C" : "%"
		const displayed = key === "idle" ? t`Idle` : `${temperature ? t`Temperature` : t`Duty`} ${name.toUpperCase()}`
		const id = `fan-curve-${name}`
		return (
			<div key={name} className="flex flex-col gap-1.5">
				<div className="flex items-center justify-between gap-2">
					<Label htmlFor={id} className="text-xs font-medium text-muted-foreground">
						{displayed}
					</Label>
					{locked && (
						<span className="inline-flex items-center gap-1 text-xs text-warning-text">
							<LockIcon className="size-3.5" aria-hidden="true" />
							{t`Locked`}
						</span>
					)}
				</div>
				<div className="relative">
					<Input
						id={id}
						type="number"
						inputMode="numeric"
						min={0}
						max={temperature ? 120 : 100}
						disabled={locked}
						value={curve[key]}
						className="pe-10 font-mono tabular-nums"
						onChange={(e) => onChange({ ...curve, [key]: Number(e.target.value) })}
					/>
					<span className="pointer-events-none absolute inset-y-0 end-3 flex items-center text-xs text-muted-foreground">
						{unit}
					</span>
				</div>
				<span className="text-xs tabular-nums text-muted-foreground">{`0–${temperature ? 120 : 100} ${unit}`}</span>
				{locked && <span className="text-xs text-warning-text">{LOCK_REASON}</span>}
			</div>
		)
	}

	return (
		<div className="flex flex-col gap-5">
			<div className="grid gap-5 lg:grid-cols-2">
				<fieldset className="space-y-3">
					<legend className="text-sm font-semibold text-foreground">{t`Temperature`}</legend>
					<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
						{CURVE_FIELDS.filter((entry) => entry.group === "temperature").map((entry) =>
							field(entry.key, entry.locked === true)
						)}
					</div>
				</fieldset>
				<fieldset className="space-y-3">
					<legend className="text-sm font-semibold text-foreground">{t`Duty`}</legend>
					<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
						{CURVE_FIELDS.filter((entry) => entry.group === "duty").map((entry) =>
							field(entry.key, entry.locked === true)
						)}
					</div>
				</fieldset>
			</div>
			<details className="rounded-md border border-border bg-muted/30 p-3">
				<summary className="cursor-pointer text-sm font-medium">{t`Also written unchanged:`}</summary>
				<dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 font-mono text-xs tabular-nums sm:grid-cols-3">
					{CURVE_CARRIED.map((key) => (
						<div key={String(key)} className="flex justify-between gap-2">
							<dt className="text-muted-foreground">{String(key)}</dt>
							<dd>{curve[key]}</dd>
						</div>
					))}
				</dl>
			</details>
		</div>
	)
}

function ModeSwitch({
	manual,
	busy,
	onAuto,
	onManual,
}: {
	manual: boolean
	busy: boolean
	onAuto: () => void
	onManual: () => void
}) {
	const item =
		"flex flex-1 flex-col items-center justify-center rounded-sm px-4 py-2 text-center transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50"
	return (
		<div
			role="group"
			aria-label={t`Mode`}
			className="inline-flex w-full rounded-md border border-border bg-muted/40 p-1 sm:w-auto"
		>
			<button
				type="button"
				aria-pressed={!manual}
				disabled={busy}
				onClick={onAuto}
				className={cn(
					item,
					!manual ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground"
				)}
			>
				<span className="text-sm font-medium">{t`Auto`}</span>
				{manual && <span className="text-xs opacity-80">{t`Return to the curve`}</span>}
			</button>
			<button
				type="button"
				aria-pressed={manual}
				disabled={busy}
				onClick={onManual}
				className={cn(
					item,
					manual ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground"
				)}
			>
				<span className="text-sm font-medium">{t`Manual`}</span>
				{!manual && <span className="text-xs opacity-80">{t`Take manual control`}</span>}
			</button>
		</div>
	)
}

function DutyLadder({
	appliedName,
	appliedLabel,
	appliedLevel,
	target,
	disabled,
	onChange,
}: {
	appliedName: string
	appliedLabel: string
	appliedLevel: number | null
	target: number
	disabled: boolean
	onChange: (duty: number) => void
}) {
	const percent = (value: number) => Math.max(0, Math.min(100, value))
	const index = Math.max(0, (FAN_DUTIES as readonly number[]).indexOf(target))

	const move = (event: KeyboardEvent<HTMLDivElement>) => {
		if (disabled) return
		let next = index
		if (event.key === "ArrowRight" || event.key === "ArrowUp") next = Math.min(FAN_DUTIES.length - 1, index + 1)
		else if (event.key === "ArrowLeft" || event.key === "ArrowDown") next = Math.max(0, index - 1)
		else if (event.key === "Home") next = 0
		else if (event.key === "End") next = FAN_DUTIES.length - 1
		else if (event.key === "PageUp") next = Math.min(FAN_DUTIES.length - 1, index + 2)
		else if (event.key === "PageDown") next = Math.max(0, index - 2)
		else return
		event.preventDefault()
		onChange(FAN_DUTIES[next])
	}

	const choose = (event: PointerEvent<HTMLDivElement>) => {
		if (disabled) return
		const track = event.currentTarget
		const rect = track.getBoundingClientRect()
		// Measure the click from the inline-start edge so the mapping follows
		// the writing direction (the right edge in RTL, the left edge in LTR).
		// happy-dom (DOM tests) does not cascade `dir` into computed style, so
		// consult the nearest [dir] attribute as well as the computed style.
		const rtl =
			track.closest("[dir=rtl]") !== null ||
			getComputedStyle(track).direction === "rtl"
		const at = rtl
			? ((rect.right - event.clientX) / rect.width) * 100
			: ((event.clientX - rect.left) / rect.width) * 100
		let nearest: number = FAN_DUTIES[0]
		for (const duty of FAN_DUTIES) {
			if (Math.abs(duty - at) < Math.abs(nearest - at)) nearest = duty
		}
		onChange(nearest)
	}

	return (
		<div className="space-y-2">
			<div className="flex items-baseline justify-between gap-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
				<span>{appliedName}</span>
				<span className="flex items-center gap-2 normal-case">
					{t`Target`}
					<Select value={String(target)} onValueChange={(value) => onChange(Number(value))} disabled={disabled}>
						<SelectTrigger className="h-8 w-24 font-mono text-sm tabular-nums" aria-label={t`Target duty`}>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{FAN_DUTIES.map((duty) => (
								<SelectItem key={duty} value={String(duty)}>
									{`${duty}%`}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</span>
			</div>
			<div
				role="slider"
				tabIndex={disabled ? -1 : 0}
				aria-label={t`Target duty`}
				aria-valuemin={FAN_DUTIES[0]}
				aria-valuemax={FAN_DUTIES[FAN_DUTIES.length - 1]}
				aria-valuenow={target}
				aria-valuetext={`${target}%`}
				aria-disabled={disabled}
				onKeyDown={move}
				onPointerDown={choose}
				className={cn(
					"relative h-12 w-full rounded-md border border-border bg-background outline-none",
					disabled ? "opacity-60" : "cursor-pointer focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
				)}
			>
				<span className="sr-only">{`${appliedName}: ${appliedLabel}`}</span>
				<span
					aria-hidden="true"
					className="absolute inset-y-2 inset-x-0 grid grid-cols-10 overflow-hidden rounded-sm border border-border"
				>
					{Array.from({ length: 10 }, (_, i) => (
						<span
							key={i}
							className={cn("border-e border-border/60 last:border-e-0", i * 10 < target ? "bg-signal-muted" : "bg-muted/40")}
						/>
					))}
				</span>
				{appliedLevel !== null && (
					<span
						aria-hidden="true"
						className="absolute inset-y-1 w-0.5 -translate-x-1/2 rtl:translate-x-1/2 bg-foreground"
						style={{ insetInlineStart: `${percent(appliedLevel)}%` }}
					/>
				)}
				<span
					aria-hidden="true"
					className="absolute -top-1 size-3 -translate-x-1/2 rtl:translate-x-1/2 rotate-45 border border-signal bg-signal"
					style={{ insetInlineStart: `${target}%` }}
				/>
			</div>
			<div className="flex justify-between font-mono text-xs tabular-nums text-muted-foreground">
				<span>0%</span>
				<span>25%</span>
				<span>50%</span>
				<span>75%</span>
				<span>100%</span>
			</div>
		</div>
	)
}

function OutcomeBanner({ outcome, onRestart }: { outcome: FanWriteOutcome; onRestart: () => void }) {
	const { OUTCOME_TEXT, OUTCOME_BADGE } = outcomeCopy()
	const tone = outcome.inForce ? "success" : outcome.needsRestart ? "warning" : "info"
	return (
		<StatusBar
			tone={tone}
			title={OUTCOME_BADGE[outcome.kind]}
			action={
				outcome.needsRestart && (
					<Button className="w-full sm:w-auto" size="sm" variant="outline" onClick={onRestart}>
						{t`Restart the curve daemon`}
					</Button>
				)
			}
		>
			{OUTCOME_TEXT[outcome.kind]}
		</StatusBar>
	)
}

// WriteFeedback renders the three ways a write can fail to land: a request that
// never reached the server, a refusal the server named, and the outcome banner.
// The level and curve panels rendered this block twice; keeping one copy means a
// fix to the retry wiring cannot land in only one of them.
function WriteFeedback({
	outcome,
	offline,
	unreadable,
	refusal,
	retryWrite,
	onRetry,
	onRestart,
}: {
	outcome: FanWriteOutcome | null
	offline: boolean
	unreadable: boolean
	refusal: { key: FanRefusalKey; detail?: string } | null
	retryWrite: FanWrite | null
	onRetry: (write: FanWrite) => void
	onRestart: () => void
}) {
	const REFUSAL_TEXT = refusalCopy()
	return (
		<>
			{outcome && <OutcomeBanner outcome={outcome} onRestart={onRestart} />}
			{unreadable && (
				<div
					role="alert"
					className={cn(
						"flex flex-col gap-3 rounded-md border-s-4 p-4 sm:flex-row sm:items-center sm:justify-between",
						STATUS_TONE.warning
					)}
				>
					<p className="text-sm">
						{t`The server answered, but this panel could not read the result. The change may be in force; check the current state before writing again.`}
					</p>
					{retryWrite && (
						<Button
							className="w-full sm:w-auto"
							size="sm"
							variant="outline"
							onClick={() => onRetry(retryWrite)}
						>
							{t`Retry`}
						</Button>
					)}
				</div>
			)}
			{offline && (
				<div
					role="alert"
					className={cn(
						"flex flex-col gap-3 rounded-md border-s-4 p-4 sm:flex-row sm:items-center sm:justify-between",
						STATUS_TONE.danger
					)}
				>
					<p className="text-sm">{t`The request never reached the server.`}</p>
					{retryWrite && (
						<Button
							className="w-full sm:w-auto"
							size="sm"
							variant="outline"
							onClick={() => onRetry(retryWrite)}
						>
							{t`Retry`}
						</Button>
					)}
				</div>
			)}
			{refusal && (
				<div
					role="alert"
					className={cn(
						"flex flex-col gap-3 rounded-md border-s-4 p-4 sm:flex-row sm:items-center sm:justify-between",
						STATUS_TONE.danger
					)}
				>
					<div className="min-w-0 space-y-1">
						<p className="text-sm">{REFUSAL_TEXT[refusal.key]}</p>
						{refusal.detail && <p className="font-mono text-xs opacity-70">{refusal.detail}</p>}
					</div>
					{retryWrite && (
						<Button
							className="w-full sm:w-auto"
							size="sm"
							variant="outline"
							onClick={() => onRetry(retryWrite)}
						>
							{t`Retry`}
						</Button>
					)}
				</div>
			)}
		</>
	)
}

function ArmedDialog({
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
		<AlertDialog open onOpenChange={(open) => !open && onCancel()}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{ARMED_TITLE[write.kind]}</AlertDialogTitle>
					<AlertDialogDescription>
						{t`Confirm to write this to the fan control path. There is no undo from this panel.`}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<div className="rounded-md border border-border bg-muted/40 p-3 font-mono text-sm tabular-nums">
					<ArmedTarget write={write} />
				</div>
				<AlertDialogFooter>
					<AlertDialogCancel autoFocus disabled={busy} onClick={onCancel}>
						{t`Cancel`}
					</AlertDialogCancel>
					<AlertDialogAction
						disabled={busy}
						onClick={(event) => {
							event.preventDefault()
							onFire()
						}}
					>
						{t`Confirm`}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	)
}

function modeIntent(mode: FanMode): FanWrite {
	return { kind: "mode", mode }
}

function useFanHost() {
	const page = useStore($router)
	const systems = useStore($allSystemsById)
	if (page?.route !== "system") return undefined
	return systems[page.params.id]?.name ?? page.params.id
}

export function PlusFanTab(_props: { grid: boolean; dataEmpty: boolean }) {
	const [armed, setArmed] = useState<FanWrite | null>(null)
	const [outcome, setOutcome] = useState<FanWriteOutcome | null>(null)
	const [refusal, setRefusal] = useState<{ key: FanRefusalKey; detail?: string } | null>(null)
	const [offline, setOffline] = useState(false)
	const [unreadable, setUnreadable] = useState(false)
	const [retryWrite, setRetryWrite] = useState<FanWrite | null>(null)
	const [targetDuty, setTargetDuty] = useState(60)

	const { state, status, retry, lastUpdated } = useFanState()
	const host = useFanHost()
	// An unreadable write answer says "the change may be in force". Once a later
	// read succeeds the panel is no longer guessing, so the warning must go even
	// though no second write happened. Without this it stayed up until the next
	// write settled, which is a banner outliving its own claim.
	useEffect(() => {
		if (status === "ready") setUnreadable(false)
	}, [status])
	const written = useFanWrite((settled, write) => {
		applySettled(settled, setOutcome, setRefusal, setOffline, setUnreadable)
		setArmed(null)
		setRetryWrite(settled.ok ? null : write)
		if (settled.ok) retry()
	})

	const { OUTCOME_BADGE, OUTCOME_TEXT } = outcomeCopy()
	const manual = state ? !state.pfand_active : false

	return (
		<Card className="col-span-full min-w-0 p-5 sm:p-6">
			<PanelHeader
				title={t`Fan Level`}
				host={host}
				description={t`Duty written to the firmware. Nothing writes it back afterwards: the firmware reads zero as zero percent, so returning the fan to its curve is yours to do.`}
				right={
					<>
						<div className="flex flex-wrap gap-1.5 sm:justify-end">
							{state && manual && state.level_read && (
								<StatusChip tone="success">{OUTCOME_BADGE.held}</StatusChip>
							)}
							{state && outcome?.needsRestart && (
								<StatusChip tone="warning">{OUTCOME_BADGE.pending_restart}</StatusChip>
							)}
							{state && state.thermal_floor_armed === true && (
								<StatusChip tone="warning">{t`Thermal guard holding`}</StatusChip>
							)}
							{state && status === "error" && <StatusChip tone="warning">{t`Stale`}</StatusChip>}
							{!state && status === "error" && <StatusChip tone="danger">{t`Unavailable`}</StatusChip>}
						</div>
						<p className="text-xs text-muted-foreground">
							<span>{t`Updated`}</span>{" "}
							<span className="font-mono tabular-nums text-foreground">
								{lastUpdated ? hourWithSeconds(lastUpdated) : UNKNOWN}
							</span>
						</p>
					</>
				}
			/>

			<div className="space-y-4 py-5">
				{status === "loading" && !state && <ReadingState />}
				{status === "error" && !state && <ReadProblem stale={false} onRetry={retry} />}
				{status === "ready" && !state && (
					<div role="alert" className={cn("rounded-md border-s-4 p-4", STATUS_TONE.danger)}>
						<p className="text-sm font-semibold">{t`Unavailable`}</p>
						<p className="text-sm opacity-90">{t`No fan state is on record.`}</p>
						<Button className="mt-3 w-full sm:w-auto" size="sm" variant="outline" onClick={retry}>
							{t`Retry`}
						</Button>
					</div>
				)}
				{status === "error" && state && <ReadProblem stale={true} onRetry={retry} />}
				{state && (
					<>
						{(state.health === "failsafe" || state.breaker_open) && (
							<output
								className={cn(
									"flex flex-col gap-2 rounded-md border-s-4 p-3 text-sm sm:flex-row sm:items-center sm:justify-between",
									state.breaker_open ? STATUS_TONE.danger : STATUS_TONE.warning
								)}
							>
								<span>
									{state.breaker_open
										? t`Fan writes are latched after repeated failures. The last verified duty is held until the control path recovers.`
										: t`The hub is acting on a reading it could not verify. The emergency duty is held until the sensor recovers.`}
								</span>
								<Button size="sm" variant="outline" onClick={retry}>
									{t`Retry`}
								</Button>
							</output>
						)}
						<div className="grid gap-3 sm:grid-cols-3">
							<ReadoutCell
								title={OUTCOME_TEXT.appliedDuty}
								value={fanLevelLabel(state)}
								detail={
									!state.level_read && (
										<span className="flex flex-wrap items-center gap-2">
											{t`The current duty could not be read; the fan's present duty is unknown.`}
											<Button size="sm" variant="ghost" onClick={retry}>
												{t`Retry`}
											</Button>
										</span>
									)
								}
							/>
							<ReadoutCell
								title={t({ message: "Package", context: "CPU package" })}
								value={state.pkg_temp_read ? fmtCelsius(state.pkg_temp_c) : UNKNOWN}
								detail={
									!state.pkg_temp_read && (
										<span className="flex flex-wrap items-center gap-2">
											{t`The package temperature could not be read.`}
											<Button size="sm" variant="ghost" onClick={retry}>
												{t`Retry`}
											</Button>
										</span>
									)
								}
							/>
							<ReadoutCell
								title={t`Mode`}
								value={state.pfand_active ? t`Curve running` : t`Curve stopped`}
								detail={state.pfand_active ? OUTCOME_TEXT.daemon_running : OUTCOME_TEXT.daemon_stopped}
							/>
						</div>

						<div className="flex flex-col gap-4 border-t border-border pt-5">
							<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
								<ModeSwitch
									manual={manual}
									busy={written.busy}
									onAuto={() => manual && setArmed(modeIntent("auto"))}
									onManual={() => !manual && setArmed(modeIntent("manual"))}
								/>
								{!manual && (
									<p className="text-xs text-muted-foreground">
										{t`The curve daemon is in control. Switch to manual before setting a duty by hand.`}
									</p>
								)}
							</div>

							<DutyLadder
								appliedName={OUTCOME_TEXT.appliedDuty}
								appliedLabel={fanLevelLabel(state)}
								appliedLevel={state.level_read ? state.level : null}
								target={targetDuty}
								disabled={!manual || written.busy}
								onChange={setTargetDuty}
							/>

							<div className="flex justify-end">
								<Button
									className="w-full sm:w-auto"
									disabled={!manual || written.busy}
									onClick={() => setArmed({ kind: "level", level: targetDuty })}
								>
									{t`Write this duty to the firmware`}
								</Button>
							</div>
						</div>

						<div className="space-y-3">
							<WriteFeedback
								outcome={outcome}
								offline={offline}
								unreadable={unreadable}
								refusal={refusal}
								retryWrite={retryWrite}
								onRetry={setArmed}
								onRestart={() => setArmed(modeIntent("auto"))}
							/>
						</div>

						<RegressLine state={state} />
					</>
				)}
			</div>

			{armed && (
				<ArmedDialog
					write={armed}
					busy={written.busy}
					onFire={() => written.run(armed)}
					onCancel={() => setArmed(null)}
				/>
			)}
		</Card>
	)
}

export function PlusFanCurveTab(_props: { grid: boolean; dataEmpty: boolean }) {
	const [draft, setDraft] = useState<FanCurve | null>(null)
	const [armed, setArmed] = useState<FanWrite | null>(null)
	const [outcome, setOutcome] = useState<FanWriteOutcome | null>(null)
	const [refusal, setRefusal] = useState<{ key: FanRefusalKey; detail?: string } | null>(null)
	const [offline, setOffline] = useState(false)
	const [unreadable, setUnreadable] = useState(false)
	const [retryWrite, setRetryWrite] = useState<FanWrite | null>(null)

	const { state, status, retry, lastUpdated } = useFanState()
	const host = useFanHost()
	// An unreadable write answer says "the change may be in force". Once a later
	// read succeeds the panel is no longer guessing, so the warning must go even
	// though no second write happened. Without this it stayed up until the next
	// write settled, which is a banner outliving its own claim.
	useEffect(() => {
		if (status === "ready") setUnreadable(false)
	}, [status])
	const written = useFanWrite((settled, write) => {
		applySettled(settled, setOutcome, setRefusal, setOffline, setUnreadable)
		setArmed(null)
		setRetryWrite(settled.ok ? null : write)
		if (settled.ok) {
			setDraft(null)
			retry()
		}
	})

	const curve = draft ?? state?.curve ?? null

	return (
		<Card className="col-span-full min-w-0 p-5 sm:p-6">
			<PanelHeader
				title={t`Temperature Curve`}
				host={host}
				description={t`pfand steps the duty by package temperature. Every field is written, including the ones this form does not offer.`}
				right={
					<p className="text-xs text-muted-foreground">
						<span>{t`Updated`}</span>{" "}
						<span className="font-mono tabular-nums text-foreground">
							{lastUpdated ? hourWithSeconds(lastUpdated) : UNKNOWN}
						</span>
					</p>
				}
			/>

			<div className="space-y-5 pt-5">
				{status === "loading" && !state && <ReadingState />}
				{status === "error" && !state && <ReadProblem stale={false} onRetry={retry} />}
				{status === "error" && state && <ReadProblem stale={true} onRetry={retry} />}
				{state && !curve && (
					<div role="alert" className={cn("flex flex-col gap-3 rounded-md border-s-4 p-4", STATUS_TONE.warning)}>
						<p className="text-sm font-semibold">{t`No curve on record.`}</p>
						<Button className="w-full sm:w-auto" size="sm" variant="outline" onClick={retry}>
							{t`Retry`}
						</Button>
					</div>
				)}
				{curve && (
					<>
						<CurveEditor curve={curve} onChange={setDraft} />

						{draft !== null && (
							<p className="text-xs text-muted-foreground">{t`Unsaved changes`}</p>
						)}

						<div className="flex flex-col gap-3 border-t border-border pt-5">
							<p className="text-sm text-muted-foreground">
								{t`The daemon picks the curve up on its next restart.`}
							</p>
							<div className="flex flex-col gap-2 sm:flex-row sm:justify-end">
								{draft !== null && (
									<Button
										className="w-full sm:w-auto"
										size="sm"
										variant="outline"
										disabled={written.busy}
										onClick={() => setDraft(null)}
									>
										{t`Discard changes`}
									</Button>
								)}
								<Button
									className="w-full sm:w-auto"
									size="sm"
									variant="destructive"
									disabled={written.busy || draft === null}
									onClick={() => setArmed({ kind: "curve", curve })}
								>
									{t`Write this curve to the disk`}
								</Button>
							</div>
						</div>

						<div className="space-y-3">
							<WriteFeedback
								outcome={outcome}
								offline={offline}
								unreadable={unreadable}
								refusal={refusal}
								retryWrite={retryWrite}
								onRetry={setArmed}
								onRestart={() => setArmed(modeIntent("auto"))}
							/>
						</div>
					</>
				)}
			</div>

			{armed && (
				<ArmedDialog
					write={armed}
					busy={written.busy}
					onFire={() => written.run(armed)}
					onCancel={() => setArmed(null)}
				/>
			)}
		</Card>
	)
}
