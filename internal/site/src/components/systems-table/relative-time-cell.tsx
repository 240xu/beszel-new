import { useLingui } from "@lingui/react/macro"
import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "./relative-time"

/** Relative timestamp that refreshes itself (30s) while the page stays open. */
export function RelativeTimeCell({ value, className }: { value: string; className?: string }) {
	const { i18n } = useLingui()
	const [now, setNow] = useState(() => Date.now())

	useEffect(() => {
		const timer = window.setInterval(() => setNow(Date.now()), 30_000)
		return () => window.clearInterval(timer)
	}, [])

	const date = new Date(value)
	if (Number.isNaN(date.getTime())) {
		return <span className={cn("text-muted-foreground", className)}>—</span>
	}

	return (
		<time
			dateTime={date.toISOString()}
			title={date.toLocaleString(i18n.locale)}
			className={cn("whitespace-nowrap tabular-nums", className)}
		>
			{formatRelativeTime(date, now, i18n.locale)}
		</time>
	)
}
