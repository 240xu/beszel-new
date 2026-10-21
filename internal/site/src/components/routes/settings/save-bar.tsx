import type * as React from "react"

import { cn } from "@/lib/utils"

/**
 * Sticky action row for the settings forms. It stays in normal flow, so the
 * content above can always be scrolled clear of it, and the negative bottom
 * margin extends the surface to the card edge.
 */
export function SettingsSaveBar({ className, children }: { className?: string; children: React.ReactNode }) {
	return (
		<div
			className={cn(
				"sticky bottom-0 z-10 -mb-5 sm:-mb-6 flex justify-end border-t bg-card py-3",
				className
			)}
		>
			{children}
		</div>
	)
}
