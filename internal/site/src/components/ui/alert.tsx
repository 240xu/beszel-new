import { cva, type VariantProps } from "class-variance-authority"
import * as React from "react"

import { cn } from "@/lib/utils"

const alertVariants = cva(
	"relative w-full rounded-lg border p-4 [&>svg~*]:ps-7 [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:start-4 [&>svg]:top-4",
	{
		variants: {
			variant: {
				default: "border-border bg-card text-foreground [&>svg]:text-foreground",
				destructive:
					"border-destructive/40 bg-destructive/5 text-destructive-text [&>svg]:text-destructive-text",
				success: "border-success/40 bg-success-muted text-success-text [&>svg]:text-success-text",
				warning: "border-warning/40 bg-warning-muted text-warning-text [&>svg]:text-warning-text",
				info: "border-info/40 bg-info-muted text-info-text [&>svg]:text-info-text",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	}
)

const Alert = React.forwardRef<
	HTMLDivElement,
	React.HTMLAttributes<HTMLDivElement> & VariantProps<typeof alertVariants>
>(({ className, variant, ...props }, ref) => (
	<div ref={ref} role="alert" className={cn(alertVariants({ variant }), className)} {...props} />
))
Alert.displayName = "Alert"

const AlertTitle = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLHeadingElement>>(
	({ className, ...props }, ref) => (
		<h5 ref={ref} className={cn("mb-1 -mt-0.5 font-semibold leading-tight tracking-tight", className)} {...props} />
	)
)
AlertTitle.displayName = "AlertTitle"

const AlertDescription = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLParagraphElement>>(
	({ className, ...props }, ref) => (
		<div ref={ref} className={cn("text-sm [&_p]:leading-relaxed", className)} {...props} />
	)
)
AlertDescription.displayName = "AlertDescription"

export { Alert, AlertTitle, AlertDescription }
