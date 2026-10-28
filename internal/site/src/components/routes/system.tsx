import { lazy, memo, Suspense, useEffect, useRef, useState } from "react"
import { Trans } from "@lingui/react/macro"
import { compareSemVer, parseSemVer, supportsNetworkMonitors } from "@/lib/utils"
import { SystemStatus } from "@/lib/enums"
import type { GPUData } from "@/types"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import InfoBar from "./system/info-bar"
import { useSystemData } from "./system/use-system-data"
import { CpuChart, ContainerCpuChart } from "./system/charts/cpu-charts"
import { MemoryChart, ContainerMemoryChart, SwapChart } from "./system/charts/memory-charts"
import { RootDiskCharts, ExtraFsCharts } from "./system/charts/disk-charts"
import { ZfsCharts } from "./system/charts/storage-pool-charts"
import { BandwidthChart, ContainerNetworkChart } from "./system/charts/network-charts"
import { TemperatureChart, FanChart, BatteryChart } from "./system/charts/sensor-charts"
import { PlusChart } from "./system/plus-sheet"
import { plusAvailable } from "@/lib/plus-metrics"
import { WiFiChart } from "./system/charts/wifi-chart"
import { GpuPowerChart, GpuCharts } from "./system/charts/gpu-charts"
import {
	LazyContainersTable,
	LazyNetworkMonitorsTable,
	LazyPackageUpdatesTable,
	LazySmartTable,
	LazySystemdTable,
	LazyZfsTable,
} from "./system/lazy-tables"
import { LoadAverageChart } from "./system/charts/load-average-chart"
import { ContainerIcon, CpuIcon, HardDriveIcon, NetworkIcon, PackageIcon, TerminalSquareIcon } from "lucide-react"
import { GpuIcon } from "../ui/icons"

const PlusFanTab = lazy(() => import("./system/plus-fan-tab").then((m) => ({ default: m.PlusFanTab })))
const SystemdTable = lazy(() => import("../systemd-table/systemd-table"))
const ContainersTable = lazy(() => import("../containers-table/containers-table"))

const SEMVER_0_14_0 = parseSemVer("0.14.0")
const SEMVER_0_15_0 = parseSemVer("0.15.0")

const tabTriggerClass =
	"inline-flex h-11 min-h-11 w-auto items-center justify-center gap-1.5 whitespace-nowrap rounded-none border-b-2 border-transparent px-3 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted/40 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 data-[state=active]:border-foreground data-[state=active]:bg-transparent data-[state=active]:text-foreground data-[state=active]:shadow-none"

