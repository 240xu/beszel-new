import { plural } from "@lingui/core/macro"
import { useLingui as useLinguiContext } from "@lingui/react"
import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { subscribeKeys } from "nanostores"
import { getPagePath } from "@nanostores/router"
import {
	type ColumnDef,
	type ColumnFiltersState,
	flexRender,
	getCoreRowModel,
	getFilteredRowModel,
	getSortedRowModel,
	type Row,
	type SortingState,
	type Table as TableType,
	useReactTable,
	type VisibilityState,
} from "@tanstack/react-table"
import { useVirtualizer, type VirtualItem } from "@tanstack/react-virtual"
import {
	ArrowDownIcon,
	ArrowUpDownIcon,
	ArrowUpIcon,
	EyeIcon,
	FilterIcon,
	LayoutGridIcon,
	LayoutListIcon,
	PlusIcon,
	RefreshCwIcon,
	Settings2Icon,
	XIcon,
} from "lucide-react"
import { Fragment, memo, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AddSystemDialog } from "@/components/add-system"
import { Button } from "@/components/ui/button"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { SystemStatus } from "@/lib/enums"
import { isReadOnlyUser, queueUserSettings } from "@/lib/api"
import { $downSystems, $pausedSystems, $systems, $upSystems, $userSettings } from "@/lib/stores"
import { $systemsStatus, refresh as refreshSystems } from "@/lib/systemsManager"
import { cn, runOnce } from "@/lib/utils"
import type { SystemRecord } from "@/types"
import AlertButton from "../alerts/alert-button"
import { $router, Link } from "../router"
import { Card, CardContent, CardHeader } from "../ui/card"
import { SystemStatusBadge } from "../system-status"
import { SystemsTableColumns, ActionsButton } from "./systems-table-columns"

type ViewMode = "table" | "grid"
type StatusFilter = "all" | SystemRecord["status"]

const preloadSystemDetail = runOnce(() => import("@/components/routes/system.tsx"))

/** Core overview columns; the operational extras stay one click away in "Visible Fields". */
const DEFAULT_COLUMNS: VisibilityState = {
	gpu: false,
	loadAverage: false,
	net: false,
	temp: false,
	battery: false,
	wifi: false,
	services: false,
	updates: false,
	agent: false,
}

const MOBILE_QUERY = "(max-width: 1023px)"

function isMobile(): boolean {
	return typeof window !== "undefined" && window.matchMedia(MOBILE_QUERY).matches
}

