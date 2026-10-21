import { useLingui } from "@lingui/react/macro"
import { Badge } from "@/components/ui/badge"
import { SystemStatus } from "@/lib/enums"
import { cn } from "@/lib/utils"
import type { SystemRecord } from "@/types"

type Status = SystemRecord["status"]

const variants = {
	[SystemStatus.Up]: "success",
	[SystemStatus.Down]: "danger",
	[SystemStatus.Paused]: "secondary",
	[SystemStatus.Pending]: "warning",
} as const

export function useSystemStatusLabel(status: Status): string {
	const { t } = useLingui()
	if (status === SystemStatus.Up) return t`Up`
	if (status === SystemStatus.Down) return t`Down`
	if (status === SystemStatus.Paused) return t`Paused`
	return t({ message: "Pending", comment: "Context: System status is pending its first connection" })
}

/** Status chip: semantic Badge variant plus a dot and the translated status text,
 *  so the state never depends on colour alone. */
export function SystemStatusBadge({ status, className }: { status: Status; className?: string }) {
	const label = useSystemStatusLabel(status)
	return (
		<Badge variant={variants[status] ?? "outline"} className={cn("gap-1.5 whitespace-nowrap", className)}>
			<span className="size-1.5 shrink-0 rounded-full bg-current" aria-hidden="true" />
			{label}
		</Badge>
	)
}
