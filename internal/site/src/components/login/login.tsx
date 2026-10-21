import { t } from "@lingui/core/macro"
import { useStore } from "@nanostores/react"
import type { AuthMethodsList } from "pocketbase"
import { useEffect, useMemo, useState } from "react"
import { UserAuthForm } from "@/components/login/auth-form"
import { Card } from "@/components/ui/card"
import { pb } from "@/lib/api"
import { Logo } from "../logo"
import { ModeToggle } from "../mode-toggle"
import { $router } from "../router"
import ForgotPassword from "./forgot-pass-form"
import { OtpRequestForm } from "./otp-forms"

export default function () {
	const page = useStore($router)
	const [isFirstRun, setFirstRun] = useState(false)
	const [authMethods, setAuthMethods] = useState<AuthMethodsList>()

	useEffect(() => {
		document.title = t`Login` + " / Beszel"

		pb.send("/api/beszel/first-run", {}).then(({ firstRun }) => {
			setFirstRun(firstRun)
		})
	}, [])

	useEffect(() => {
		pb.collection("users")
			.listAuthMethods()
			.then((methods) => {
				setAuthMethods(methods)
			})
	}, [])

	const subtitle = useMemo(() => {
		if (isFirstRun) {
			return t`Please create an admin account`
		} else if (page?.route === "forgot_password") {
			return t`Enter email address to reset password`
		} else if (page?.route === "request_otp") {
			return t`Request a one-time password`
		} else {
			return t`Please sign in to your account`
		}
	}, [isFirstRun, page])

	if (!authMethods) {
		return null
	}

	return (
		<div className="relative min-h-svh grid items-center px-4 py-12">
			<div className="absolute top-3 end-3">
				<ModeToggle />
			</div>
			<Card className="w-full max-w-sm mx-auto p-5 sm:p-6">
				<div className="grid gap-6">
					<div className="grid gap-2 text-center">
						<h1 className="flex items-center justify-center">
							<Logo className="h-7 fill-foreground" />
							<span className="sr-only">Beszel</span>
						</h1>
						<p className="text-sm text-muted-foreground">{subtitle}</p>
					</div>
					{page?.route === "forgot_password" ? (
						<ForgotPassword />
					) : page?.route === "request_otp" ? (
						<OtpRequestForm />
					) : (
						<UserAuthForm isFirstRun={isFirstRun} authMethods={authMethods} />
					)}
				</div>
			</Card>
		</div>
	)
}