export default memo(function SystemDetail({ id }: { id: string }) {
	const systemData = useSystemData(id)

	const {
		system,
		systemStats,
		containerData,
		chartData,
		containerChartConfigs,
		details,
		grid,
		setGrid,
		displayMode,
		setDisplayMode,
		activeTab,
		setActiveTab,
		mountedTabs,
		tabsRef,
		maxValues,
		isLongerChart,
		showMax,
		dataEmpty,
		isPodman,
		lastGpus,
		hasGpuData,
		hasGpuEnginesData,
		hasGpuPowerData,
	} = systemData

	// extra margin to add to bottom of page, specifically for temperature chart,
	// where the tooltip can go past the bottom of the page if lots of sensors
	const [pageBottomExtraMargin, setPageBottomExtraMargin] = useState(0)
	const tabsListRef = useRef<HTMLDivElement>(null)

	// Keep the active tab visible when the tab strip scrolls horizontally.
	useEffect(() => {
		const list = tabsListRef.current
		const active = list?.querySelector<HTMLElement>('[data-state="active"]')
		if (!list || !active || list.scrollWidth <= list.clientWidth + 1) {
			return
		}
		const listRect = list.getBoundingClientRect()
		const activeRect = active.getBoundingClientRect()
		const left = list.scrollLeft + activeRect.left - listRect.left - (listRect.width - activeRect.width) / 2
		const behavior = window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth"
		list.scrollTo({ left, behavior })
	}, [activeTab])

	if (!system.id) {
		return null
	}

	const hasContainers = containerData.length > 0
	const maybeHasSmartData = compareSemVer(chartData.agentVersion, SEMVER_0_15_0) >= 0
	const hasContainersTable = hasContainers && compareSemVer(chartData.agentVersion, SEMVER_0_14_0) >= 0
	const hasSystemd = system.info.sv
	const hasGpu = hasGpuData || hasGpuPowerData
	const hasZfs = Object.keys(systemStats.at(-1)?.stats?.z ?? {}).length > 0
	const hasNetworkMonitors = supportsNetworkMonitors(system)
	// counts key the table so it refetches the list only after a new check
	const packageUpdates = system.status === SystemStatus.Up && system.info.pu?.[0] ? system.info.pu.join(",") : ""

	// keep tabsRef in sync for keyboard navigation
	const tabs = ["core", "network", "disk"]
	if (hasGpu) tabs.push("gpu")
	if (hasContainers) tabs.push("containers")
	if (hasSystemd) tabs.push("services")
	if (packageUpdates) tabs.push("updates")
	tabsRef.current = tabs

	// shared chart props
	const coreProps = { chartData, grid, dataEmpty, showMax, isLongerChart, maxValues }

	function defaultLayout() {
		return (
			<>
				{/* main charts */}
				<div className="grid grid-cols-1 gap-4 xl:grid-cols-2 xl:gap-5">
					<CpuChart {...coreProps} />

					{hasContainers && (
						<ContainerCpuChart
							chartData={chartData}
							grid={grid}
							dataEmpty={dataEmpty}
							isPodman={isPodman}
							cpuConfig={containerChartConfigs.cpu}
						/>
					)}

					<MemoryChart {...coreProps} />

					{hasContainers && (
						<ContainerMemoryChart
							chartData={chartData}
							grid={grid}
							dataEmpty={dataEmpty}
							isPodman={isPodman}
							memoryConfig={containerChartConfigs.memory}
						/>
					)}

					<RootDiskCharts systemData={systemData} />

					<BandwidthChart {...coreProps} systemStats={systemStats} />

					{hasContainers && (
						<ContainerNetworkChart
							chartData={chartData}
							grid={grid}
							dataEmpty={dataEmpty}
							isPodman={isPodman}
							networkConfig={containerChartConfigs.network}
						/>
					)}

					<SwapChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} systemStats={systemStats} />

					<LoadAverageChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} />

					<TemperatureChart {...coreProps} />

					<FanChart {...coreProps} />
					<PlusChart chartData={chartData} grid={grid} />
					{plusAvailable(systemStats) && (
						<div className="grid gap-4 xl:col-span-2 xl:gap-5">
							<Suspense fallback={null}>
								<PlusFanTab grid={grid} dataEmpty={dataEmpty} />
							</Suspense>
						</div>
					)}

					<BatteryChart system={system} {...coreProps} />
					<WiFiChart system={system} {...coreProps} />

					{hasGpuPowerData && <GpuPowerChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} />}
				</div>

				{hasGpuData && lastGpus && (
					<GpuCharts
						chartData={chartData}
						grid={grid}
						dataEmpty={dataEmpty}
						lastGpus={lastGpus as Record<string, GPUData>}
						hasGpuEnginesData={hasGpuEnginesData}
					/>
				)}

				<ExtraFsCharts systemData={systemData} />

				{hasZfs && <ZfsCharts systemData={systemData} />}

				{hasZfs && <LazyZfsTable systemId={system.id} />}

				{maybeHasSmartData && <LazySmartTable systemId={system.id} />}

				{hasContainersTable && <LazyContainersTable systemId={system.id} />}

				{hasSystemd && <LazySystemdTable systemId={system.id} />}

				{packageUpdates && <LazyPackageUpdatesTable systemId={system.id} counts={packageUpdates} />}

				{hasNetworkMonitors && <LazyNetworkMonitorsTable systemId={system.id} />}
			</>
		)
	}

	function tabbedLayout() {
		return (
			<Tabs value={activeTab} onValueChange={setActiveTab} className="contents">
				<TabsList
					ref={tabsListRef}
					className="flex h-auto w-full flex-wrap justify-start gap-x-1 overflow-x-auto rounded-none border-b border-border bg-transparent p-0 shadow-none"
				>
					<TabsTrigger value="core" className={tabTriggerClass}>
						<CpuIcon className="size-3.5" />
						<Trans context="Core system metrics">Core</Trans>
					</TabsTrigger>
					<TabsTrigger value="network" className={tabTriggerClass}>
						<NetworkIcon className="size-3.5" />
						<Trans>Network</Trans>
					</TabsTrigger>
					<TabsTrigger value="disk" className={tabTriggerClass}>
						<HardDriveIcon className="size-3.5" />
						<Trans>Disk</Trans>
					</TabsTrigger>
					{hasGpu && (
						<TabsTrigger value="gpu" className={tabTriggerClass}>
							<GpuIcon className="size-3.5" />
							<Trans>GPU</Trans>
						</TabsTrigger>
					)}
					{hasContainers && (
						<TabsTrigger value="containers" className={tabTriggerClass}>
							<ContainerIcon className="size-3.5" />
							<Trans>Containers</Trans>
						</TabsTrigger>
					)}
					{hasSystemd && (
						<TabsTrigger value="services" className={tabTriggerClass}>
							<TerminalSquareIcon className="size-3.5" />
							<Trans>Services</Trans>
						</TabsTrigger>
					)}
					{packageUpdates && (
						<TabsTrigger value="updates" className={tabTriggerClass}>
							<PackageIcon className="size-3.5" />
							<Trans>Updates</Trans>
						</TabsTrigger>
					)}
				</TabsList>

				<TabsContent value="core" forceMount className={activeTab === "core" ? "contents" : "hidden"}>
					<div className="grid grid-cols-1 gap-4 xl:grid-cols-2 xl:gap-5">
						<CpuChart {...coreProps} />
						<LoadAverageChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} />
						<MemoryChart {...coreProps} />
						<SwapChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} systemStats={systemStats} />
						<TemperatureChart {...coreProps} setPageBottomExtraMargin={setPageBottomExtraMargin} />
						<FanChart {...coreProps} />
						<PlusChart chartData={chartData} grid={grid} />
						{plusAvailable(systemStats) && (
							<div className="grid gap-4 xl:col-span-2 xl:gap-5">
								<Suspense fallback={null}>
									<PlusFanTab grid={grid} dataEmpty={dataEmpty} />
								</Suspense>
							</div>
						)}
						<BatteryChart system={system} {...coreProps} />
						{pageBottomExtraMargin > 0 && <div style={{ marginBottom: pageBottomExtraMargin }}></div>}
					</div>
				</TabsContent>

				<TabsContent value="network" forceMount className={activeTab === "network" ? "contents" : "hidden"}>
					{mountedTabs.has("network") && (
						<>
							<div className="grid xl:grid-cols-2 gap-4">
								<BandwidthChart {...coreProps} systemStats={systemStats} />
								<WiFiChart system={system} {...coreProps} />
							</div>
							{hasNetworkMonitors && <LazyNetworkMonitorsTable systemId={system.id} />}
						</>
					)}
				</TabsContent>

				<TabsContent value="disk" forceMount className={activeTab === "disk" ? "contents" : "hidden"}>
					{mountedTabs.has("disk") && (
						<>
							<div className="grid xl:grid-cols-2 gap-4">
								<RootDiskCharts systemData={systemData} />
							</div>
							<ExtraFsCharts systemData={systemData} />
							{hasZfs && <ZfsCharts systemData={systemData} />}
							{hasZfs && <LazyZfsTable systemId={system.id} />}
							{maybeHasSmartData && <LazySmartTable systemId={system.id} />}
						</>
					)}
				</TabsContent>

				{hasGpu && (
					<TabsContent value="gpu" forceMount className={activeTab === "gpu" ? "contents" : "hidden"}>
						<GpuCharts
							chartData={chartData}
							grid={grid}
							dataEmpty={dataEmpty}
							lastGpus={(lastGpus ?? {}) as Record<string, GPUData>}
							hasGpuEnginesData={hasGpuEnginesData}
						>
							{hasGpuPowerData && <GpuPowerChart chartData={chartData} grid={grid} dataEmpty={dataEmpty} />}
						</GpuCharts>
					</TabsContent>
				)}

				{hasContainers && (
					<TabsContent value="containers" forceMount className={activeTab === "containers" ? "contents" : "hidden"}>
						{mountedTabs.has("containers") && (
							<>
								<div className="grid xl:grid-cols-2 gap-4">
									<ContainerCpuChart
										chartData={chartData}
										grid={grid}
										dataEmpty={dataEmpty}
										isPodman={isPodman}
										cpuConfig={containerChartConfigs.cpu}
									/>
									<ContainerMemoryChart
										chartData={chartData}
										grid={grid}
										dataEmpty={dataEmpty}
										isPodman={isPodman}
										memoryConfig={containerChartConfigs.memory}
									/>
									<ContainerNetworkChart
										chartData={chartData}
										grid={grid}
										dataEmpty={dataEmpty}
										isPodman={isPodman}
										networkConfig={containerChartConfigs.network}
									/>
								</div>
								{hasContainersTable && (
									<Suspense fallback={null}>
										<ContainersTable systemId={system.id} />
									</Suspense>
								)}
							</>
						)}
					</TabsContent>
				)}

				{hasSystemd && (
					<TabsContent value="services" forceMount className={activeTab === "services" ? "contents" : "hidden"}>
						{mountedTabs.has("services") && (
							<Suspense fallback={null}>
								<SystemdTable systemId={system.id} />
							</Suspense>
						)}
					</TabsContent>
				)}

				{packageUpdates && (
					<TabsContent value="updates" forceMount className={activeTab === "updates" ? "contents" : "hidden"}>
						{mountedTabs.has("updates") && <LazyPackageUpdatesTable systemId={system.id} counts={packageUpdates} />}
					</TabsContent>
				)}
			</Tabs>
		)
	}

	return (
		<div className="grid min-h-0 gap-4 mb-14 overflow-x-clip">
			{/* system info */}
			<InfoBar
				system={system}
				chartData={chartData}
				grid={grid}
				setGrid={setGrid}
				displayMode={displayMode}
				setDisplayMode={setDisplayMode}
				details={details}
			/>

			{displayMode === "tabs" ? tabbedLayout() : defaultLayout()}
		</div>
	)
})
