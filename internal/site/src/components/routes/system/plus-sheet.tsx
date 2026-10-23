import { t } from "@lingui/core/macro"
import { MoreHorizontalIcon } from "lucide-react"
import { memo, useRef, useState, type ReactNode } from "react"
import ChartTimeSelect from "@/components/charts/chart-time-select"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import {
	cpufreqRows,
	dptfRows,
	FAN_POWER_IS_NOMINAL,
	fanRows,
	fanStepRows,
	formatSpan,
	netRows,
	nvmeRows,
	plusAvailable,
	previewRows,
	wifiRows,
	type MetricRow,
	type RowNote,
	type StaticLabelKey,
} from "@/lib/plus-metrics"
import type { ChartData } from "@/types"
import { ChartCard } from "./chart-card"

/** Translated labels for the rows whose key names a fixed term. `row.key` is the
 *  only discriminator a row needs: rows keyed by a device name — a zone, an
 *  interface, a cpu number — are absent here and fall back to that name. Typed
 *  against `StaticLabelKey`, so a key the sheet fails to cover is a build error
 *  rather than an English string that quietly survives a locale. Built in the
 *  component's render like the fan panel's copy tables: a module-level table
 *  resolved `t` once at import, so switching language later left it behind. */
function labelCopy(): Record<StaticLabelKey, string> {
	const LABELS: Record<StaticLabelKey, string> = {
		"nvme.model": t`Model`,
		"nvme.temp_c": t`Temperature`,
		"nvme.percentage_used_pct": t`Percentage Used`,
		"nvme.available_spare_pct": t`Available Spare`,
		"nvme.unsafe_shutdowns": t`Unsafe Shutdowns`,
		"nvme.media_errors": t`Media Errors`,
		"nvme.power_on_hours": t`Power On Hours`,
		"nvme.power_cycles": t`Power Cycles`,
		"nvme.controller_busy_min": t`Controller Busy (min)`,
		"nvme.critical_comp_time_min": t`Critical Comp Time (min)`,
		"nvme.data_units_read": t`Data Read (cumulative)`,
		"nvme.data_units_written": t`Data Written (cumulative)`,
		"cpufreq.governor": t`Governor`,
		"cpufreq.epp": t`EPP`,
		"cpufreq.pstate_status": t`P-State`,
		"cpufreq.no_turbo": t`No Turbo`,
		"cpufreq.hwp_dynamic_boost": t`HWP Dynamic Boost`,
		"wifi.iface": t`Interface`,
		"wifi.signal_dbm": t`Signal`,
		"wifi.freq_mhz": t`Frequency`,
		"wifi.tx_mbit": t`Transmit Rate`,
		"wifi.retry_delta": t`Retry since previous sample`,
		"fan.set_level_pct": t`Programmed Level`,
		"fan.ec76_pct": t`Applied Duty`,
		"fan.temp_c": t`Temperature`,
		"fan.gov": t`Mode`,
	}

	return LABELS
}

/** The words a qualifier is said in. `plus-metrics.ts` names which qualifier a row
 *  carries and cannot speak here — it is pure and runs where `@lingui/core/macro`
 *  does not resolve — so the two halves meet in this switch. A qualifier it does
 *  not name has no branch, and `default` returns an empty string rather than
 *  rendering `[object Object]` on a screen. */
function noteText(note: RowNote): string {
	switch (note.kind) {
		case "stale":
			return t`stale`
		case "span":
			return t`over ${formatSpan(note.seconds)}`
		case "duty_span":
			return t`duty ${note.from} to ${note.to}`
	}
}

/** One metric as a stacked pair: the label is the quiet part and the value is
 *  what the eye lands on. Reading a wall of `label: value` in a 4-up grid made
 *  every reading the same weight, so an operator scanning for the one number that
 *  matters had to read all of them. A definition list is also the honest markup:
 *  these really are terms and their values. */
function MetricValue({ row }: { row: MetricRow }) {
	const LABELS = labelCopy()
	const label = LABELS[row.key as StaticLabelKey] ?? row.label
	return (
		<div className="min-w-0">
			<dt className="text-xs leading-snug text-muted-foreground">{label}</dt>
			<dd className="mt-0.5 font-mono text-[0.9375rem] leading-tight tabular-nums">
				{row.value}
				{row.notes?.map((note) => (
					<span key={note.kind} className="ms-1.5 font-sans text-xs font-normal text-muted-foreground">
						{noteText(note)}
					</span>
				))}
			</dd>
		</div>
	)
}

