import type { RecordModel } from "pocketbase"
import type { Unit, Os, BatteryState, HourFormat, ConnectionType, ServiceStatus, ServiceSubState } from "@/lib/enums"
import type { FanRejectCode } from "@/lib/plus-refusals"

// global window properties
declare global {
	var BESZEL: {
		BASE_PATH: string
		HUB_VERSION: string
		HUB_URL: string
		OAUTH_DISABLE_POPUP: boolean
	}
}

export interface FingerprintRecord extends RecordModel {
	id: string
	system: string
	fingerprint: string
	token: string
	expand: {
		system: {
			name: string
		}
	}
}

export interface SystemRecord extends RecordModel {
	name: string
	host: string
	status: "up" | "down" | "paused" | "pending"
	port: string
	info: SystemInfo
	v: string
	updated: string
}

export interface WiFi {
	s?: string
	r?: number
}

export interface SystemInfo {
	/** connected Wi-Fi interfaces */
	wf?: Record<string, WiFi>
	/** hostname */
	h: string
	/** kernel **/
	k?: string
	/** cpu percent */
	cpu: number
	/** cpu threads */
	t?: number
	/** cpu cores */
	c: number
	/** cpu model */
	m: string
	/** load average */
	la?: [number, number, number]
	/** operating system */
	o?: string
	/** uptime */
	u: number
	/** memory percent */
	mp: number
	/** disk percent */
	dp: number
	/** battery percent and state */
	bat?: [number, BatteryState]
	/** bandwidth (mb) */
	b: number
	/** bandwidth bytes */
	bb?: number
	/** agent version */
	v: string
	/** agent can read the system journal */
	jl?: boolean
	/** system is using podman */
	p?: boolean
	/** highest gpu utilization */
	g?: number
	/** dashboard display temperature */
	dt?: number
	/** operating system */
	os?: Os
	/** connection type */
	ct?: ConnectionType
	/** extra filesystem percentages */
	efs?: Record<string, number>
	/** services [totalServices, numFailedServices] */
	sv?: [number, number]
	/** custom root disk name */
	rdn?: string
	/** pending package updates [total, security] (security omitted if unknown) */
	pu?: [number, number?]
	/** beszel-new snapshot. The cached copy every stats record is filled from. */
	pl?: PlusStats
}

export interface SystemStats {
	/** cpu percent */
	cpu: number
	/** peak cpu */
	cpum?: number
	/** cpu breakdown [user, system, iowait, steal, idle] (0-100 integers) */
	cpub?: number[]
	/** per-core cpu usage [CPU0..] (0-100 integers) */
	cpus?: number[]
	/** load average */
	la?: [number, number, number]
	/** total memory (gb) */
	m: number
	/** memory used (gb) */
	mu: number
	/** memory percent */
	mp: number
	/** memory buffer + cache (gb) */
	mb: number
	/** max used memory (gb) */
	mm?: number
	/** zfs arc memory (gb) */
	mz?: number
	/** swap space (gb) */
	s: number
	/** swap used (gb) */
	su: number
	/** disk size (gb) */
	d: number
	/** disk used (gb) */
	du: number
	/** disk percent */
	dp: number
	/** disk read (mb) */
	dr: number
	/** disk write (mb) */
	dw: number
	/** max disk read (mb) */
	drm?: number
	/** max disk write (mb) */
	dwm?: number
	/** disk I/O bytes [read, write] */
	dio?: [number, number]
	/** max disk I/O bytes [read, write] */
	diom?: [number, number]
	/** disk io stats [read time factor, write time factor, io utilization %, r_await ms, w_await ms, weighted io %] */
	dios?: [number, number, number, number, number, number]
	/** max disk io stats */
	diosm?: [number, number, number, number, number, number]
	/** cumulative device I/O bytes [total read, total write] */
	diot?: [number, number]
	/** network sent (mb) */
	ns: number
	/** network received (mb) */
	nr: number
	/** bandwidth bytes [sent, recv] */
	b?: [number, number]
	/** max network sent (mb) */
	nsm?: number
	/** max network received (mb) */
	nrm?: number
	/** max network sent (bytes) */
	bm?: [number, number]
	/** temperatures */
	t?: Record<string, number>
	/** fan speeds (RPM) — keyed by `<chip>_<label-or-fan-idx>` */
	f?: Record<string, number>
	/** extra filesystems */
	efs?: Record<string, ExtraFsStats>
	/** ZFS pool metrics */
	z?: Record<string, ZfsPool>
	/** GPU data */
	g?: Record<string, GPUData>
	/** battery percent and state */
	bat?: [number, BatteryState]
	/** battery percentages by device name */
	bats?: Record<string, number>
	/** Wi-Fi RSSI (dBm) by interface */
	wf?: Record<string, number>
	/** network interfaces [upload bytes, download bytes, total upload bytes, total download bytes] */
	ni?: Record<string, [number, number, number, number]>
	/** beszel-new snapshot, copied from info.pl, so both carry the same reading.
	 *  Prefer this one; the cache is there for the agent's own bookkeeping. */
	plus?: PlusStats
}

