import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { LoaderCircle, SendHorizonalIcon } from "lucide-react"
import { useCallback, useState } from "react"
import { pb } from "@/lib/api"
import { cn } from "@/lib/utils"
import { buttonVariants } from "../ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "../ui/dialog"
import { FormField } from "../ui/form-field"
import { Input } from "../ui/input"
import { toast } from "../ui/use-toast"

const showLoginFaliedToast = () => {
	toast({
		title: t`Login attempt failed`,
		description: t`Please check your credentials and try again`,
		variant: "destructive",
	})
}

export default function ForgotPassword() {
	const [isLoading, setIsLoading] = useState<boolean>(false)
	const [email, setEmail] = useState("")

	const handleSubmit = useCallback(
		async (e: React.FormEvent<HTMLFormElement>) => {
			e.preventDefault()
			setIsLoading(true)
			try {
				// console.log(email)
				await pb.collection("users").requestPasswordReset(email)
				toast({
					title: t`Password reset request received`,
					description: t`Check ${email} for a reset link.`,
				})
			} catch (e) {
				showLoginFaliedToast()
			} finally {
				setIsLoading(false)
				setEmail("")
			}
		},
		[email]
	)

	return (
		<>
			<form onSubmit={handleSubmit}>
				<div className="grid gap-4">
					<FormField id="email" label={<Trans>Email</Trans>} required>
						<Input
							value={email}
							onChange={(e) => setEmail(e.target.value)}
							name="email"
							required
							placeholder="name@example.com"
							type="email"
							autoCapitalize="none"
							autoComplete="email"
							autoCorrect="off"
							disabled={isLoading}
						/>
					</FormField>
					<button className={cn(buttonVariants())} disabled={isLoading}>
						{isLoading ? (
							<LoaderCircle className="me-2 h-4 w-4 animate-spin" />
						) : (
							<SendHorizonalIcon className="me-2 h-4 w-4" />
						)}
						<Trans>Reset Password</Trans>
					</button>
				</div>
			</form>
			<Dialog>
				<DialogTrigger asChild>
					<button className="text-sm mx-auto hover:text-brand underline underline-offset-4 opacity-70 hover:opacity-100 transition-opacity">
						<Trans>Command line instructions</Trans>
					</button>
				</DialogTrigger>
				<DialogContent className="max-w-[41em]">
					<DialogHeader>
						<DialogTitle>
							<Trans>Command line instructions</Trans>
						</DialogTitle>
					</DialogHeader>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>
							If you've lost the password to your admin account, you may reset it using the following command.
						</Trans>
					</p>
					<p className="text-sm text-muted-foreground leading-relaxed">
						<Trans>Then log into the backend and reset your user account password in the users table.</Trans>
					</p>
					<code className="bg-muted rounded-sm py-0.5 px-2.5 me-auto text-sm">
						./beszel superuser upsert user@example.com password
					</code>
					<code className="bg-muted rounded-sm py-0.5 px-2.5 me-auto text-sm">
						docker exec beszel /beszel superuser upsert name@example.com password
					</code>
				</DialogContent>
			</Dialog>
		</>
	)
}