/** A run of related readings. `columns` is chosen by the caller from the data's
 *  shape, not applied uniformly: a device list wants wide cells, a handful of
 *  scalars wants narrow ones, and the same 4-up grid for both is what made the
 *  panel read as a dump. */
function MetricGroup({ rows, columns = 2 }: { rows: MetricRow[]; columns?: 2 | 3 | 4 }) {
	const cols = {
		2: "grid-cols-2",
		3: "grid-cols-2 sm:grid-cols-3",
		4: "grid-cols-2 sm:grid-cols-4",
	}[columns]
	return (
		<dl className={`grid gap-x-4 gap-y-3 ${cols}`}>
			{rows.map((row) => (
				<MetricValue key={row.key} row={row} />
			))}
		</dl>
	)
}

/** Device rows carry a name in the label and a compound reading in the value, so
 *  they get a full-width row each: a grid of two would put a long interface name
 *  and its two counters in half a line and truncate both. */
function DeviceList({ rows }: { rows: MetricRow[] }) {
	const LABELS = labelCopy()
	return (
		<dl className="divide-y divide-border/60">
			{rows.map((row) => {
				const label = LABELS[row.key as StaticLabelKey] ?? row.label
				return (
					<div key={row.key} className="flex items-baseline justify-between gap-4 py-1.5 first:pt-0 last:pb-0">
						<dt className="min-w-0 truncate font-mono text-xs text-muted-foreground" title={label}>
							{label}
						</dt>
						<dd className="shrink-0 font-mono text-[0.9375rem] tabular-nums">{row.value}</dd>
					</div>
				)
			})}
		</dl>
	)
}

/** NVMe's twelve fields are two different questions: how healthy the drive is,
 *  and how much work it has done. Stacking them in one eleven-cell grid hid the
 *  health numbers among the cumulative counters, which is backwards for a panel
 *  whose job is to show a failing disk at a glance. */
const NVME_HEALTH_KEYS = new Set([
	"nvme.model",
	"nvme.temp_c",
	"nvme.percentage_used_pct",
	"nvme.available_spare_pct",
	"nvme.media_errors",
	"nvme.critical_comp_time_min",
])

function NvmeBody({ rows }: { rows: MetricRow[] }) {
	const health = rows.filter((row) => NVME_HEALTH_KEYS.has(row.key))
	const wear = rows.filter((row) => !NVME_HEALTH_KEYS.has(row.key))
	return (
		<div className="space-y-4">
			<MetricGroup rows={health} columns={3} />
			<div>
				<h4 className="mb-2 text-xs font-medium text-muted-foreground">{t`Lifetime counters`}</h4>
				<MetricGroup rows={wear} columns={4} />
			</div>
		</div>
	)
}

