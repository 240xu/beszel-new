import { plural } from "@lingui/core/macro"
import { Trans, useLingui } from "@lingui/react/macro"
import { ChevronRightSquareIcon, Settings2Icon } from "lucide-react"
import type { ReactNode } from "react"
import ChartTimeSelect from "@/components/charts/chart-time-select"
import { RelativeTimeCell } from "@/components/systems-table/relative-time-cell"
import { SystemStatusBadge } from "@/components/system-status"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { WebSocketIcon } from "@/components/ui/icons"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ConnectionType, connectionTypeLabels, Os } from "@/lib/enums"
import { formatBytes, getHostDisplayValue, secondsToUptimeString, toFixedFloat } from "@/lib/utils"
import type { ChartData, SystemDetailsRecord, SystemRecord } from "@/types"

const OS_NAMES: Record<Os, string> = {
	[Os.Linux]: "Linux",
	[Os.Darwin]: "macOS",
	[Os.Windows]: "Windows",
	[Os.FreeBSD]: "FreeBSD",
}

function MetricCell({
	label,
	value,
	unit,
	helper,
	meter,
}: {
	label: string
	value: ReactNode
	unit?: string
	helper?: ReactNode
	meter?: number
}) {
	return (
		<div className="min-w-0 rounded-md bg-muted/30 px-3 py-3">
			<div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">{label}</div>
			<div className="mt-1 flex items-baseline gap-1 tabular-nums">
				<span className="text-2xl font-semibold tracking-[-0.02em] sm:text-3xl">{value}</span>
				{unit && <span className="text-sm text-muted-foreground">{unit}</span>}
			</div>
			{typeof meter === "number" && (
				<div className="mt-2 h-1 overflow-hidden rounded-full bg-muted">
					<div className="h-full rounded-full bg-foreground/70" style={{ width: `${Math.min(100, Math.max(0, meter))}%` }} />
				</div>
			)}
			<div className="mt-1.5 truncate text-xs text-muted-foreground">{helper}</div>
		</div>
	)
}