export interface GPUData {
	/** name */
	n: string
	/** memory used (mb) */
	mu?: number
	/** memory total (mb) */
	mt?: number
	/** usage (%) */
	u: number
	/** power (w) */
	p?: number
	/** power package (w) */
	pp?: number
	/** engines */
	e?: Record<string, number>
}

export interface ZfsPool {
	/** Friendly name; map keys are stable pool identities. */
	n?: string
	/** Equivalent filesystem charts are already displayed. */
	hu?: boolean
	hi?: boolean
	raw?: boolean
	/** total capacity (GiB) */
	d: number
	/** allocated (GiB) */
	du: number
	/** read throughput (bytes/s) */
	rb?: number
	/** write throughput (bytes/s) */
	wb?: number
	/** health: ONLINE, DEGRADED, FAULTED, ... */
	h?: string
}

export interface ZfsScrub {
	/** NONE, SCANNING, FINISHED, CANCELED */
	state?: string
	/** progress while scanning, e.g. "10.00%" */
	progress?: string
	errors?: number
}

export interface ZfsVdev {
	name: string
	state?: string
	readErrs?: number
	writeErrs?: number
	checksumErrs?: number
}

/** pending package update from GET /api/beszel/package-updates */
export interface PackageUpdate {
	name: string
	/** installed version, missing if unknown */
	current?: string
	available: string
	security?: boolean
}

export interface PackageUpdates {
	/** package manager name, e.g. "apt" */
	manager?: string
	/** unix time in seconds of the last check */
	checkedAt?: number
	/** true if the package manager flags security updates per package */
	securityKnown?: boolean
	packages: PackageUpdate[] | null
}

export interface ZfsDataset {
	name: string
	used?: number
	avail?: number
	mount?: string
}

export interface ZfsPoolRecord extends RecordModel {
	display_name?: string
	raw?: boolean
	system: string
	name: string
	health: string
	size: number
	alloc: number
	free: number
	scrub: ZfsScrub | null
	vdevs: ZfsVdev[] | null
	datasets: ZfsDataset[] | null
	details_updated: string
	updated: string
}

export interface ExtraFsStats {
	/** disk size (gb) */
	d: number
	/** disk used (gb) */
	du: number
	/** total read (mb) */
	r: number
	/** total write (mb) */
	w: number
	/** max read (mb) */
	rm: number
	/** max write (mb) */
	wm: number
	/** read per second (bytes) */
	rb: number
	/** write per second (bytes) */
	wb: number
	/** max read per second (bytes) */
	rbm: number
	/** max write per second (mb) */
	wbm: number
	/** disk io stats [read time factor, write time factor, io utilization %, r_await ms, w_await ms, weighted io %] */
	dios?: [number, number, number, number, number, number]
	/** max disk io stats */
	diosm?: [number, number, number, number, number, number]
	/** cumulative device read bytes */
	tr?: number
	/** cumulative device write bytes */
	tw?: number
}

