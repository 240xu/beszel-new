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

// ProdECSysRoot is where the kernel publishes its EC handles. ec_sys is a
// GPL-2.0 kernel driver; using its interface from an MIT agent is the same
// position as reading a procfs file, because nothing of it is linked in.
const ProdECSysRoot = "/sys/kernel/debug/ec"

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

// ECBackend names the mechanism that reaches one EC byte. It is a closed set:
// an unknown value is refused rather than defaulted, because a default here
// decides which physical controller the agent reads.
type ECBackend string

const (
	// ECBackendACPI evaluates the chassis's own EC88 method, which is what this
	// fork has always done and the only path its behaviour has been observed on.
	ECBackendACPI ECBackend = "acpi"
	// ECBackendECSys reads the kernel's ec_sys handle directly. It is the portable
	// spelling of the same register, and it is read-only here on purpose.
	ECBackendECSys ECBackend = "ec_sys"
)

// Why an EC configuration was refused. Named so a caller can switch on them
// rather than match English, and kept distinct so no two refusals collapse into
// one branch.
var (
	ErrECBackendUnknown = errors.New("EC_BACKEND names no known EC backend")
	ErrECIONodeRequired = errors.New("EC_BACKEND=ec_sys needs EC_IO naming the kernel EC node")
	ErrECIOInvalid      = errors.New("EC_IO is not a kernel EC node")
	ErrECPairMismatch   = errors.New("EC_ROOT and FAN_DEVICE do not name one controller")
)

// ECConf is the machine's EC wiring in the form the fan code consumes it: the
// four keys the host loader already required, turned into values, plus the
// optional backend selection. Before this existed all four were parsed,
// validated, covered by tests and then discarded, so editing one changed nothing
// the agent did.
type ECConf struct {
	// ECRoot prefixes the EC register access; FANDevice prefixes the fan methods.
	// They stay independent keys because real machines differ -- see the host's own
	// warning in the platform file.
	ECRoot    string
	FANDevice string
	// Dev is the node an ACPI expression is written to.
	Dev string
	// Cmd is the command byte every EC register access carries.
	Cmd uint64
	// Backend selects how an EC byte is reached. IO is the ec_sys node, empty on
	// the ACPI backend, which addresses its object through ECRoot instead.
	Backend ECBackend
	IO      string
}

// EC derives the wiring and refuses the configurations that cannot be right. It
// validates only the keys this layer owns: ParsePlatformConf stays as strict as
// it was, so the hub's thermal read keeps working on a file whose EC half is
// unusable -- and that is the half nothing reads but the fan.
func (p *PlatformConf) EC() (ECConf, error) {
	cmd, err := strconv.ParseUint(strings.TrimPrefix(p.ECCmd, "0x"), 16, 64)
	if err != nil {
		// Unreachable through ParsePlatformConf, which already admits only a
		// 0x-prefixed hex string. A hand-built PlatformConf is the only caller
		// that can arrive here, and guessing a command byte is the one thing an
		// EC write must never do.
		return ECConf{}, fmt.Errorf("EC_CMD=%q is not hexadecimal: %w", p.ECCmd, err)
	}
	conf := ECConf{
		ECRoot:    p.ECRoot,
		FANDevice: p.FANDevice,
		Dev:       p.Dev,
		Cmd:       cmd,
		Backend:   ECBackendACPI,
	}

	// Present-but-blank is separated from absent on purpose: an operator who wrote
	// the key and left it empty made a mistake worth surfacing, while an operator
	// who never wrote it gets the chassis as it was built.
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

// isKernelECNode accepts exactly one shape. A prefix test would take "../.." and
// an arbitrary path through the door, and the file behind this key addresses a
// physical controller.
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

// NeedsCandidates reports whether ValidatePairing will consult the candidate
// list, so a caller can skip a file read the chassis's own pairing never needs.
// Keeping the shipped configuration free of that dependency is what lets the
// guardrail exist without making production depend on a second file.
func (c ECConf) NeedsCandidates() bool {
	return !strings.HasPrefix(c.FANDevice, c.ECRoot+".")
}

// ValidatePairing refuses a configuration whose two ACPI keys name different
// controllers. EC_ROOT is where a register read lands and FAN_DEVICE is where a
// duty is programmed, so a cross-EC slip makes the agent read one machine's fan
// state and write another's, with nothing failing in between.
//
// A fan device under its own root is provably one controller and is accepted
// without consulting anything. Anything else is legitimate only if the host's
// candidate list records it -- that list exists precisely because machines exist
// whose fan device is not under EC0. An empty list is not approval.
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
