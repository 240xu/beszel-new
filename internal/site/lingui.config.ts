import { defineConfig } from "@lingui/cli"

export default defineConfig({
	locales: [
		"en",
		"ar",
		"bg",
		"cs",
		"da",
		"de",
		"el",
		"es",
		"fa",
		"fr",
		"he",
		"hr",
		"hu",
		"id",
		"it",
		"ja",
		"ko",
		"nl",
		"no",
		"pl",
		"pt",
		"ro",
		"ru",
		"sl",
		"sr",
		"sv",
		"th",
		"tr",
		"ug",
		"uk",
		"uz",
		"vi",
		"zh",
		"zh-CN",
		"zh-HK",
	],
	sourceLocale: "en",
	// zh-HK is translated through Crowdin less completely than zh. Without this,
	// `lingui compile` fills an untranslated zh-HK entry with the ENGLISH source
	// string, so a Hong Kong reader sees English in exactly those spots. Listing
	// zh as a fallback makes the compiler write zh's translation instead; zh-HK's
	// own entries still win wherever they exist.
	fallbackLocales: {
		"zh-HK": "zh",
	},
	compileNamespace: "ts",
	formatOptions: {
		lineNumbers: false,
	},
	catalogs: [
		{
			path: "<rootDir>/src/locales/{locale}/{locale}",
			include: ["src"],
		},
	],
})
