import * as React from "react"

import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

export interface FormFieldControlProps {
	id: string
	"aria-describedby"?: string
	"aria-invalid"?: true
	"aria-required"?: true
}

export interface FormFieldProps {
	/** id for the control; a stable id is generated when omitted. */
	id?: string
	label: React.ReactNode
	hint?: React.ReactNode
	error?: React.ReactNode
	required?: boolean
	srOnlyLabel?: boolean
	className?: string
	labelClassName?: string
	hintClassName?: string
	errorClassName?: string
	children: React.ReactNode | ((control: FormFieldControlProps) => React.ReactNode)
}

function withFieldProps(child: React.ReactElement, control: FormFieldControlProps) {
	const childProps = child.props as Record<string, unknown>
	const describedBy =
		[childProps["aria-describedby"], control["aria-describedby"]].filter(Boolean).join(" ") || undefined

	return React.cloneElement(child as React.ReactElement<Record<string, unknown>>, {
		id: childProps.id ?? control.id,
		"aria-describedby": describedBy,
		"aria-invalid": control["aria-invalid"] ?? childProps["aria-invalid"],
		"aria-required": control["aria-required"] ?? childProps["aria-required"],
	})
}

/**
 * Label + control + hint + error, with the aria wiring a plain `<Label>` misses:
 * `aria-describedby` points at hint/error, `aria-invalid` follows the error state,
 * and the error text announces itself via `role="alert"`.
 */
export function FormField({
	id,
	label,
	hint,
	error,
	required,
	srOnlyLabel,
	className,
	labelClassName,
	hintClassName,
	errorClassName,
	children,
}: FormFieldProps) {
	const generatedId = React.useId()
	const controlId = id ?? `${generatedId}-control`
	const hintId = hint ? `${generatedId}-hint` : undefined
	const errorId = error ? `${generatedId}-error` : undefined
	const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined

	const control: FormFieldControlProps = {
		id: controlId,
		"aria-describedby": describedBy,
		"aria-invalid": error ? true : undefined,
		"aria-required": required ? true : undefined,
	}

	const rendered =
		typeof children === "function"
			? children(control)
			: React.isValidElement(children)
				? withFieldProps(children, control)
				: children

	// keep an id owned by the child, so the label points at the element that actually renders it
	const labelFor =
		typeof children !== "function" && React.isValidElement(children)
			? ((children.props as Record<string, unknown>).id as string | undefined) ?? controlId
			: controlId

	return (
		<div className={cn("grid gap-2", className)}>
			<Label htmlFor={labelFor} className={cn(srOnlyLabel && "sr-only", labelClassName)}>
				{label}
			</Label>
			{rendered}
			{hint && (
				<p id={hintId} className={cn("text-xs leading-relaxed text-muted-foreground", hintClassName)}>
					{hint}
				</p>
			)}
			{error && (
				<p
					id={errorId}
					role="alert"
					className={cn("text-xs font-medium leading-relaxed text-destructive-text", errorClassName)}
				>
					{error}
				</p>
			)}
		</div>
	)
}
