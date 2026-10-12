package plus

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ProdCurveConf is the file the host's curve daemon sources on start.
const ProdCurveConf = "/etc/pfand.conf"

const (
	curveMinDuty  = 25
	curveMaxTrim  = 30
	curveMaxGain  = 10
	curveMinIV    = 2
	curveConfMode = 0o644
	curveMaxIV    = 60
	curveMaxDuty  = 100
)

// curveMaxTempC caps every temperature the curve names. Too high and the emergency
// edge sits above every reachable temperature, leaving the emergency duty and the
// guard's interlock unreachable; too low only refuses the write, which is safe. The
// value is this machine's own thermal ceiling, read from the hwmon sensor the
// matched profile names: sensor order is not a ranking, and the ceiling of whichever
// sensor enumerated first is a disk's alarm threshold presented as a CPU's.
// Unresolved is a refusal, never a default.
func curveMaxTempC() (int, error) {
	return prodSource().ThermalCeiling()
}

// ErrCurveInvalid marks a curve refused on its values. A guard that cannot tell
// that from a failed write charges its breaker for its own carefulness.
var ErrCurveInvalid = errors.New("the curve is not one this host would run")

// curveMaxDead is the widest dead band that still lets the skeleton reach the fan:
// the daemon writes only when the change clears the band. Derived, not stated.
var curveMaxDead = narrowestBandStep()

func narrowestBandStep() int {
	duties := make([]int, 0, len(curveBandDuty))
	for d := range curveBandDuty {
		duties = append(duties, d)
	}
	sort.Ints(duties)
	narrowest := duties[len(duties)-1] - duties[0]
	for i := 1; i < len(duties); i++ {
		if gap := duties[i] - duties[i-1]; gap < narrowest {
			narrowest = gap
		}
	}
	return narrowest
}

// curveBandDuty lists the duties the daemon's trim and hysteresis can address.
// Its floor_of and center_of are keyed on the duty value, not a band index, and
// anything outside this set falls through to the emergency edge: floor_of then
// blocks a downshift until the package is nearly at emergency, and center_of
// measures the trim from the wrong reference. A curve banded on any other duty
// passes every numeric check the host runs and still misbehaves, so bands are
// restricted to this set rather than to the wider write-time duty whitelist.
var curveBandDuty = map[int]bool{
	25: true, 30: true, 40: true, 50: true, 60: true, 70: true,
}

// Curve is the skeleton the daemon evaluates: a band upper bound paired with the
// duty that band drives, then the emergency pair and the debounce parameters.
// Pairing is (T1, L1) through (T5, L5), with (Idle, L0) closing the bottom band.
// Reading an edge as bounding the band below it shifts the skeleton by one duty.
type Curve struct {
	Idle, L0               int
	T1, L1, T2, L2         int
	T3, L3, T4, L4, T5, L5 int
	Emerg, E, EmergRel     int
	Cap, Trim, Gain        int
	Hyst, Dwell, Dead, IV  int
}

type curveField struct {
	key string
	dst *int
}

// curveFields is the emission order, pairing each edge with the duty it bounds.
// Sorting would group L0..L5 ahead of T1..T5: still parseable, and it loses the
// pairing that makes the file readable.
func curveFields(c *Curve) []curveField {
	return []curveField{
		{"IDLE", &c.Idle}, {"L0", &c.L0},
		{"T1", &c.T1}, {"L1", &c.L1},
		{"T2", &c.T2}, {"L2", &c.L2},
		{"T3", &c.T3}, {"L3", &c.L3},
		{"T4", &c.T4}, {"L4", &c.L4},
		{"T5", &c.T5}, {"L5", &c.L5},
		{"EMERG", &c.Emerg}, {"E", &c.E}, {"EMERG_REL", &c.EmergRel},
		{"CAP", &c.Cap}, {"TRIM", &c.Trim}, {"GAIN", &c.Gain},
		{"HYST", &c.Hyst}, {"DWELL", &c.Dwell}, {"DEAD", &c.Dead}, {"IV", &c.IV},
	}
}