export default function InfoBar({
	system,
	chartData,
	grid,
	setGrid,
	displayMode,
	setDisplayMode,
	details,
}: {
	system: SystemRecord
	chartData: ChartData
	grid: boolean
	setGrid: (grid: boolean) => void
	displayMode: "default" | "tabs"
	setDisplayMode: (mode: "default" | "tabs") => void
	details: SystemDetailsRecord | null
}) {
	const { t } = useLingui()

	const cpu = system.info?.cpu ?? 0
	const memory = system.info?.mp ?? 0
	const disk = system.info?.dp ?? 0
	const uptime = system.info?.u ?? 0
	const cores = details?.cores ?? system.info?.c ?? 0
	const threads = details?.threads ?? system.info?.t ?? 0
	const cpuModel = details?.cpu ?? system.info?.m
	const os = details?.os ?? system.info?.os
	const osName = details?.os_name || (os !== undefined ? OS_NAMES[os] : undefined)
	const arch = details?.arch
	const kernel = details?.kernel ?? system.info?.k
	const totalMemory = details?.memory

	const lastStats = chartData.systemStats.at(-1)?.stats
	const sentBytes = lastStats?.b?.[0]
	const receivedBytes = lastStats?.b?.[1]
	const netTotal = sentBytes !== undefined && receivedBytes !== undefined ? sentBytes + receivedBytes : undefined
	const netValue = netTotal !== undefined ? formatBytes(netTotal, true) : undefined
	const sentValue = sentBytes !== undefined ? formatBytes(sentBytes, true) : undefined
	const receivedValue = receivedBytes !== undefined ? formatBytes(receivedBytes, true) : undefined

	const memoryTotal = totalMemory ? formatBytes(totalMemory) : undefined
	const connectionType = system.info?.ct as ConnectionType | undefined

	return (
		<Card className="p-4 shadow-none sm:p-5">
			<div className="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
				<div className="min-w-0">
					<h1 className="truncate text-2xl font-semibold tracking-[-0.02em] sm:text-[1.75rem]">{system.name}</h1>
					<div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-sm text-muted-foreground">
						<Tooltip>
							<TooltipTrigger asChild>
								<span className="inline-flex">
									<SystemStatusBadge status={system.status} />
								</span>
							</TooltipTrigger>
							{connectionType ? (
								<TooltipContent>
									<div className="flex items-center gap-1">
										{connectionType === ConnectionType.WebSocket ? (
											<WebSocketIcon className="size-4" />
										) : (
											<ChevronRightSquareIcon className="size-4" strokeWidth={2} />
										)}
										{connectionTypeLabels[connectionType]}
									</div>
								</TooltipContent>
							) : null}
						</Tooltip>

						<span className="font-mono text-xs text-muted-foreground/90">{getHostDisplayValue(system)}</span>

						{(osName || arch) && (
							<span className="inline-flex min-w-0 items-center gap-1.5" title={kernel}>
								<span className="truncate">{osName}</span>
								{arch && <span className="text-muted-foreground/70">{arch}</span>}
							</span>
						)}

						{uptime > 0 && (
							<span className="inline-flex items-center gap-1.5 whitespace-nowrap">
								<Trans>Uptime</Trans>
								<span className="tabular-nums text-foreground/80">{secondsToUptimeString(uptime)}</span>
							</span>
						)}

						{system.updated && (
							<span className="inline-flex items-center gap-1.5 whitespace-nowrap">
								<Trans>Updated</Trans>
								<RelativeTimeCell value={system.updated} className="text-foreground/80" />
							</span>
						)}
					</div>
				</div>

				<div className="flex w-full shrink-0 flex-wrap items-center gap-2 xl:w-auto xl:justify-end">
					<ChartTimeSelect className="min-w-0 flex-1 sm:w-40 sm:flex-none" agentVersion={chartData.agentVersion} />
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button aria-label={t`Settings`} variant="outline" size="icon" className="p-0 text-primary">
								<Settings2Icon className="size-4 opacity-90" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="min-w-44">
							<DropdownMenuLabel className="px-3.5">
								<Trans context="Layout display options">Display</Trans>
							</DropdownMenuLabel>
							<DropdownMenuSeparator />
							<DropdownMenuRadioGroup
								className="px-1 pb-1"
								value={displayMode}
								onValueChange={(v) => setDisplayMode(v as "default" | "tabs")}
							>
								<DropdownMenuRadioItem value="default" onSelect={(e) => e.preventDefault()}>
									<Trans context="Default system layout option">Default</Trans>
								</DropdownMenuRadioItem>
								<DropdownMenuRadioItem value="tabs" onSelect={(e) => e.preventDefault()}>
									<Trans context="Tabs system layout option">Tabs</Trans>
								</DropdownMenuRadioItem>
							</DropdownMenuRadioGroup>
							<DropdownMenuSeparator />
							<DropdownMenuLabel className="px-3.5">
								<Trans>Chart width</Trans>
							</DropdownMenuLabel>
							<DropdownMenuSeparator />
							<DropdownMenuRadioGroup
								className="px-1 pb-1"
								value={grid ? "grid" : "full"}
								onValueChange={(v) => setGrid(v === "grid")}
							>
								<DropdownMenuRadioItem value="grid" onSelect={(e) => e.preventDefault()}>
									<Trans>Grid</Trans>
								</DropdownMenuRadioItem>
								<DropdownMenuRadioItem value="full" onSelect={(e) => e.preventDefault()}>
									<Trans>Full</Trans>
								</DropdownMenuRadioItem>
							</DropdownMenuRadioGroup>
						</DropdownMenuContent>
					</DropdownMenu>
				</div>
			</div>

			<div className="mt-4 grid grid-cols-2 gap-2 border-t border-border pt-4 sm:gap-3 lg:grid-cols-4">
				<MetricCell
					label={t`CPU`}
					value={toFixedFloat(cpu, cpu >= 10 ? 1 : 2)}
					unit="%"
					meter={cpu}
					helper={
						cpuModel ||
						`${plural(cores, { one: "# core", other: "# cores" })} / ${plural(threads, { one: "# thread", other: "# threads" })}`
					}
				/>
				<MetricCell
					label={t`Memory`}
					value={toFixedFloat(memory, memory >= 10 ? 1 : 2)}
					unit="%"
					meter={memory}
					helper={memoryTotal ? `${t`Total`}: ${toFixedFloat(memoryTotal.value, memoryTotal.value >= 10 ? 1 : 2)} ${memoryTotal.unit}` : undefined}
				/>
				<MetricCell
					label={t`Disk`}
					value={toFixedFloat(disk, disk >= 10 ? 1 : 2)}
					unit="%"
					meter={disk}
					helper={system.info?.rdn || t`Root`}
				/>
				<MetricCell
					label={t`Network`}
					value={netValue ? toFixedFloat(netValue.value, netValue.value >= 10 ? 1 : 2) : "—"}
					unit={netValue?.unit}
					helper={
						sentValue && receivedValue ? (
							<span className="inline-flex gap-1">
								<Trans>Sent</Trans> {toFixedFloat(sentValue.value, 1)} {sentValue.unit}
								<span aria-hidden="true">·</span>
								<Trans>Received</Trans> {toFixedFloat(receivedValue.value, 1)} {receivedValue.unit}
							</span>
						) : undefined
					}
				/>
			</div>
		</Card>
	)
}