export interface ContainerStatsRecord extends RecordModel {
	system: string
	stats: ContainerStats[]
	created: string | number
}

interface ContainerStats {
	/** name */
	n: string
	/** cpu percent */
	c: number
	/** memory used (mb) */
	m: number
	// network sent (mb)
	ns?: number
	// network received (mb)
	nr?: number
	/** bandwidth bytes [sent, recv] */
	b?: [number, number]
}

export interface SystemStatsRecord extends RecordModel {
	system: string
	stats: SystemStats
	created: string | number
}

export interface AlertRecord extends RecordModel {
	id: string
	system: string
	name: string
	triggered: boolean
	value: number
	min: number
	// user: string
}

export interface AlertsHistoryRecord extends RecordModel {
	monitor_name?: string
	alert: string
	user: string
	system: string
	name: string
	val: number
	created: string
	resolved?: string | null
}

export interface QuietHoursRecord extends RecordModel {
	id: string
	user: string
	system: string
	type: "one-time" | "daily"
	start: string
	end: string
	expand?: {
		system?: {
			name: string
		}
	}
}

export interface ContainerRecord extends RecordModel {
	id: string
	system: string
	name: string
	image: string
	updatable?: boolean
	ports: string
	cpu: number
	memory: number
	net: number
	health: number
	status: string
	updated: number
}

export type ChartTimes = "1m" | "1h" | "12h" | "24h" | "1w" | "30d"

export interface ChartTimeData {
	[key: string]: {
		type: "1m" | "10m" | "20m" | "120m" | "480m"
		expectedInterval: number
		label: () => string
		ticks?: number
		format: (timestamp: string) => string
		getOffset: (endTime: Date) => Date
		minVersion?: string
	}
}

export interface UserSettings {
	/** may be missing in settings stored by older versions -- use getUserChartTime() */
	chartTime?: ChartTimes
	emails?: string[]
	webhooks?: string[]
	unitTemp?: Unit
	unitNet?: Unit
	unitDisk?: Unit
	colorWarn?: number
	colorCrit?: number
	hourFormat?: HourFormat
	layoutWidth?: number
	lang?: string
	cols?: Record<string, boolean>
	statusFilter?: "all" | "up" | "down" | "paused" | "pending"
	viewMode?: "table" | "grid"
	sortMode?: Array<{ id: string; desc: boolean }>
	monitorCols?: Record<string, boolean>
	monitorSortMode?: Array<{ id: string; desc: boolean }>
	monitorSortModeSystem?: Array<{ id: string; desc: boolean }>
	grid?: boolean
	displayMode?: "default" | "tabs"
}

type ChartDataContainer = {
	created: number | null
} & {
	[key: string]: key extends "created" ? never : ContainerStats
}

export interface SemVer {
	major: number
	minor: number
	patch: number
}

export interface ChartOptions {
	agentVersion: SemVer
	orientation: "right" | "left"
	chartTime: ChartTimes
}

export interface ChartData extends ChartOptions {
	systemStats: SystemStatsRecord[]
	containerData: ChartDataContainer[]
}

export interface AlertInfo {
	name: () => string
	unit: string
	icon: any
	desc: () => string
	max?: number
	min?: number
	step?: number
	start?: number
	/** Single value description (when there's only one value, like status) */
	singleDesc?: () => string
	/** Hides the duration slider for alerts that fire on first observation */
	noDuration?: boolean
	/** Hides the threshold control for binary alerts */
	noThreshold?: boolean
	/** Description shown instead of numeric threshold and duration values */
	triggeredDesc?: () => string
	/** Additional information that remains visible while the alert is enabled */
	note?: () => string
	invert?: boolean
	/** Selectable threshold units. Values are stored in the first unit (factor 1) */
	units?: AlertUnit[]
}