// ParseCurve reads the daemon's curve file, deliberately stricter than the panel's
// reader. The daemon sources it with a shell, so a line that is not an assignment is a
// command rather than a syntax error, and an unrecognised key leaves that parameter
// on its compiled-in default with nothing logged.
func ParseCurve(src string) (*Curve, error) {
	c := &Curve{}
	fields := curveFields(c)
	dst := make(map[string]*int, len(fields))
	for _, f := range fields {
		dst[f.key] = f.dst
	}
	seen := make(map[string]bool, len(fields))
	for n, raw := range strings.Split(src, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: %q is not an assignment, and the daemon sources this file",
				n+1, strings.TrimSpace(raw))
		}
		key, val := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		field, ok := dst[key]
		if !ok {
			return nil, fmt.Errorf("line %d: unknown key %q, which would leave that parameter on its compiled-in default",
				n+1, key)
		}
		// Padding is accepted because both host readers accept it; Render is the
		// half that must not emit it, since the daemon sources " L1 = 30" as the
		// command "L1" and leaves L1 unset.
		if !curveDigits(val) {
			return nil, fmt.Errorf("line %d: %s=%q is not a plain unsigned integer", n+1, key, val)
		}
		v, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s=%q is out of range", n+1, key, val)
		}
		// Both host readers assign as they read, so a repeat leaves the last value.
		*field = v
		seen[key] = true
	}
	var missing []string
	for _, f := range fields {
		if !seen[f.key] {
			missing = append(missing, f.key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("curve is missing %d of %d keys: %s",
			len(missing), len(fields), strings.Join(missing, ", "))
	}
	return c, nil
}

func curveDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Validate rejects a curve the daemon would refuse to run on. Its own check is
// narrower, so anything accepted here is also accepted there: the rules below cover
// every case it re-checks.
func (c *Curve) Validate() error {
	// The parser reads unsigned integers, so a negative accepted here is refused
	// on the way to disk, after the breaker was charged for our own refusal.
	for _, f := range curveFields(c) {
		if *f.dst < 0 {
			return fmt.Errorf("%s=%d must not be negative", f.key, *f.dst)
		}
	}
	edges := []struct {
		name string
		val  int
	}{{"IDLE", c.Idle}, {"T1", c.T1}, {"T2", c.T2}, {"T3", c.T3},
		{"T4", c.T4}, {"T5", c.T5}, {"EMERG", c.Emerg}}
	for i := 1; i < len(edges); i++ {
		if edges[i].val <= edges[i-1].val {
			return fmt.Errorf("%s=%d must exceed %s=%d", edges[i].name, edges[i].val, edges[i-1].name, edges[i-1].val)
		}
	}
	// The emergency latch enters at Emerg and releases only below EmergRel; equal
	// or inverted values make it unreachable, or released on the sample that set it.
	if c.EmergRel >= c.Emerg {
		return fmt.Errorf("EMERG_REL=%d must be below EMERG=%d", c.EmergRel, c.Emerg)
	}
	// Above this the emergency branch, and the guard's interlock, are unreachable.
	ceiling, err := curveMaxTempC()
	if err != nil {
		return fmt.Errorf("this curve cannot be bounded: %w", err)
	}
	if c.Emerg > ceiling {
		return fmt.Errorf("EMERG=%d is above %dC, past which this platform throttles "+
			"or shuts down on its own and the curve no longer decides anything",
			c.Emerg, ceiling)
	}
	// 0 is the duty whitelist's handover, but in a curve it is the stop value.
	if c.L0 < curveMinDuty {
		return fmt.Errorf("L0=%d must be at least %d, below which the firmware stops the fan", c.L0, curveMinDuty)
	}
	levels := []struct {
		name string
		val  int
	}{{"L0", c.L0}, {"L1", c.L1}, {"L2", c.L2}, {"L3", c.L3}, {"L4", c.L4}, {"L5", c.L5}}
	for _, l := range levels {
		if l.val < 0 || l.val > c.Cap {
			return fmt.Errorf("%s=%d is outside [0,%d]", l.name, l.val, c.Cap)
		}
		if !curveBandDuty[l.val] {
			return fmt.Errorf("%s=%d is not a duty the daemon's trim table can band on", l.name, l.val)
		}
	}
	// Strictly above, not at: the daemon refuses one that only equals the ceiling.
	if c.E <= c.Cap {
		return fmt.Errorf("E=%d must exceed CAP=%d, or the emergency duty is clamped "+
			"below full speed and the daemon refuses the curve outright", c.E, c.Cap)
	}
	if c.E > curveMaxDuty || c.Cap > curveMaxDuty {
		return fmt.Errorf("E=%d CAP=%d must be within [0,%d], the range the fan control exposes",
			c.E, c.Cap, curveMaxDuty)
	}
	if c.IV < curveMinIV {
		return fmt.Errorf("IV=%d must be at least %ds", c.IV, curveMinIV)
	}
	// The longest stretch the machine can spend on a stale duty past the edge.
	if c.IV > curveMaxIV {
		return fmt.Errorf("IV=%d must be at most %ds, or the emergency edge is polled too "+
			"rarely to catch the crossing", c.IV, curveMaxIV)
	}
	if c.Dead > curveMaxDead {
		return fmt.Errorf("DEAD=%d must be at most %d, or the duty ladder can never be "+
			"written past its own adjacent steps", c.Dead, curveMaxDead)
	}
	// The way back down out of the emergency duty is the test T <= EMERG-HYST, so a
	// hysteresis reaching the edge removes it and the emergency duty becomes the only
	// one the curve can leave. HYST is the one tuning whose overrun cools harder
	// rather than less -- the daemon gates downshifts on it and never upshifts -- so
	// this bounds a curve that stops tracking, not one that can run hot.
	if c.Hyst >= c.Emerg {
		return fmt.Errorf("HYST=%d must be below EMERG=%d, or the emergency duty becomes "+
			"the only duty the curve can ever leave", c.Hyst, c.Emerg)
	}
	if c.Dwell < c.IV {
		return fmt.Errorf("DWELL=%d must be at least IV=%d", c.Dwell, c.IV)
	}
	if c.Trim < 0 || c.Trim > curveMaxTrim {
		return fmt.Errorf("TRIM=%d must be within [0,%d]", c.Trim, curveMaxTrim)
	}
	if c.Gain < 0 || c.Gain > curveMaxGain {
		return fmt.Errorf("GAIN=%d must be within [0,%d]", c.Gain, curveMaxGain)
	}
	return nil
}

// Render writes the curve in the layout the host's writer produces. Comments are
// regenerated rather than preserved: carrying forward whatever text preceded them
// would let an edit smuggle in a line the daemon would source.
func (c *Curve) Render() string {
	var b strings.Builder
	b.WriteString("# pfand 曲线参数 —— 由面板「自定义曲线」按钮写入\n# 分级骨架：温度上限 -> 档位\n\n")
	section := map[string]string{
		"EMERG": "# 应急\n", "CAP": "# 上限与微调\n", "HYST": "# 防抖\n",
	}
	for _, f := range curveFields(c) {
		if s, ok := section[f.key]; ok {
			b.WriteString(s)
		}
		b.WriteString(f.key)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(*f.dst))
		b.WriteByte('\n')
	}
	return b.String()
}

