import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { LoaderCircle, SendHorizonalIcon } from "lucide-react"
import { useCallback, useState } from "react"
import { FormField } from "@/components/ui/form-field"
import { InputOTP, InputOTPGroup, InputOTPSlot } from "@/components/ui/otp"
import { pb } from "@/lib/api"
import { $authenticated } from "@/lib/stores"
import { cn } from "@/lib/utils"
import { $router } from "../router"
import { buttonVariants } from "../ui/button"
import { Input } from "../ui/input"
import { showLoginFaliedToast } from "./auth-form"

export function OtpInputForm({ otpId, mfaId }: { otpId: string; mfaId: string }) {
	const [value, setValue] = useState("")

	if (value.length === 6) {
		pb.collection("users")
			.authWithOTP(otpId, value, { mfaId })
			.then(() => {
				$router.open("/")
				$authenticated.set(true)
			})
			.catch((err) => {
				showLoginFaliedToast(err.message)
			})
	}

	return (
		<div className="grid gap-3 items-center justify-center">
			<InputOTP maxLength={6} value={value} onChange={setValue} autoFocus aria-label={t`One-time password`}>
				<InputOTPGroup>
					{Array.from({ length: 6 }).map((_, i) => (
						<InputOTPSlot key={i} index={i} />
					))}
				</InputOTPGroup>
			</InputOTP>
			<div className="text-center text-sm text-muted-foreground">
				<Trans>Enter your one-time password.</Trans>
			</div>
		</div>
	)
}

export function OtpRequestForm() {
	const [isLoading, setIsLoading] = useState<boolean>(false)
	const [email, setEmail] = useState("")
	const [otpId, setOtpId] = useState<string | undefined>()

	const handleSubmit = useCallback(
		async (e: React.FormEvent<HTMLFormElement>) => {
			e.preventDefault()
			setIsLoading(true)
			try {
				// console.log(email)
				const { otpId } = await pb.collection("users").requestOTP(email)
				setOtpId(otpId)
			} catch (e: any) {
				showLoginFaliedToast(e?.message)
			} finally {
				setIsLoading(false)
				setEmail("")
			}
		},
		[email]
	)

	if (otpId) {
		return <OtpInputForm otpId={otpId} mfaId={""} />
	}

	return (
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
					<Trans>Request OTP</Trans>
				</button>
			</div>
		</form>
	)
}
