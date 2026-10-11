package plus

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	ProdPlatformConf    = "/etc/pfan/platform.conf"
	ProdFanCandidates   = "/etc/pfan/fan-candidates.txt"
	platformThermalLeaf = "temp"
)

// ProdECSysRoot is where the kernel publishes its EC handles. ec_sys is GPL-2.0;
// using its interface from an MIT agent links none of it in, like reading procfs.
const ProdECSysRoot = "/sys/kernel/debug/ec"

// PlatformConf is the machine's ACPI wiring, as generated on the host.
//
// ECRoot and FANDevice stay separate keys: the host records machines whose fan
// device sits outside the EC subtree, so deriving one from the other is a
// documented error. ThermalZone is a file path because its index differs per
// machine and must be probed; the host treats a missing value as a reason to run
// the fan full speed.
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

// ECBackend names how one EC byte is reached. A closed set: an unknown value is
// refused, because a default decides which controller is read.
type ECBackend string

const (
	// ECBackendACPI is the EC88 method this fork has always used, and the only
	// path observed on real hardware.
	ECBackendACPI ECBackend = "acpi"
	// ECBackendECSys reads the kernel's ec_sys handle. Portable, read-only here.
	ECBackendECSys ECBackend = "ec_sys"
)

// Why an EC configuration was refused: named so a caller switches on them.
var (
	ErrECBackendUnknown = errors.New("EC_BACKEND names no known EC backend")
	ErrECIONodeRequired = errors.New("EC_BACKEND=ec_sys needs EC_IO naming the kernel EC node")
	ErrECIOInvalid      = errors.New("EC_IO is not a kernel EC node")
	ErrECPairMismatch   = errors.New("EC_ROOT and FAN_DEVICE do not name one controller")
)

// ECConf is the machine's EC wiring as the fan code consumes it. Before it all
// four keys were parsed, validated, tested and discarded.
type ECConf struct {
	// ECRoot prefixes the EC register access, FANDevice the fan methods.
	// Independent keys: real machines differ, see the platform file's own warning.
	ECRoot    string
	FANDevice string
	Dev       string // node an ACPI expression is written to
	Cmd       uint64 // command byte every EC register access carries
	Backend   ECBackend
	IO        string // ec_sys node; empty on ACPI, which uses ECRoot instead
}

// EC derives the wiring and refuses what cannot be right. It validates only its
// own keys: ParsePlatformConf is unchanged, so the hub's thermal read still
// works on a file whose EC half nothing but the fan reads is unusable.
func (p *PlatformConf) EC() (ECConf, error) {
	cmd, err := strconv.ParseUint(strings.TrimPrefix(p.ECCmd, "0x"), 16, 64)
	if err != nil {
		// Unreachable via ParsePlatformConf, which admits only 0x-prefixed hex.
		// Guessing a command byte is the one thing an EC access must never do.
		return ECConf{}, fmt.Errorf("EC_CMD=%q is not hexadecimal: %w", p.ECCmd, err)
	}
	conf := ECConf{
		ECRoot:    p.ECRoot,
		FANDevice: p.FANDevice,
		Dev:       p.Dev,
		Cmd:       cmd,
		Backend:   ECBackendACPI,
	}

	// Present-but-blank is separated from absent: a blank key is a mistake worth
	// surfacing, an absent one just gets the chassis as it was built.
	raw, named := p.extra["EC_BACKEND"]
	switch {
	case !named:
	case raw == string(ECBackendACPI):
		conf.Backend = ECBackendACPI
	case raw == string(ECBackendECSys):
		conf.Backend = ECBackendECSys
	default:
		return ECConf{}, fmt.Errorf("%w: %q", ErrECBackendUnknown, raw)
	}

	if conf.Backend != ECBackendECSys {
		return conf, nil
	}
	node, ok := p.extra["EC_IO"]
	if !ok || node == "" {
		return ECConf{}, fmt.Errorf("%w: expected %s/ec<N>/io", ErrECIONodeRequired, ProdECSysRoot)
	}
	if !isKernelECNode(node) {
		return ECConf{}, fmt.Errorf("%w: %q is not %s/ec<N>/io", ErrECIOInvalid, node, ProdECSysRoot)
	}
	conf.IO = node
	return conf, nil
}

// isKernelECNode accepts one shape: a prefix test would take "../.." through.
func isKernelECNode(path string) bool {
	rest, ok := strings.CutPrefix(path, ProdECSysRoot+"/ec")
	if !ok {
		return false
	}
	index, tail, ok := strings.Cut(rest, "/")
	if !ok || tail != "io" || index == "" {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// NeedsCandidates reports whether ValidatePairing consults the candidate list,
// so the chassis's own pairing -- which never does -- adds no second dependency.
func (c ECConf) NeedsCandidates() bool {
	return !strings.HasPrefix(c.FANDevice, c.ECRoot+".")
}

// ValidatePairing refuses two ACPI keys naming different controllers: a register
// read lands on ECRoot and a duty is programmed on FAN_DEVICE, so a cross-EC
// slip reads one machine's fan and writes another's, nothing failing in between.
// A device under its own root is provably one controller; anything else needs the
// candidate list, which exists because machines exist whose device is not under
// EC0. An empty list is not approval.
func (c ECConf) ValidatePairing(candidates []FanCandidate) error {
	if !c.NeedsCandidates() {
		return nil
	}
	for _, cand := range candidates {
		if cand.ECRoot == c.ECRoot && cand.FANDevice == c.FANDevice {
			return nil
		}
	}
	return fmt.Errorf("%w: EC_ROOT=%s and FAN_DEVICE=%s; either correct the pairing or record it in %s",
		ErrECPairMismatch, c.ECRoot, c.FANDevice, ProdFanCandidates)
}

// ParseFanCandidates reads the candidate list. Exactly two columns: a third is a
// typo in one of them, and reading the first two would discard the wrong one.
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