export default function SystemsTable() {
	const data = useStore($systems)
	const status = useStore($systemsStatus)
	const downSystems = $downSystems.get()
	const upSystems = $upSystems.get()
	const pausedSystems = $pausedSystems.get()
	const { t } = useLingui()
	const [addOpen, setAddOpen] = useState(false)
	const explicitViewMode = useRef(
		$userSettings.get().viewMode !== undefined || localStorage.getItem("besz-viewMode") !== null
	)
	const [filter, setFilter] = useState<string>("")
	const [statusFilter, setStatusFilter] = useState<StatusFilter>(
		() =>
			$userSettings.get().statusFilter ??
			(JSON.parse(localStorage.getItem("besz-statusFilter") || "null") as StatusFilter | null) ??
			"all"
	)
	const [sorting, setSorting] = useState<SortingState>(
		() =>
			$userSettings.get().sortMode ??
			JSON.parse(sessionStorage.getItem("besz-sortMode") || "null") ?? [{ id: "system", desc: false }]
	)
	const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
	const [columnVisibility, setColumnVisibility] = useState<VisibilityState>(() => ({
		...DEFAULT_COLUMNS,
		...($userSettings.get().cols ?? JSON.parse(localStorage.getItem("besz-cols") || "{}")),
	}))

	// Apply settings from server once they load (handles incognito / new devices)
	const applied = useRef(new Set<string>())
	useEffect(() => {
		return subscribeKeys($userSettings, ["cols", "statusFilter", "viewMode", "sortMode"], (vals) => {
			if (!applied.current.has("cols") && vals.cols !== undefined) {
				applied.current.add("cols")
				setColumnVisibility({ ...DEFAULT_COLUMNS, ...vals.cols })
			}
			if (!applied.current.has("statusFilter") && vals.statusFilter !== undefined) {
				applied.current.add("statusFilter")
				setStatusFilter(vals.statusFilter)
			}
			if (!applied.current.has("viewMode") && vals.viewMode !== undefined) {
				applied.current.add("viewMode")
				explicitViewMode.current = true
				setViewMode(vals.viewMode)
			}
			if (!applied.current.has("sortMode") && vals.sortMode !== undefined) {
				applied.current.add("sortMode")
				setSorting(vals.sortMode)
			}
		})
	}, [])

	const handleColumnVisibilityChange = useCallback(
		(updater: VisibilityState | ((prev: VisibilityState) => VisibilityState)) => {
			setColumnVisibility((prev) => {
				const next = typeof updater === "function" ? updater(prev) : updater
				localStorage.setItem("besz-cols", JSON.stringify(next))
				$userSettings.setKey("cols", next)
				queueUserSettings({ cols: next })
				return next
			})
		},
		[]
	)

	const handleStatusFilterChange = useCallback((value: string) => {
		const next = value as StatusFilter
		setStatusFilter(next)
		localStorage.setItem("besz-statusFilter", JSON.stringify(next))
		$userSettings.setKey("statusFilter", next)
		queueUserSettings({ statusFilter: next })
	}, [])

	const handleViewModeChange = useCallback((view: string) => {
		const next = view as ViewMode
		explicitViewMode.current = true
		setViewMode(next)
		localStorage.setItem("besz-viewMode", JSON.stringify(next))
		$userSettings.setKey("viewMode", next)
		queueUserSettings({ viewMode: next })
	}, [])

	const handleSortingChange = useCallback((updater: SortingState | ((prev: SortingState) => SortingState)) => {
		setSorting((prev) => {
			const next = typeof updater === "function" ? updater(prev) : updater
			sessionStorage.setItem("besz-sortMode", JSON.stringify(next))
			$userSettings.setKey("sortMode", next)
			queueUserSettings({ sortMode: next })
			return next
		})
	}, [])

	// Filter data based on status filter
	const filteredData = useMemo(() => {
		if (statusFilter === "all") {
			return data
		}
		if (statusFilter === SystemStatus.Up) {
			return Object.values(upSystems) ?? []
		}
		if (statusFilter === SystemStatus.Down) {
			return Object.values(downSystems) ?? []
		}
		return Object.values(pausedSystems) ?? []
	}, [data, statusFilter])

	const [viewMode, setViewMode] = useState<ViewMode>(
		() =>
			$userSettings.get().viewMode ??
			(JSON.parse(localStorage.getItem("besz-viewMode") || "null") as ViewMode | null) ??
			// show grid view on mobile if there are less than 200 systems (looks better but table is more efficient)
			(data.length < 200 && isMobile() ? "grid" : "table")
	)

	// Follow the mobile breakpoint until the user explicitly picks a view mode.
	useEffect(() => {
		if (explicitViewMode.current) {
			return
		}
		const media = window.matchMedia(MOBILE_QUERY)
		const onChange = () => {
			if (explicitViewMode.current) {
				return
			}
			setViewMode(media.matches && data.length < 200 ? "grid" : "table")
		}
		media.addEventListener("change", onChange)
		return () => media.removeEventListener("change", onChange)
	}, [data.length])

	useEffect(() => {
		if (filter !== undefined) {
			table.getColumn("system")?.setFilterValue(filter)
		}
	}, [filter])

	const columnDefs = useMemo(() => SystemsTableColumns(viewMode), [viewMode])

	const table = useReactTable({
		data: filteredData,
		columns: columnDefs,
		getCoreRowModel: getCoreRowModel(),
		onSortingChange: handleSortingChange,
		getSortedRowModel: getSortedRowModel(),
		onColumnFiltersChange: setColumnFilters,
		getFilteredRowModel: getFilteredRowModel(),
		onColumnVisibilityChange: handleColumnVisibilityChange,
		state: {
			sorting,
			columnFilters,
			columnVisibility,
		},
		defaultColumn: {
			invertSorting: true,
			sortUndefined: "last",
			minSize: 0,
			size: 900,
			maxSize: 900,
		},
	})

	const rows = table.getRowModel().rows
	const retrySystems = useCallback(() => {
		$systemsStatus.set("loading")
		void refreshSystems()
	}, [])

	const columns = table.getAllColumns()
	const visibleColumns = table.getVisibleLeafColumns()

	const [upSystemsLength, downSystemsLength, pausedSystemsLength] = useMemo(() => {
		return [Object.values(upSystems).length, Object.values(downSystems).length, Object.values(pausedSystems).length]
	}, [upSystems, downSystems, pausedSystems])

	const pageHeader = (
		<header className="flex flex-col gap-3 px-1 sm:px-0">
			<div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
				<div className="min-w-0">
					<h1 className="text-2xl font-semibold tracking-[-0.02em] sm:text-[1.75rem]">
						<Trans>All Systems</Trans>
					</h1>
					<div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
						<span className="tabular-nums">
							{plural(data.length, { one: "# system", other: "# systems" })}
						</span>
						<span aria-hidden="true">·</span>
						<span className="tabular-nums text-foreground">{upSystemsLength}</span> <Trans>Up</Trans>
						<span aria-hidden="true">·</span>
						<span className="tabular-nums text-foreground">{downSystemsLength}</span> <Trans>Down</Trans>
						{pausedSystemsLength > 0 && (
							<>
								<span aria-hidden="true">·</span>
								<span className="tabular-nums text-foreground">{pausedSystemsLength}</span> <Trans>Paused</Trans>
							</>
						)}
					</div>
				</div>

				<div className="flex w-full flex-wrap items-center gap-2 md:w-auto md:flex-nowrap">
					<div className="relative min-w-0 flex-1 md:w-64 md:flex-none">
						<Input
							placeholder={t`Filter...`}
							aria-label={t`Search`}
							onChange={(e) => setFilter(e.target.value)}
							value={filter}
							className="ps-4 pe-10 w-full h-11 sm:h-9"
						/>
						{filter && (
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={t`Clear`}
								className="absolute end-1 top-1/2 -translate-y-1/2 h-7 w-7 text-muted-foreground"
								onClick={() => setFilter("")}
							>
								<XIcon className="h-4 w-4" />
							</Button>
						)}
					</div>
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button variant="outline" className="h-11 sm:h-9">
								<Settings2Icon className="me-1.5 size-4 opacity-80" />
								<Trans>View</Trans>
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="h-72 md:h-auto min-w-48 md:min-w-auto overflow-y-auto">
							<div className="grid grid-cols-1 md:grid-cols-4 divide-y md:divide-s md:divide-y-0">
								<div className="border-e">
									<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
										<LayoutGridIcon className="size-4" />
										<Trans>Layout</Trans>
									</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<DropdownMenuRadioGroup className="px-1 pb-1" value={viewMode} onValueChange={handleViewModeChange}>
										<DropdownMenuRadioItem value="table" onSelect={(e) => e.preventDefault()} className="gap-2">
											<LayoutListIcon className="size-4" />
											<Trans>Table</Trans>
										</DropdownMenuRadioItem>
										<DropdownMenuRadioItem value="grid" onSelect={(e) => e.preventDefault()} className="gap-2">
											<LayoutGridIcon className="size-4" />
											<Trans>Grid</Trans>
										</DropdownMenuRadioItem>
									</DropdownMenuRadioGroup>
								</div>

								<div className="border-e">
									<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
										<FilterIcon className="size-4" />
										<Trans>Status</Trans>
									</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<DropdownMenuRadioGroup
										className="px-1 pb-1"
										value={statusFilter}
										onValueChange={handleStatusFilterChange}
									>
										<DropdownMenuRadioItem value="all" onSelect={(e) => e.preventDefault()}>
											<Trans>All Systems</Trans>
										</DropdownMenuRadioItem>
										<DropdownMenuRadioItem value="up" onSelect={(e) => e.preventDefault()}>
											<Trans>Up ({upSystemsLength})</Trans>
										</DropdownMenuRadioItem>
										<DropdownMenuRadioItem value="down" onSelect={(e) => e.preventDefault()}>
											<Trans>Down ({downSystemsLength})</Trans>
										</DropdownMenuRadioItem>
										<DropdownMenuRadioItem value="paused" onSelect={(e) => e.preventDefault()}>
											<Trans>Paused ({pausedSystemsLength})</Trans>
										</DropdownMenuRadioItem>
									</DropdownMenuRadioGroup>
								</div>

								<div className="border-e">
									<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
										<ArrowUpDownIcon className="size-4" />
										<Trans>Sort By</Trans>
									</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<div className="px-1 pb-1">
										{columns.map((column) => {
											if (!column.getCanSort()) return null
											let Icon = <span className="w-6"></span>
											// if current sort column, show sort direction
											if (sorting[0]?.id === column.id) {
												if (sorting[0]?.desc) {
													Icon = <ArrowUpIcon className="me-2 size-4" />
												} else {
													Icon = <ArrowDownIcon className="me-2 size-4" />
												}
											}
											return (
												<DropdownMenuItem
													onSelect={(e) => {
														e.preventDefault()
														handleSortingChange([
															{ id: column.id, desc: sorting[0]?.id === column.id && !sorting[0]?.desc },
														])
													}}
													key={column.id}
												>
													{Icon}
													{/* @ts-ignore */}
													{column.columnDef.name()}
												</DropdownMenuItem>
											)
										})}
									</div>
								</div>

								<div>
									<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
										<EyeIcon className="size-4" />
										<Trans>Visible Fields</Trans>
									</DropdownMenuLabel>
									<DropdownMenuSeparator />
									<div className="px-1.5 pb-1">
										{columns
											.filter((column) => column.getCanHide())
											.map((column) => {
												return (
													<DropdownMenuCheckboxItem
														key={column.id}
														onSelect={(e) => e.preventDefault()}
														checked={column.getIsVisible()}
														onCheckedChange={(value) => column.toggleVisibility(!!value)}
													>
														{/* @ts-ignore */}
														{column.columnDef.name()}
													</DropdownMenuCheckboxItem>
												)
											})}
									</div>
								</div>
							</div>
						</DropdownMenuContent>
					</DropdownMenu>
					{!isReadOnlyUser() && (
						<Button className="h-11 sm:h-9" onClick={() => setAddOpen(true)}>
							<PlusIcon className="me-1.5 size-4" />
							<Trans>Add System</Trans>
						</Button>
					)}
				</div>
			</div>
		</header>
	)

	return (
		<div className="grid min-h-0 gap-4">
			{pageHeader}
			{status === "error" && data.length > 0 && <SystemsError onRetry={retrySystems} />}
			{status === "loading" && !data.length ? (
				<SystemsTableSkeleton viewMode={viewMode} />
			) : status === "error" && !data.length ? (
				<SystemsError onRetry={retrySystems} />
			) : !data.length ? (
				<SystemsEmpty readOnly={isReadOnlyUser()} onAdd={() => setAddOpen(true)} />
			) : viewMode === "table" ? (
				// table layout
				<Card className="min-h-0 overflow-hidden p-0 shadow-none">
					<AllSystemsTable table={table} rows={rows} colLength={visibleColumns.length} />
				</Card>
			) : (
				// grid layout
				<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
					{rows?.length ? (
						rows.map((row) => {
							return <SystemCard key={row.original.id} row={row} table={table} colLength={visibleColumns.length} />
						})
					) : (
						<Card className="col-span-full py-10 text-center text-sm text-muted-foreground shadow-none">
							<Trans>No systems found.</Trans>
						</Card>
					)}
				</div>
			)}
			<AddSystemDialog open={addOpen} setOpen={setAddOpen} />
		</div>
	)
}

