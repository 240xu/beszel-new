import { t } from "@lingui/core/macro"
import { MoreHorizontalIcon } from "lucide-react"
import { memo, useRef, useState } from "react"
import ChartTimeSelect from "@/components/charts/chart-time-select"
import { Button } from "@/components/ui/button"
import { DialogTitle } from "@/components/ui/dialog"
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet"
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

function MetricGrid({ rows }: { rows: MetricRow[] }) {
	const LABELS = labelCopy()
	const labelFor = (row: MetricRow): string => LABELS[row.key as StaticLabelKey] ?? row.label
	return (
		<div className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
			{rows.map((row) => (
				<div key={row.key}>
					<span className="text-muted-foreground">{labelFor(row)}: </span>
					<span className="font-mono">{row.value}</span>
					{row.notes?.map((note) => (
						<span key={note.kind} className="ms-1 text-xs text-muted-foreground">
							({noteText(note)})
						</span>
					))}
				</div>
			))}
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

	return (
		<Sheet open={open} onOpenChange={setOpen}>
			<DialogTitle className="sr-only">{t`beszel-new`}</DialogTitle>
			<SheetTrigger asChild>
				<Button title={t`View more`} variant="outline" size="icon" className="shrink-0">
					<MoreHorizontalIcon />
				</Button>
			</SheetTrigger>
			{hasOpened.current && (
				<SheetContent aria-describedby={undefined} className="overflow-auto w-200 !max-w-full p-4 sm:p-6">
					<ChartTimeSelect className="w-[calc(100%-2em)] bg-card" agentVersion={chartData.agentVersion} />

					{dptf.length > 0 && (
						<ChartCard
							key="plus-dptf"
							grid={grid}
							className="min-h-auto"
							title={t`Thermal Zones`}
							description={t`Intel DPTF sensors. A zone the collector could not read is left out rather than shown as zero.`}
						>
							<MetricGrid rows={dptf} />
						</ChartCard>
					)}

					{cpufreq.length > 0 && (
						<ChartCard
							key="plus-cpufreq"
							grid={grid}
							className="min-h-auto"
							title={t`CPU Frequency`}
							description={t`Per core, named by cpu number, plus the active tuning policy`}
						>
							<MetricGrid rows={cpufreq} />
						</ChartCard>
					)}

					{nvme.length > 0 && (
						<ChartCard
							key="plus-nvme"
							grid={grid}
							className="min-h-auto"
							title={t`NVMe Health`}
							description={t`SMART log. A field the drive omits reads as unknown, not as zero.`}
						>
							<MetricGrid rows={nvme} />
						</ChartCard>
					)}

					{wifi.length > 0 && (
						<ChartCard
							key="plus-wifi"
							grid={grid}
							className="min-h-auto"
							title={t`WiFi`}
							description={t`Radio link. Retry is a difference between two samples, not a counter reading.`}
						>
							<MetricGrid rows={wifi} />
						</ChartCard>
					)}

					{netall.length > 0 && (
						<ChartCard
							key="plus-netall"
							grid={grid}
							className="min-h-auto"
							title={t`All Interfaces`}
							description={t`Cumulative counters, including veth pairs and docker bridges`}
						>
							<MetricGrid rows={netall} />
						</ChartCard>
					)}

					{fan.length > 0 && (
						<ChartCard
							key="plus-fan"
							grid={grid}
							className="min-h-auto"
							title={t`Fan`}
							description={t`Duty programmed, duty applied and package temperature.`}
						>
							<MetricGrid rows={fan} />
						</ChartCard>
					)}

					{steps.length > 0 && (
						<ChartCard
							key="plus-fan-steps"
							grid={grid}
							className="min-h-auto"
							title={t`Firmware Fan Table`}
							description={t`The steps the firmware itself publishes. Power is the duty scaled by a fixed constant.`}
						>
							<FanStepTable rows={steps} />
						</ChartCard>
					)}
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
			className="min-h-auto"
			title={t`beszel-new`}
			description={t`Thermal zones, per core frequency, NVMe health, WiFi and fan`}
			cornerEl={<PlusSheet chartData={chartData} grid={grid} />}
		>
			<MetricGrid rows={rows} />
		</ChartCard>
	)
}