export interface AlertUnit {
	/** Unit suffix shown after the value */
	unit: string
	/** Multiplier converting a value in this unit to the stored value */
	factor: number
	min: number
	max: number
	step: number
	/** Finer step for the number input, which also accepts values down to this step */
	inputStep?: number
}

export type AlertMap = Record<string, Map<string, AlertRecord>>

export interface SmartData {
	/** model family */
	// mf?: string
	/** model name */
	mn?: string
	/** serial number */
	sn?: string
	/** firmware version */
	fv?: string
	/** capacity */
	c?: number
	/** smart status */
	s?: string
	/** disk name (like /dev/sda) */
	dn?: string
	/** disk type */
	dt?: string
	/** temperature */
	t?: number
	/** attributes */
	a?: SmartAttribute[]
}

export interface SmartAttribute {
	/** id */
	id?: number
	/** name */
	n: string
	/** value */
	v: number
	/** worst */
	w?: number
	/** threshold */
	t?: number
	/** raw value */
	rv?: number
	/** raw string */
	rs?: string
	/** when failed */
	wf?: string
}

export interface SystemDetailsRecord extends RecordModel {
	system: string
	hostname: string
	kernel: string
	cores: number
	threads: number
	cpu: string
	os: Os
	os_name: string
	memory: number
	podman: boolean
}

export interface SmartDeviceRecord extends RecordModel {
	id: string
	system: string
	name: string
	model: string
	state: string
	capacity: number
	temp: number
	firmware: string
	serial: string
	type: string
	hours: number
	cycles: number
	attributes: SmartAttribute[]
	updated: string
}

export interface SystemdRecord extends RecordModel {
	system: string
	name: string
	state: ServiceStatus
	sub: ServiceSubState
	cpu: number
	cpuPeak: number
	memory: number
	memPeak: number
	updated: number
}

export interface SystemdServiceDetails {
	AccessSELinuxContext: string
	ActivationDetails: any[]
	ActiveEnterTimestamp: number
	ActiveEnterTimestampMonotonic: number
	ActiveExitTimestamp: number
	ActiveExitTimestampMonotonic: number
	ActiveState: string
	After: string[]
	AllowIsolate: boolean
	AssertResult: boolean
	AssertTimestamp: number
	AssertTimestampMonotonic: number
	Asserts: any[]
	Before: string[]
	BindsTo: any[]
	BoundBy: any[]
	CPUUsageNSec: number
	CanClean: any[]
	CanFreeze: boolean
	CanIsolate: boolean
	CanLiveMount: boolean
	CanReload: boolean
	CanStart: boolean
	CanStop: boolean
	CollectMode: string
	ConditionResult: boolean
	ConditionTimestamp: number
	ConditionTimestampMonotonic: number
	Conditions: any[]
	ConflictedBy: any[]
	Conflicts: string[]
	ConsistsOf: any[]
	DebugInvocation: boolean
	DefaultDependencies: boolean
	Description: string
	Documentation: string[]
	DropInPaths: any[]
	ExecMainPID: number
	FailureAction: string
	FailureActionExitStatus: number
	Following: string
	FragmentPath: string
	FreezerState: string
	Id: string
	IgnoreOnIsolate: boolean
	InactiveEnterTimestamp: number
	InactiveEnterTimestampMonotonic: number
	InactiveExitTimestamp: number
	InactiveExitTimestampMonotonic: number
	InvocationID: string
	Job: Array<number | string>
	JobRunningTimeoutUSec: number
	JobTimeoutAction: string
	JobTimeoutRebootArgument: string
	JobTimeoutUSec: number
	JoinsNamespaceOf: any[]
	LoadError: string[]
	LoadState: string
	MainPID: number
	Markers: any[]
	MemoryCurrent: number
	MemoryLimit: number
	MemoryPeak: number
	NRestarts: number
	Names: string[]
	NeedDaemonReload: boolean
	OnFailure: any[]
	OnFailureJobMode: string
	OnFailureOf: any[]
	OnSuccess: any[]
	OnSuccessJobMode: string
	OnSuccessOf: any[]
	PartOf: any[]
	Perpetual: boolean
	PropagatesReloadTo: any[]
	PropagatesStopTo: any[]
	RebootArgument: string
	Refs: any[]
	RefuseManualStart: boolean
	RefuseManualStop: boolean
	ReloadPropagatedFrom: any[]
	RequiredBy: any[]
	Requires: string[]
	RequiresMountsFor: any[]
	Requisite: any[]
	RequisiteOf: any[]
	Result: string
	SliceOf: any[]
	SourcePath: string
	StartLimitAction: string
	StartLimitBurst: number
	StartLimitIntervalUSec: number
	StateChangeTimestamp: number
	StateChangeTimestampMonotonic: number
	StopPropagatedFrom: any[]
	StopWhenUnneeded: boolean
	SubState: string
	SuccessAction: string
	SuccessActionExitStatus: number
	SurviveFinalKillSignal: boolean
	TasksCurrent: number
	TasksMax: number
	Transient: boolean
	TriggeredBy: string[]
	Triggers: any[]
	UnitFilePreset: string
	UnitFileState: string
	UpheldBy: any[]
	Upholds: any[]
	WantedBy: any[]
	Wants: string[]
	WantsMountsFor: any[]
}