function AllSystemsTable({
	table,
	rows,
	colLength,
}: {
	table: TableType<SystemRecord>
	rows: Row<SystemRecord>[]
	colLength: number
}) {
		// The virtualizer will need a reference to the scrollable container element
		const scrollRef = useRef<HTMLDivElement>(null)

		const virtualizer = useVirtualizer<HTMLDivElement, HTMLTableRowElement>({
			count: rows.length,
			estimateSize: () => 54,
			getScrollElement: () => scrollRef.current,
			overscan: 5,
		})
		const virtualRows = virtualizer.getVirtualItems()

		const paddingTop = Math.max(0, virtualRows[0]?.start ?? 0 - virtualizer.options.scrollMargin)
		const paddingBottom = Math.max(0, virtualizer.getTotalSize() - (virtualRows[virtualRows.length - 1]?.end ?? 0))

		return (
			<div
				className={cn(
					"relative grid max-h-[70svh] min-h-0 max-w-full overflow-auto",
					// don't set min height if there are less than 2 rows, do set if we need to display the empty state
					(!rows.length || rows.length > 2) && "min-h-50"
				)}
				ref={scrollRef}
			>
				{/* add header height to table size */}
				<div style={{ height: `${virtualizer.getTotalSize() + 48}px`, paddingTop, paddingBottom }}>
					<table className="text-sm w-full h-full">
						<SystemsTableHead table={table} />
						<TableBody onMouseEnter={preloadSystemDetail}>
							{rows.length ? (
								virtualRows.map((virtualRow) => {
									const row = rows[virtualRow.index] as Row<SystemRecord>
									return (
										<SystemTableRow
											key={row.id}
											row={row}
											virtualRow={virtualRow}
											length={rows.length}
											colLength={colLength}
										/>
									)
								})
							) : (
								<TableRow>
									<TableCell colSpan={colLength} className="h-37 text-center pointer-events-none">
										<Trans>No systems found.</Trans>
									</TableCell>
								</TableRow>
							)}
						</TableBody>
					</table>
				</div>
			</div>
		)
	}

