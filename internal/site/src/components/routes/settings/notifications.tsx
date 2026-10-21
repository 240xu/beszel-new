import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import { BellIcon, LoaderCircleIcon, PlusIcon, SaveIcon, Trash2Icon } from "lucide-react"
import { type ChangeEventHandler, useEffect, useState } from "react"
import * as v from "valibot"
import { prependBasePath } from "@/components/router"
import { Button } from "@/components/ui/button"
import { FormField } from "@/components/ui/form-field"
import { Input } from "@/components/ui/input"
import { InputTags } from "@/components/ui/input-tags"
import { Separator } from "@/components/ui/separator"
import { toast } from "@/components/ui/use-toast"
import { isAdmin, pb } from "@/lib/api"
import type { UserSettings } from "@/types"
import { saveSettings } from "./layout"
import { SettingsSaveBar } from "./save-bar"
import { QuietHours } from "./quiet-hours"
import type { ClientResponseError } from "pocketbase"

interface ShoutrrrUrlCardProps {
	url: string
	onUrlChange: ChangeEventHandler<HTMLInputElement>
	onRemove: () => void
}

const NotificationSchema = v.object({
	emails: v.array(v.pipe(v.string(), v.rfcEmail())),
	webhooks: v.array(v.pipe(v.string(), v.url())),
})

const SettingsNotificationsPage = ({ userSettings }: { userSettings: UserSettings }) => {
	const [webhooks, setWebhooks] = useState(userSettings.webhooks ?? [])
	const [emails, setEmails] = useState<string[]>(userSettings.emails ?? [])
	const [isLoading, setIsLoading] = useState(false)

	// update values when userSettings changes
	useEffect(() => {
		setWebhooks(userSettings.webhooks ?? [])
		setEmails(userSettings.emails ?? [])
	}, [userSettings])

	function addWebhook() {
		setWebhooks([...webhooks, ""])
		// focus on the new input
		queueMicrotask(() => {
			const inputs = document.querySelectorAll("#webhooks input") as NodeListOf<HTMLInputElement>
			inputs[inputs.length - 1]?.focus()
		})
	}
	const removeWebhook = (index: number) => setWebhooks(webhooks.filter((_, i) => i !== index))

	function updateWebhook(index: number, value: string) {
		const newWebhooks = [...webhooks]
		newWebhooks[index] = value
		setWebhooks(newWebhooks)
	}

	async function updateSettings() {
		setIsLoading(true)
		try {
			const parsedData = v.parse(NotificationSchema, { emails, webhooks })
			await saveSettings(parsedData)
		} catch (e: unknown) {
			toast({
				title: t`Failed to save settings`,
				description: (e as Error).message,
				variant: "destructive",
			})
		}
		setIsLoading(false)
	}

	return (
		<div>
			<section className="grid gap-2">
				<h2 className="text-lg font-semibold tracking-tight">
					<Trans>Notifications</Trans>
				</h2>
				<p className="text-sm text-muted-foreground leading-relaxed">
					<Trans>Configure how you receive alert notifications.</Trans>
				</p>
				<p className="text-sm text-muted-foreground mt-1.5 leading-relaxed">
					<Trans>
						Looking instead for where to create alerts? Click the bell <BellIcon className="inline h-4 w-4" /> icons in
						the systems table.
					</Trans>
				</p>
			</section>
			<Separator />
			<div className="grid gap-6">
				<section className="grid gap-3">
					<div className="grid gap-1">
						<h3 className="text-sm font-semibold">
							<Trans>Email notifications</Trans>
						</h3>
						{isAdmin() && (
							<p className="text-sm text-muted-foreground leading-relaxed">
								<Trans>
									Please{" "}
									<a href={prependBasePath("/_/#/settings/mail")} className="link" target="_blank">
										configure an SMTP server
									</a>{" "}
									to ensure alerts are delivered.
								</Trans>
							</p>
						)}
					</div>
					<FormField
						id="email"
						label={<Trans>To email(s)</Trans>}
						hint={<Trans>Save address using enter key or comma. Leave blank to disable email notifications.</Trans>}
					>
						<InputTags
							value={emails}
							onChange={setEmails}
							placeholder={t`Enter email address...`}
							className="w-full"
							type="email"
						/>
					</FormField>
				</section>
				<Separator />
				<div className="space-y-3">
					<div className="grid grid-cols-1 sm:flex items-center justify-between gap-4">
						<div className="grid gap-1">
							<h3 className="text-sm font-semibold">
								<Trans>Webhook / Push notifications</Trans>
							</h3>
							<p className="text-sm text-muted-foreground leading-relaxed">
								<Trans>
									Beszel uses{" "}
									<a href="https://beszel.dev/guide/notifications" target="_blank" className="link" rel="noopener">
										Shoutrrr
									</a>{" "}
									to integrate with popular notification services.
								</Trans>
							</p>
						</div>
						<Button type="button" variant="outline" className="h-10 shrink-0" onClick={addWebhook}>
							<PlusIcon className="size-4" />
							<span className="ms-1">
								<Trans>Add URL</Trans>
							</span>
						</Button>
					</div>
					{webhooks.length > 0 && (
						<div className="grid gap-2.5" id="webhooks">
							{webhooks.map((webhook, index) => (
								<ShoutrrrUrlCard
									key={index}
									url={webhook}
									onUrlChange={(e: React.ChangeEvent<HTMLInputElement>) => updateWebhook(index, e.target.value)}
									onRemove={() => removeWebhook(index)}
								/>
							))}
						</div>
					)}
				</div>
				<Separator />
				<div className="space-y-3">
					<QuietHours />
				</div>
				<Separator />
				<SettingsSaveBar>
					<Button
						type="button"
						className="flex items-center gap-1.5 disabled:opacity-100"
						onClick={updateSettings}
						disabled={isLoading}
					>
						{isLoading ? <LoaderCircleIcon className="h-4 w-4 animate-spin" /> : <SaveIcon className="h-4 w-4" />}
						<Trans>Save Settings</Trans>
					</Button>
				</SettingsSaveBar>
			</div>
		</div>
	)
}

