package plus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// collectDPTF reads Intel DPTF thermal zones.
//
// The value here is coverage, not accuracy: sampled at the same instant the
// package zone tracks hwmon coretemp to within 1 C. hwmon never reports
// INT3400 Thermal or SEN1/2/3/4/6, but the remaining keys are not extra
// sensors either -- B0D4 and x86_pkg_temp are two ACPI names for the hwmon
// package die, and pch_cannonlake and iwlwifi_1 shadow hwmon3 and hwmon5, so
// the 10 zones on this NAS carry 9 distinct readings. Collapsing the duplicates
// is a display concern, so this collector reports each zone under its own ACPI
// type verbatim.
//
// Reading sysfs directly also sidesteps the panic-recovery wrapper gopsutil
// needs in sensors.go; os.ReadFile has no such failure mode.
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
		// The window is closed: -50.0 and 150.0 are kept, only strictly
		// further out is dropped.
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