function SystemsTableHead({ table }: { table: TableType<SystemRecord> }) {
	useLinguiContext()
	return (
		<TableHeader className="sticky top-0 z-30 w-full border-b-2">
			{table.getHeaderGroups().map((headerGroup) => (
				<tr key={headerGroup.id}>
					{headerGroup.headers.map((header) => {
						return (
							<TableHead className="sticky top-0 z-30 bg-table-header px-1.5" key={header.id}>
								{flexRender(header.column.columnDef.header, header.getContext())}
							</TableHead>
						)
					})}
				</tr>
			))}
		</TableHeader>
	)
}

const SystemTableRow = memo(
	({
		row,
		virtualRow,
		colLength,
	}: {
		row: Row<SystemRecord>
		virtualRow: VirtualItem
		length: number
		colLength: number
	}) => {
		const system = row.original
		const { t } = useLingui()
		return useMemo(() => {
			return (
				<TableRow
					// data-state={row.getIsSelected() && "selected"}
					className={cn("cursor-pointer transition-opacity relative safari:transform-3d", {
						"opacity-50": system.status === SystemStatus.Paused,
					})}
				>
					{row.getVisibleCells().map((cell) => (
						<TableCell
							key={cell.id}
							style={{
								width: cell.column.getSize(),
								height: virtualRow.size,
							}}
							className="py-0 ps-4.5"
						>
							{flexRender(cell.column.columnDef.cell, cell.getContext())}
						</TableCell>
					))}
				</TableRow>
			)
		}, [system, system.status, colLength, t])
	}
)

