import { t } from "@lingui/core/macro"
import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { getPagePath, redirectPage } from "@nanostores/router"
import {
	AlertOctagonIcon,
	BellIcon,
	FileSlidersIcon,
	FingerprintIcon,
	HeartPulseIcon,
	SettingsIcon,
} from "lucide-react"
import { lazy, useEffect } from "react"
import { $router } from "@/components/router.tsx"
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card.tsx"
import { toast } from "@/components/ui/use-toast.ts"
import { saveUserSettings } from "@/lib/api"
import { $userSettings } from "@/lib/stores.ts"
import type { UserSettings } from "@/types"
import { Separator } from "../../ui/separator"
import { SidebarNav } from "./sidebar-nav.tsx"

const generalSettingsImport = () => import("./general.tsx")
const notificationsSettingsImport = () => import("./notifications.tsx")
const configYamlSettingsImport = () => import("./config-yaml.tsx")
const fingerprintsSettingsImport = () => import("./tokens-fingerprints.tsx")
const alertsHistoryDataTableSettingsImport = () => import("./alerts-history-data-table.tsx")
const heartbeatSettingsImport = () => import("./heartbeat.tsx")

const GeneralSettings = lazy(generalSettingsImport)
const NotificationsSettings = lazy(notificationsSettingsImport)
const ConfigYamlSettings = lazy(configYamlSettingsImport)
const FingerprintsSettings = lazy(fingerprintsSettingsImport)
const AlertsHistoryDataTableSettings = lazy(alertsHistoryDataTableSettingsImport)
const HeartbeatSettings = lazy(heartbeatSettingsImport)

export async function saveSettings(newSettings: Partial<UserSettings>) {
	try {
		await saveUserSettings(newSettings)
		toast({
			title: t`Settings saved`,
			description: t`Your user settings have been updated.`,
		})
	} catch (e) {
		console.error("save settings", e)
		toast({
			title: t`Failed to save settings`,
			description: t`Check logs for more details.`,
			variant: "destructive",
		})
	}
}

export default function SettingsLayout() {
	const { t } = useLingui()

	const sidebarNavItems = [
		{
			title: t({ message: `General`, comment: "Context: General settings" }),
			href: getPagePath($router, "settings", { name: "general" }),
			icon: SettingsIcon,
		},
		{
			title: t`Notifications`,
			href: getPagePath($router, "settings", { name: "notifications" }),
			icon: BellIcon,
			preload: notificationsSettingsImport,
		},
		{
			title: t`Tokens & Fingerprints`,
			href: getPagePath($router, "settings", { name: "tokens" }),
			icon: FingerprintIcon,
			noReadOnly: true,
			preload: fingerprintsSettingsImport,
		},
		{
			title: t`Alert History`,
			href: getPagePath($router, "settings", { name: "alert-history" }),
			icon: AlertOctagonIcon,
			preload: alertsHistoryDataTableSettingsImport,
		},
		{
			title: t`Heartbeat`,
			href: getPagePath($router, "settings", { name: "heartbeat" }),
			icon: HeartPulseIcon,
			admin: true,
			preload: heartbeatSettingsImport,
		},
		{
			title: t`YAML Config`,
			href: getPagePath($router, "settings", { name: "config" }),
			icon: FileSlidersIcon,
			admin: true,
			preload: configYamlSettingsImport,
		},
	]

	const page = useStore($router)

	// biome-ignore lint/correctness/useExhaustiveDependencies: no dependencies
	useEffect(() => {
		document.title = `${t`Settings`} / Beszel`
		// @ts-expect-error redirect to account page if no page is specified
		if (!page?.params?.name) {
			redirectPage($router, "settings", { name: "general" })
		}
	}, [])

	return (
		<Card className="p-5 sm:p-6 min-h-96 mb-14">
			<CardHeader className="p-0">
				<h1 className="text-card-title font-semibold leading-none tracking-tight mb-1">
					<Trans>Settings</Trans>
				</h1>
				<CardDescription>
					<Trans>Manage display and notification preferences.</Trans>
				</CardDescription>
			</CardHeader>
			<CardContent className="p-0">
				<Separator className="hidden md:block my-4" />
				<div className="flex flex-col gap-4 md:flex-row md:gap-6 lg:gap-10">
					<aside className="md:max-w-52 min-w-40">
						<SidebarNav items={sidebarNavItems} />
					</aside>
					<div className="flex-1 min-w-0">
						{/* @ts-ignore */}
						<SettingsContent name={page?.params?.name ?? "general"} />
					</div>
				</div>
			</CardContent>
		</Card>
	)
}

function SettingsContent({ name }: { name: string }) {
	const userSettings = useStore($userSettings)

	switch (name) {
		case "general":
			return <GeneralSettings userSettings={userSettings} />
		case "notifications":
			return <NotificationsSettings userSettings={userSettings} />
		case "config":
			return <ConfigYamlSettings />
		case "tokens":
			return <FingerprintsSettings />
		case "alert-history":
			return <AlertsHistoryDataTableSettings />
		case "heartbeat":
			return <HeartbeatSettings />
	}
}
