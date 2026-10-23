package plus

import "os"

// envOr returns the environment value for name, or fallback when it is unset or
// empty. An empty value is treated as unset so a blank variable cannot name an
// empty path: this package reads paths straight from these results.
//
// It lives in an untagged file because callers span build tags: profile.go is
// platform-neutral and uses it for the DMI/hwmon/sysfs roots, while the Linux
// fanctl file uses it for device paths. Declaring it beside the Linux-only
// fanctl code made every non-Linux build fail to resolve the profile callers,
// which in turn made the !linux stubs unreachable.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