function SystemCard({
	row,
	table,
	colLength,
}: {
	row: Row<SystemRecord>
	table: TableType<SystemRecord>
	colLength: number
}) {
		const system = row.original
		const { t } = useLingui()
		const visibility = table.getState().columnVisibility

		return useMemo(() => {
			return (
				<Card
					onMouseEnter={preloadSystemDetail}
					className={cn(
						"relative w-full cursor-pointer overflow-hidden bg-card p-0 shadow-none transition-colors duration-150 hover:border-input",
						{
							"opacity-50": system.status === SystemStatus.Paused,
						}
					)}
				>
					<CardHeader className="border-b border-border/60 bg-muted/30 px-4 py-3">
						<div className="flex w-full items-center gap-2 overflow-hidden">
							<h3 className="flex min-w-0 flex-1 items-center gap-2 text-sm font-semibold text-foreground">
								<span className="truncate">{system.name}</span>
								<SystemStatusBadge status={system.status} className="shrink-0" />
							</h3>
							{table.getColumn("actions")?.getIsVisible() && (
								<div className="flex gap-1 shrink-0 relative z-10">
									<AlertButton system={system} />
									<ActionsButton system={system} />
								</div>
							)}
						</div>
					</CardHeader>
					<CardContent className="px-4 pb-4 pt-3 text-sm">
						<div
							className="grid gap-x-2 gap-y-2.5"
							style={{ gridTemplateColumns: "16px minmax(4rem, max-content) minmax(0, 1fr)" }}
						>
							{table.getAllColumns().map((column) => {
								if (!column.getIsVisible() || column.id === "system" || column.id === "status" || column.id === "actions") return null
								const cell = row.getAllCells().find((cell) => cell.column.id === column.id)
								if (!cell) return null
								// @ts-expect-error
								const { Icon, name } = column.columnDef as ColumnDef<SystemRecord, unknown>
								return (
									<Fragment key={column.id}>
										<div className="flex items-center">
											{Icon && <Icon className="size-4 text-muted-foreground" />}
										</div>
										<div className="flex min-w-0 items-center pe-2 text-muted-foreground">{name()}</div>
										<div className="flex min-w-0 items-center overflow-hidden">
											{flexRender(cell.column.columnDef.cell, cell.getContext())}
										</div>
									</Fragment>
								)
							})}
						</div>
					</CardContent>
					<Link
						href={getPagePath($router, "system", { id: row.original.id })}
						className="inset-0 absolute w-full h-full"
					>
						<span className="sr-only">{row.original.name}</span>
					</Link>
				</Card>
			)
		}, [system, colLength, t, visibility])
	}