export interface BeszelInfo {
	key: string // public key
	v: string // version
	cu: boolean // check updates
}

export interface UpdateInfo {
	v: string // new version
	url: string // url to new version
}

export interface NetworkMonitorRecord {
	id: string
	system: string
	target: string
	protocol: "icmp" | "tcp" | "http" | "dns"
	port: number
	server: string
	res: number
	resMin1h: number
	resMax1h: number
	resAvg1h: number
	loss: number
	loss1h: number
	interval: number
	enabled: boolean
	/** Latest TLS certificate details, reported for HTTPS targets. */
	certInfo?: MonitorCertInfo | null
	updated: string
}

/** Leaf TLS certificate details reported by the agent. Timestamps are Unix milliseconds. */
export interface MonitorCertInfo {
	expires: number
	issuer?: string
}

/** Response times in microseconds and packet loss percentage (0-100). */
export interface MonitorStats {
	/** null when no probe succeeded, so there is no response time */
	res_avg: number | null
	res_min: number | null
	res_max: number | null
	loss: number
}

/** Raw per-monitor record stored in the DB. */
export interface RawMonitorStatsRecord {
	res_min: number
	res_max: number
	total_count: number
	success_count: number
	res_sum: number
	id?: string
	type?: string
	monitor: string
	created: number // unix timestamp (ms)
}

/**
 * Merged stats record keyed by monitor ID, used by chart components.
 * Constructed from multiple RawMonitorStatsRecord entries sharing the same timestamp.
 */
export interface NetworkMonitorStatsRecord {
	id?: string
	type?: string
	stats: Record<string, MonitorStats>
	created: number // unix timestamp (ms) for Recharts xAxis
}

// ---------------------------------------------------------------------------
// beszel-new: mirrors agent/plus. A Go field without `omitempty` is required
// here; one with it is optional. Collapsing the two is how a missing reading
// becomes a zero, so the required set is exactly the no-omitempty fields.
// ---------------------------------------------------------------------------

export interface DPTFStats {
	zones?: Record<string, number>
}

/** `idx` is the cpu<N> number parsed from sysfs, NOT the array position: a core
 *  whose frequency is unreadable is skipped, so the positions shift. */
export interface CpufreqCore {
	idx: number
	freq_mhz: number
	min_mhz: number
	max_mhz: number
}

/** Governor, EPP and pstate are single global values (last core read wins)
 *  though the kernel reports them per core. */
export interface CpufreqStats {
	cores?: CpufreqCore[]
	governor?: string
	epp?: string
	no_turbo?: number
	hwp_dynamic_boost?: number
	pstate_status?: string
}

