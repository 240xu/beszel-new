import { cva, type VariantProps } from "class-variance-authority"
import type * as React from "react"

import { cn } from "@/lib/utils"

const badgeVariants = cva(
	"inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors duration-150 ease-out focus:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
	{
		variants: {
			variant: {
				default: "border-transparent bg-primary text-primary-foreground",
				secondary: "border-transparent bg-secondary text-secondary-foreground",
				destructive: "border-transparent bg-destructive text-destructive-foreground",
				outline: "border-border-strong text-foreground",
				success: "border-transparent bg-success-muted text-success-text",
				warning: "border-transparent bg-warning-muted text-warning-text",
				danger: "border-transparent bg-destructive/10 text-destructive-text",
				info: "border-transparent bg-info-muted text-info-text",
				neutral: "border-transparent bg-muted text-muted-foreground",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	}
)

export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement>, VariantProps<typeof badgeVariants> {
	/** Shape + text: prefixes a status dot that inherits the badge text color. */
	dot?: boolean
}

function Badge({ className, variant, dot = false, children, ...props }: BadgeProps) {
	return (
		<div className={cn(badgeVariants({ variant }), className)} {...props}>
			{dot && <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-current" />}
			{children}
		</div>
	)
}

export { Badge, badgeVariants }