function showTestNotificationError(msg: string) {
	toast({
		title: t`Error`,
		description: msg ?? t`Failed to send test notification`,
		variant: "destructive",
	})
}

const ShoutrrrUrlCard = ({ url, onUrlChange, onRemove }: ShoutrrrUrlCardProps) => {
	const [isLoading, setIsLoading] = useState(false)

	const sendTestNotification = async () => {
		setIsLoading(true)
		try {
			const res = await pb.send("/api/beszel/test-notification", { method: "POST", body: { url } })
			if ("err" in res && !res.err) {
				toast({
					title: t`Test notification sent`,
					description: t`Check your notification service`,
				})
			} else {
				showTestNotificationError(res.err)
			}
		} catch (e: unknown) {
			showTestNotificationError((e as ClientResponseError).data?.message)
		} finally {
			setIsLoading(false)
		}
	}

	return (
		<div className="bg-muted/30 rounded-md p-2 md:p-3">
			<div className="flex items-center gap-1">
				<FormField label={<Trans>Webhook URL</Trans>} srOnlyLabel className="flex-1">
					<Input
						type="url"
						className="light:bg-card"
						required
						placeholder="generic://webhook.site/xxxxxx"
						value={url}
						onChange={onUrlChange}
					/>
				</FormField>
				<Button type="button" variant="outline" disabled={isLoading || url === ""} onClick={sendTestNotification}>
					{isLoading ? (
						<LoaderCircleIcon className="h-4 w-4 animate-spin" />
					) : (
						<span>
							<Trans>
								Test <span className="hidden sm:inline">URL</span>
							</Trans>
						</span>
					)}
				</Button>
				<Button type="button" variant="outline" size="icon" className="shrink-0" aria-label={t`Delete`} onClick={onRemove}>
					<Trash2Icon className="h-4 w-4" />
				</Button>
			</div>
		</div>
	)
}

export default SettingsNotificationsPage