/** Cumulative and unsigned, so difference them yourself and read a decrease as a wrap or a counter reset, never as negative traffic. Bytes are always written; packets are omitted when zero and so disappear. */
export interface NetIfaceStats {
	rx_bytes: number
	tx_bytes: number
	rx_packets?: number
	tx_packets?: number
	oper_state?: string
	speed_mbit?: number
	mtu?: number
}

export interface NvmeStats {
	model?: string
	temp_c?: number
	available_spare_pct?: number
	percentage_used_pct?: number
	data_units_read?: number
	data_units_written?: number
	power_on_hours?: number
	power_cycles?: number
	unsafe_shutdowns?: number
	media_errors?: number
	controller_busy_min?: number
	critical_comp_time_min?: number
}

export interface WiFiStats {
	signal_dbm?: number
	tx_mbit?: number
	freq_mhz?: number
	retry?: number
	iface?: string
}

/** `rpm_est` is a duty-derived estimate, not a tachometer reading, and `rpm_est_is_estimate` is present so a consumer can assert it is labelled.
 *  `set_level_pct` and `ec76_pct` are tri-state: a recorded NULL is not zero. */
export interface FanStats {
	set_level_pct?: number
	ec76_pct?: number
	rpm_est?: number
	rpm_est_is_estimate: boolean
	gov?: string
	temp_c?: number
	/** Unix SECONDS, and the only staleness signal a consumer gets: the
	 *  collector reports whatever was last recorded and never checks the clock,
	 *  so age this against your own. Dropping it turns an hours-old reading
	 *  into a live one. */
	sample_ts_s: number
}

export interface FanSpeedStep {
	percent: number
	rpm_est: number
	noise_mdb: number
	power_mw: number
}

export interface PlusStats {
	dptf?: DPTFStats
	cpufreq?: CpufreqStats
	netall?: Record<string, NetIfaceStats>
	nvme?: NvmeStats
	wifi?: WiFiStats
	fan?: FanStats
	fan_steps?: FanSpeedStep[]
}

/** The fan control law. Every field is required: the server rejects unknown fields and refuses a curve with any field missing, because zeroing an absent one produces a curve the host will run and nobody wrote. */
export interface FanCurve {
	idle: number
	l0: number
	t1: number
	l1: number
	t2: number
	l2: number
	t3: number
	l3: number
	t4: number
	l4: number
	t5: number
	l5: number
	emerg: number
	e: number
	emerg_rel: number
	cap: number
	trim: number
	gain: number
	hyst: number
	dwell: number
	dead: number
	iv: number
}

export type FanMode = "auto" | "manual"

/** Bypasses the guard: a failed daemon restart answers with a code-less 500. */
export interface FanModeResult {
	mode: FanMode
	pfand_active: boolean
}

/** Each reading arrives with whether it was readable, because zero is a legal
 *  duty and a legal temperature and an absent reading must not borrow either.
 *
 *  `regress_pending` says whether a deadline is on record, and it is ambiguous
 *  on purpose: a manual level left in force by an agent restart looks exactly
 *  like one that was never set. No field tells those apart, so when it is false
 *  a panel shows no countdown and must not claim the level expires. */
export interface FanState {
	pfand_active: boolean
	level_read: boolean
	level: number | null
	/** Absent when the level was unreadable. Zero reads "auto", never "off". */
	level_label?: string
	pkg_temp_read: boolean
	pkg_temp_c: number | null
	regress_pending: boolean
	regress_at?: string
	/** Rides along, so an editor cannot be built against a stale curve. */
	curve: FanCurve | null
}

export interface FanAuditEntry {
	op: string
	outcome: string
	level: number
	/** RFC3339. */
	at: string
	actor?: string
	/** The refusal identifier, absent when the attempt was applied. */
	code?: FanRejectCode
	message?: string
}

export interface FanAuditTrail {
	items: FanAuditEntry[]
}