function FanStepTable({ rows }: { rows: ReturnType<typeof fanStepRows> }) {
	return (
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>{t`Duty`}</TableHead>
					<TableHead>{FAN_POWER_IS_NOMINAL ? t`Power (firmware nominal)` : t`Power`}</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{rows.map((row) => (
					<TableRow key={row.key}>
						<TableCell className="font-mono">{row.duty}</TableCell>
						<TableCell className="font-mono">{row.power}</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	)
}

const PlusSheet = memo(function PlusSheet({ chartData, grid }: { chartData: ChartData; grid: boolean }) {
	const [open, setOpen] = useState(false)
	const hasOpened = useRef(false)

	if (open && !hasOpened.current) {
		hasOpened.current = true
	}

	if (!plusAvailable(chartData.systemStats)) {
		return null
	}

	const records = chartData.systemStats
	const nowSeconds = Math.floor(Date.now() / 1000)
	const fan = fanRows(records, nowSeconds)
	const dptf = dptfRows(records)
	const cpufreq = cpufreqRows(records)
	const nvme = nvmeRows(records)
	const wifi = wifiRows(records)
	const netall = netRows(records)
	const steps = hasOpened.current ? fanStepRows(records) : []

	const sections: Array<{ id: string; title: string; description: string; body: ReactNode }> = []
	if (dptf.length > 0) {
		sections.push({
			id: "plus-thermal-zones",
			title: t`Thermal Zones`,
			description: t`Intel DPTF sensors. A zone the collector could not read is left out rather than shown as zero.`,
			body: <DeviceList rows={dptf} />,
		})
	}
	if (cpufreq.length > 0) {
		sections.push({
			id: "plus-cpu-frequency",
			title: t`CPU Frequency`,
			description: t`Per core, named by cpu number, plus the active tuning policy`,
			body: <MetricGroup rows={cpufreq} columns={4} />,
		})
	}
	if (nvme.length > 0) {
		sections.push({
			id: "plus-nvme-health",
			title: t`NVMe Health`,
			description: t`SMART log. A field the drive omits reads as unknown, not as zero.`,
			body: <NvmeBody rows={nvme} />,
		})
	}
	if (wifi.length > 0) {
		sections.push({
			id: "plus-wifi",
			title: t`WiFi`,
			description: t`Radio link. Retry is a difference between two samples, not a counter reading.`,
			body: <MetricGroup rows={wifi} columns={2} />,
		})
	}
	if (netall.length > 0) {
		sections.push({
			id: "plus-all-interfaces",
			title: t`All Interfaces`,
			description: t`Cumulative counters, including veth pairs and docker bridges`,
			body: <DeviceList rows={netall} />,
		})
	}
	if (fan.length > 0) {
		sections.push({
			id: "plus-fan",
			title: t`Fan`,
			description: t`Duty programmed, duty applied and package temperature.`,
			body: <MetricGroup rows={fan} columns={2} />,
		})
	}
	if (steps.length > 0) {
		sections.push({
			id: "plus-firmware-fan-table",
			title: t`Firmware Fan Table`,
			description: t`The steps the firmware itself publishes. Power is the duty scaled by a fixed constant.`,
			body: <FanStepTable rows={steps} />,
		})
	}

	return (
		<Sheet open={open} onOpenChange={setOpen}>
			<SheetTrigger asChild>
				<Button title={t`View more`} aria-label={t`View more`} variant="outline" size="icon" className="shrink-0">
					<MoreHorizontalIcon />
				</Button>
			</SheetTrigger>
			{hasOpened.current && (
				<SheetContent className="h-full w-full overflow-y-auto bg-popover p-4 shadow-xl sm:max-w-3xl sm:p-6">
					<SheetHeader className="gap-1 p-0 pe-10 text-start">
						<SheetTitle className="text-xl">{t`Extended metrics`}</SheetTitle>
						<SheetDescription>
							{t`Every value beyond the preview card. A reading the agent did not report is absent, not zero.`}
						</SheetDescription>
					</SheetHeader>

					<ChartTimeSelect className="w-[calc(100%-2em)] bg-card" agentVersion={chartData.agentVersion} />

					{/* A contents strip, not a second row of links competing with the
					    sections below: it is the only navigation in a long sheet, so it
					    gets its own surface and a horizontal scroll instead of wrapping
					    into an untidy block of text on a phone. */}
					<nav
						aria-label={t`Sections`}
						className="-mx-1 flex snap-x gap-1 overflow-x-auto px-1 pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
					>
						{sections.map((section) => (
							<a
								key={section.id}
								href={`#${section.id}`}
								className="shrink-0 snap-start rounded-md border border-border/60 px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:border-border hover:bg-muted/50 hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
							>
								{section.title}
							</a>
						))}
					</nav>

					{/* Sections are separated by a rule and breathing room rather than
					    eleven identical tinted cards. Equal-weight cards were what made
					    this read as one undifferentiated dump; a rule establishes the
					    sequence without inventing hierarchy that the data does not have. */}
					<div className="divide-y divide-border/70">
						{sections.map((section) => (
							<section key={section.id} id={section.id} className="scroll-mt-4 py-5 first:pt-1 last:pb-0">
								<ChartCard
									grid={grid}
									autoHeight
									title={section.title}
									description={section.description}
									className="border-0 bg-transparent p-0 shadow-none"
									contentClassName="ps-0 -ms-0 -me-0"
								>
									{section.body}
								</ChartCard>
							</section>
						))}
					</div>
				</SheetContent>
			)}
		</Sheet>
	)
})

export default PlusSheet

export function PlusChart({ chartData, grid }: { chartData: ChartData; grid: boolean }) {
	const records = chartData.systemStats
	const rows = previewRows(records, Math.floor(Date.now() / 1000))
	// No snapshot, or a snapshot with nothing in it: a card with a title and no
	// content. The other charts on this page return null rather than draw one.
	if (!plusAvailable(records) || rows.length === 0) {
		return null
	}

	return (
		<ChartCard
			grid={grid}
			autoHeight
			title={t`beszel-new`}
			description={t`Thermal zones, per core frequency, NVMe health, WiFi and fan`}
			cornerEl={<PlusSheet chartData={chartData} grid={grid} />}
		>
			<MetricGroup rows={rows} columns={2} />
		</ChartCard>
	)
}