function SystemsError({ onRetry }: { onRetry: () => void }) {
	return (
		<Card className="flex flex-col items-start gap-3 p-6 shadow-none sm:flex-row sm:items-center sm:justify-between" role="alert">
			<div>
				<p className="font-medium">
					<Trans>Failed to load systems.</Trans>
				</p>
				<p className="mt-1 text-sm text-muted-foreground">
					<Trans>The hub could not be reached. Check the connection and try again.</Trans>
				</p>
			</div>
			<Button variant="outline" className="h-11 sm:h-9" onClick={onRetry}>
				<RefreshCwIcon className="me-1.5 size-4" />
				<Trans>Retry</Trans>
			</Button>
		</Card>
	)
}

function SystemsEmpty({ readOnly, onAdd }: { readOnly: boolean; onAdd: () => void }) {
	return (
		<Card className="px-6 py-12 text-center shadow-none">
			<p className="font-medium">
				<Trans>Add your first system</Trans>
			</p>
			<p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">
				<Trans>Install the agent on a machine to start monitoring it here.</Trans>
			</p>
			{!readOnly && (
				<Button className="mt-4 h-11 sm:h-9" onClick={onAdd}>
					<PlusIcon className="me-1.5 size-4" />
					<Trans>Add System</Trans>
				</Button>
			)}
		</Card>
	)
}

function SystemsTableSkeleton({ viewMode }: { viewMode: ViewMode }) {
	const rows = Array.from({ length: 6 }, (_, index) => index)
	return (
		<div role="status" aria-live="polite">
			<span className="sr-only">
				<Trans>Loading...</Trans>
			</span>
			{viewMode === "table" ? (
				<Card className="overflow-hidden p-0 shadow-none">
					<div className="h-12 border-b bg-table-header" />
					<div className="divide-y">
						{rows.map((row) => (
							<div key={row} className="flex h-[54px] items-center gap-4 px-4">
								<div className="h-3.5 w-28 rounded bg-muted animate-pulse motion-reduce:animate-none" />
								<div className="h-3.5 w-16 rounded bg-muted animate-pulse motion-reduce:animate-none" />
								<div className="h-3.5 w-20 rounded bg-muted animate-pulse motion-reduce:animate-none" />
								<div className="ms-auto h-3.5 w-24 rounded bg-muted animate-pulse motion-reduce:animate-none" />
							</div>
						))}
					</div>
				</Card>
			) : (
				<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
					{rows.slice(0, 4).map((row) => (
						<Card key={row} className="p-4 shadow-none">
							<div className="flex items-center gap-2">
								<div className="h-4 w-32 rounded bg-muted animate-pulse motion-reduce:animate-none" />
								<div className="ms-auto h-5 w-14 rounded-full bg-muted animate-pulse motion-reduce:animate-none" />
							</div>
							<div className="mt-4 space-y-3">
								{[0, 1, 2, 3].map((line) => (
									<div key={line} className="flex items-center gap-3">
										<div className="size-4 rounded bg-muted animate-pulse motion-reduce:animate-none" />
										<div className="h-3 w-16 rounded bg-muted animate-pulse motion-reduce:animate-none" />
										<div className="h-3 flex-1 rounded bg-muted animate-pulse motion-reduce:animate-none" />
									</div>
								))}
							</div>
						</Card>
					))}
				</div>
			)}
		</div>
	)
}
