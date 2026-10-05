package plus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// collectDPTF reads Intel DPTF thermal zones. These are more accurate than
// hwmon on this platform and include zones hwmon never sees (B0D4,
// x86_pkg_temp, SEN1-6).
//
// root is the sysfs mount point, so the zones live at
// <root>/class/thermal/thermal_zone*, not at <root>/thermal_zone*. In
// production Collect passes ProdRoot ("/sys"), so a pre-joined path here would
// glob nothing and the collector would silently report no temperatures.
//
// ACPI zone types are keys verbatim: they contain spaces ("INT3400 Thermal")
// and underscores ("pch_cannonlake", "iwlwifi_1"), so the type file is trimmed
// as a whole rather than split on whitespace.
func collectDPTF(root string) map[string]float64 {
	matches, err := filepath.Glob(filepath.Join(root, "class", "thermal", "thermal_zone*"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make(map[string]float64)
	for _, dir := range matches {
		name, err := readTrimmed(filepath.Join(dir, "type"))
		if err != nil || name == "" {
			continue
		}
		raw, err := readTrimmed(filepath.Join(dir, "temp"))
		if err != nil {
			continue
		}
		milli, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		celsius := float64(milli) / 1000.0
		// -274000 marks a zone whose trip points are disabled; it is not a
		// reading. Anything outside plausible silicon range is also rejected.
		if celsius < -50 || celsius > 150 {
			continue
		}
		out[name] = celsius
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readUint(path string) (uint64, error) {
	s, err := readTrimmed(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}