// WriteCurveFile replaces the curve atomically, because the daemon must never
// observe a partial write. An unvalidated curve is refused outright: the daemon's
// own reaction to one it dislikes is to run the fan at full speed.
func WriteCurveFile(path string, c *Curve) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to write an invalid curve: %w: %w", ErrCurveInvalid, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(c.Render()), curveConfMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ValidateBytes is the one gate for a curve on its way to disk, in either shape:
// captured bytes, or rendered. Only parsing stood here before, so a curve could
// clear it and be dropped by the other layer after being charged.
func ValidateBytes(raw []byte) error {
	c, err := ParseCurve(string(raw))
	if err != nil {
		return err
	}
	return c.Validate()
}

// WriteCurveBytesFile replaces the curve file with bytes the caller has already composed,
// keeping the atomic replace of WriteCurveFile.
//
// The guard restores the bytes it read, not a re-render: Render regenerates comments,
// so a restore by rendering would put back a file that is not the one that ran.
func WriteCurveBytesFile(path string, raw []byte) error {
	if err := ValidateBytes(raw); err != nil {
		return fmt.Errorf("refusing to write an invalid curve: %w: %w", ErrCurveInvalid, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, curveConfMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ReadCurveFile reads and parses the curve file at path.
func ReadCurveFile(path string) (*Curve, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCurve(string(raw))
}

// WantFor resolves the duty the daemon would drive at a package temperature. The
// band edges are inclusive, and a span above T5 but below Emerg matches no edge,
// so it falls through to the emergency duty rather than idling at the top band.
// Every branch returns a real duty, never the handover value.
func WantFor(c *Curve, tempC int) int {
	switch {
	case tempC >= c.Emerg:
		return c.E
	case tempC <= c.Idle:
		return c.L0
	case tempC <= c.T1:
		return c.L1
	case tempC <= c.T2:
		return c.L2
	case tempC <= c.T3:
		return c.L3
	case tempC <= c.T4:
		return c.L4
	case tempC <= c.T5:
		return c.L5
	}
	return c.E
}
