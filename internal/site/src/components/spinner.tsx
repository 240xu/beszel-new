import { LoaderCircleIcon } from "lucide-react"
import { cn } from "@/lib/utils"

export default function ({ msg, className }: { msg?: string; className?: string }) {
	return (
		<div
			className={cn(className, "pointer-events-none flex flex-col items-center justify-center h-full absolute inset-0")}
			role={msg ? "status" : undefined}
			aria-live={msg ? "polite" : undefined}
			aria-hidden={msg ? undefined : true}
		>
			{msg ? (
				<p className={"opacity-60 mb-2 text-center text-sm px-4"}>{msg}</p>
			) : (
				<LoaderCircleIcon className="animate-spin h-10 w-10 opacity-60" />
			)}
		</div>
	)
}
