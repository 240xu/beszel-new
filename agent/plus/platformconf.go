package plus

import (
	"fmt"
	"strings"
)

const (
	ProdPlatformConf    = "/etc/pfan/platform.conf"
	ProdFanCandidates   = "/etc/pfan/fan-candidates.txt"
	platformThermalLeaf = "temp"
)

// PlatformConf is the machine's ACPI wiring, as generated on the host.
//
// ECRoot and FANDevice stay separate keys. The host records machines whose fan
// device sits outside the embedded-controller subtree, so deriving the device from
// the root is a documented error here, not a shortcut. ThermalZone is a file path
// rather than a zone name because its index differs per machine and must be
// probed; the host treats a missing value as a reason to run the fan full speed.
type PlatformConf struct {
	ECRoot      string
	FANDevice   string
	ThermalZone string
	Dev         string
	ECCmd       string

	// extra keeps unmodelled keys: the host loader returns its whole dictionary,
	// and dropping the rest would lose a setting without saying so.
	extra map[string]string
}

// platformKeys is the set the host's own loader requires.
var platformKeys = []string{"EC_ROOT", "FAN_DEVICE", "THERMAL_ZONE", "DEV", "EC_CMD"}

// Get returns an unmodelled key, so a caller can read an optional setting without
// this type having to grow a field for it.
func (p *PlatformConf) Get(key string) (string, bool) {
	switch key {
	case "EC_ROOT":
		return p.ECRoot, true
	case "FAN_DEVICE":
		return p.FANDevice, true
	case "THERMAL_ZONE":
		return p.ThermalZone, true
	case "DEV":
		return p.Dev, true
	case "EC_CMD":
		return p.ECCmd, true
	}
	v, ok := p.extra[key]
	return v, ok
}

// ParsePlatformConf reads the host's platform file. Unlike the curve file this one
// is never sourced as a script, so only the shape is enforced, and the split is on
// the first equals: the values are ACPI paths that may hold padding or an equals
// of their own, and both host readers accept such lines. The host's generation rule
// keeps values padded-free anyway, so a padded line is read here, never written.
// curveHexDigits admits the command the host writes, whose digits run past nine.
func curveHexDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func ParsePlatformConf(src string) (*PlatformConf, error) {
	raw := map[string]string{}
	for _, line := range strings.Split(src, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		raw[strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
	}
	var missing []string
	for _, k := range platformKeys {
		if raw[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("platform config is missing %s", strings.Join(missing, ", "))
	}
	p := &PlatformConf{
		ECRoot:      raw["EC_ROOT"],
		FANDevice:   raw["FAN_DEVICE"],
		ThermalZone: raw["THERMAL_ZONE"],
		Dev:         raw["DEV"],
		ECCmd:       raw["EC_CMD"],
		extra:       raw,
	}
	for _, acpi := range []struct{ name, val string }{{"EC_ROOT", p.ECRoot}, {"FAN_DEVICE", p.FANDevice}} {
		if strings.ContainsAny(acpi.val, " \t") {
			return nil, fmt.Errorf("%s=%q must be a single ACPI path", acpi.name, acpi.val)
		}
	}
	// The zone is read as a file, so a bare name or a directory is refused rather
	// than opened: the index it embeds is machine-specific and is not a name.
	if !strings.HasPrefix(p.ThermalZone, "/") ||
		!strings.HasSuffix(p.ThermalZone, "/"+platformThermalLeaf) ||
		strings.Count(p.ThermalZone, "/") < 3 {
		return nil, fmt.Errorf("THERMAL_ZONE=%q must be the full path of a thermal zone temp file", p.ThermalZone)
	}
	if !strings.HasPrefix(p.Dev, "/") {
		return nil, fmt.Errorf("DEV=%q must be an absolute device path", p.Dev)
	}
	if !strings.HasPrefix(p.ECCmd, "0x") || !curveHexDigits(strings.TrimPrefix(p.ECCmd, "0x")) {
		return nil, fmt.Errorf("EC_CMD=%q must be a 0x-prefixed hexadecimal command", p.ECCmd)
	}
	return p, nil
}

// FanCandidate is one (EC root, fan device) pairing. Both columns are kept because
// the list covers machines whose device is not under the configured root: probing
// adopts the first pairing that answers, so they are alternatives, not attributes.
type FanCandidate struct {
	ECRoot    string
	FANDevice string
}

// ParseFanCandidates reads the candidate list. Exactly two columns are required:
// a third is a typo in one of the two, and reading the first two anyway would
// discard whichever column was wrong while leaving a plausible-looking entry.
func ParseFanCandidates(src string) ([]FanCandidate, error) {
	var out []FanCandidate
	for n, raw := range strings.Split(src, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: expected an EC root and a fan device, got %d field(s): %q",
				n+1, len(fields), strings.TrimSpace(raw))
		}
		out = append(out, FanCandidate{ECRoot: fields[0], FANDevice: fields[1]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("candidate list holds no pairings")
	}
	return out, nil
}
