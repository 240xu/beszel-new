package plus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The catalogue is a file the operator drops beside the platform file that
// already names this machine's EC wiring, so a machine added later describes
// itself. Nothing here falls back to a default: a default is another machine's
// constant in a new name.
const ProdProfiles = "/etc/pfan/profiles.json"

const (
	prodDMIRoot   = "/sys/class/dmi/id"
	prodHwmonRoot = "/sys/class/hwmon"
	prodSysfsRoot = "/sys"

	profilesEnv = "PFAN_PROFILES"
	dmiRootEnv  = "PFAN_DMI_ROOT"
	hwmonEnv    = "PFAN_HWMON_ROOT"
	sysfsEnv    = "PFAN_SYSFS"

	dmiSysVendor   = "sys_vendor"
	dmiProductName = "product_name"
	dmiProductVer  = "product_version"
)

var (
	// ErrPlatformUnknown says no profile claimed this machine. It is a refusal
	// and not an invitation to fall back: the fallback would be another
	// machine's numbers, which is the mistake the profile exists to remove.
	ErrPlatformUnknown = errors.New("no fan profile claims this machine")
	// ErrDutySetUnknown says one of the two halves of the duty intersection is
	// missing. A half-known whitelist is not a short whitelist; admitting on
	// what happens to be known is how a machine ends up running another one's.
	ErrDutySetUnknown = errors.New("this machine's duty whitelist is unresolved")
	// ErrThermalCeilingUnknown says no hwmon sensor could be selected by the
	// label the profile names, so the curve has no ceiling to be bounded by.
	ErrThermalCeilingUnknown = errors.New("this machine's thermal ceiling is unresolved")
)

// dmiKeys is the closed set of identity fields a profile may name. Closing it
// turns a misspelling into a refusal rather than a key that compares nothing.
var dmiKeys = []string{dmiSysVendor, dmiProductName, dmiProductVer}

// Source names the four places this machine describes itself in. It is a value
// so a refusal can be re-derived from four named paths in a report.
type Source struct {
	Profiles string
	DMI      string
	Hwmon    string
	Sysfs    string
}

func prodSource() Source {
	return Source{
		Profiles: envOr(profilesEnv, ProdProfiles),
		DMI:      envOr(dmiRootEnv, prodDMIRoot),
		Hwmon:    envOr(hwmonEnv, prodHwmonRoot),
		Sysfs:    envOr(sysfsEnv, prodSysfsRoot),
	}
}

// Identity is the DMI triple a machine publishes. An unpublished field is
// absent rather than empty: those are different facts.
type Identity map[string]string

// FanTuning is what the firmware cannot be asked for.
type FanTuning struct {
	// StaticDuties are the duties this machine has actually been calibrated
	// for. They are half of the whitelist, never all of it: the other half is
	// what the firmware publishes.
	StaticDuties []int `json:"static_duties"`
	// MinDuty and MaxDuty bound the range the hardware accepts. They do not
	// filter the whitelist; they constrain the catalogue, so that a static
	// duty outside them is a mistyped file rather than a discovered limit.
	MinDuty int `json:"min_duty"`
	MaxDuty int `json:"max_duty"`
}

// Thermal names where this machine's thermal ceiling is read from.
type Thermal struct {
	// MaxTempC is what the profile believes the ceiling is. When it is stated
	// the sensor has to agree with it, so a profile left behind by a board swap
	// is refused instead of quietly bounding the curve by the old number.
	MaxTempC int `json:"max_temp_c"`
	// MaxTempLabel is the hwmon sensor label to read. It selects, so a machine
	// whose sensors are enumerated in another order still resolves the same
	// ceiling; without it there is nothing to select and the curve is refused.
	MaxTempLabel string `json:"max_temp_label"`
}

// Profile is one machine's calibration.
type Profile struct {
	Match   map[string]string `json:"match"`
	Fan     FanTuning         `json:"fan"`
	Thermal Thermal           `json:"thermal"`
}

// ProfileSet is a parsed catalogue. Order is the operator's: the first entry
// whose named dimensions all agree wins.
//
// SchemaVersion is additive and optional: a file written before the field
// existed decodes as 0 and keeps working. A file that states a version this
// build does not know is refused rather than parsed, because a future shape
// could otherwise be read as if it were this one.
type ProfileSet struct {
	SchemaVersion int       `json:"schema_version,omitempty"`
	Profiles      []Profile `json:"profiles"`
}

// profilesSchemaVersion is the only schema this build understands. Version 0 is
// "unstated", which is every file written before the field existed.
const profilesSchemaVersion = 1

func readIdentity(root string) Identity {
	var id Identity
	for _, key := range dmiKeys {
		v, err := readTrimmed(filepath.Join(root, key))
		if err != nil || strings.TrimSpace(v) == "" {
			continue
		}
		if id == nil {
			id = Identity{}
		}
		id[key] = strings.TrimSpace(v)
	}
	return id
}

// knownDMIKey reports whether key is one a profile is allowed to name.
func knownDMIKey(key string) bool {
	for _, k := range dmiKeys {
		if k == key {
			return true
		}
	}
	return false
}

// matches reports whether every dimension this profile names agrees with the
// machine. An omitted dimension is not a condition.
func (p Profile) matches(got Identity) (bool, string) {
	if len(p.Match) == 0 {
		return false, "names no identity field"
	}
	for key := range p.Match {
		if !knownDMIKey(key) {
			return false, fmt.Sprintf("names %q, which is not an identity field this fork reads", key)
		}
	}
	for _, key := range dmiKeys {
		want, ok := p.Match[key]
		if !ok {
			continue
		}
		if strings.TrimSpace(want) == "" {
			return false, fmt.Sprintf("names %s as empty, which is every machine and no machine", key)
		}
		have, published := got[key]
		if !published {
			return false, fmt.Sprintf("names %s, which this machine does not publish", key)
		}
		if have != strings.TrimSpace(want) {
			return false, fmt.Sprintf("%s is %q, not %q", key, have, want)
		}
	}
	return true, ""
}

// describeIdentity renders the machine's own words for a refusal message.
func describeIdentity(id Identity) string {
	var parts []string
	for _, key := range dmiKeys {
		if v, ok := id[key]; ok {
			parts = append(parts, key+"="+v)
		}
	}
	if len(parts) == 0 {
		return "no identity field this fork reads"
	}
	return strings.Join(parts, " ")
}

// Match returns the first profile whose every named dimension agrees.
func (s ProfileSet) Match(id Identity) (*Profile, error) {
	for i := range s.Profiles {
		if ok, _ := s.Profiles[i].matches(id); ok {
			return &s.Profiles[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %d profiles, none matching %s", ErrPlatformUnknown, len(s.Profiles), describeIdentity(id))
}

// loadProfiles reads the catalogue, refusing an entry rather than carrying on.
func loadProfiles(path string) (ProfileSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ProfileSet{}, fmt.Errorf("%w: %v", ErrPlatformUnknown, err)
	}
	var set ProfileSet
	if err := strictDecode(raw, &set); err != nil {
		return ProfileSet{}, fmt.Errorf("%w: %s: %v", ErrPlatformUnknown, filepath.Base(path), err)
	}
	if set.SchemaVersion != 0 && set.SchemaVersion != profilesSchemaVersion {
		return ProfileSet{}, fmt.Errorf("%w: %s: schema_version %d is newer than this build understands (%d)",
			ErrPlatformUnknown, filepath.Base(path), set.SchemaVersion, profilesSchemaVersion)
	}
	for i := range set.Profiles {
		if err := checkProfile(&set.Profiles[i], i); err != nil {
			return ProfileSet{}, fmt.Errorf("%w: %s: %v", ErrPlatformUnknown, filepath.Base(path), err)
		}
	}
	return set, nil
}

// strictDecode refuses any field the structs do not declare, so a typo cannot
// leave the profile looking complete and missing the value.
func strictDecode(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// checkProfile refuses an entry that could only mislead at use time.
func checkProfile(p *Profile, index int) error {
	for key := range p.Match {
		if !knownDMIKey(key) {
			return fmt.Errorf("profile %d names %q, which is not an identity field this fork reads", index, key)
		}
	}
	if len(p.Match) == 0 {
		return fmt.Errorf("profile %d names no identity field, so it would claim every machine", index)
	}
	if len(p.Fan.StaticDuties) == 0 {
		return fmt.Errorf("profile %d names no static duty", index)
	}
	if p.Fan.MinDuty <= 0 || p.Fan.MaxDuty < p.Fan.MinDuty {
		return fmt.Errorf("profile %d declares the duty range [%d,%d], which is empty", index, p.Fan.MinDuty, p.Fan.MaxDuty)
	}
	seen := make(map[int]bool, len(p.Fan.StaticDuties))
	for _, d := range p.Fan.StaticDuties {
		if d < p.Fan.MinDuty || d > p.Fan.MaxDuty {
			return fmt.Errorf("profile %d lists static duty %d, outside the range [%d,%d] it declares",
				index, d, p.Fan.MinDuty, p.Fan.MaxDuty)
		}
		if seen[d] {
			return fmt.Errorf("profile %d lists static duty %d twice", index, d)
		}
		seen[d] = true
	}
	return nil
}

// Profile returns the entry claiming this machine, or a refusal naming it.
func (s Source) Profile() (*Profile, error) {
	set, err := loadProfiles(s.Profiles)
	if err != nil {
		return nil, err
	}
	return set.Match(readIdentity(s.DMI))
}

// sensorMax keeps millidegrees so a message can name the number the operator
// reads off the machine.
type sensorMax struct {
	label string
	milli uint64
}

// hwmonMaxima collects the maximum published by every sensor whose label is
// exactly want. Selection is by label because the glob is lexical: a disk's
// "Composite" alarm is read before the coretemp ceiling, and taking the first
// hit bounds every curve by the disk.
func hwmonMaxima(root, want string) ([]sensorMax, error) {
	devices, err := filepath.Glob(filepath.Join(root, "hwmon*"))
	if err != nil {
		return nil, err
	}
	var found []sensorMax
	for _, dev := range devices {
		labels, err := filepath.Glob(filepath.Join(dev, "temp*_label"))
		if err != nil {
			continue
		}
		for _, labelFile := range labels {
			raw, err := readTrimmed(labelFile)
			if err != nil || strings.TrimSpace(raw) != want {
				continue
			}
			base := strings.TrimSuffix(labelFile, "_label")
			milli, err := readUint(base + "_max")
			if err != nil || milli == 0 {
				continue
			}
			found = append(found, sensorMax{label: want, milli: milli})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].milli < found[j].milli })
	return found, nil
}

// ThermalCeiling resolves the ceiling from the sensor the profile names, and
// refuses rather than assuming one: a guess bounds a curve by a guess.
func (s Source) ThermalCeiling() (int, error) {
	profile, err := s.Profile()
	if err != nil {
		return 0, err
	}
	want := strings.TrimSpace(profile.Thermal.MaxTempLabel)
	if want == "" {
		return 0, fmt.Errorf("%w: the profile for this machine names no sensor label, so none can be selected",
			ErrThermalCeilingUnknown)
	}
	found, err := hwmonMaxima(s.Hwmon, want)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrThermalCeilingUnknown, err)
	}
	if len(found) == 0 {
		return 0, fmt.Errorf("%w: no hwmon sensor under %s is labelled %q, so no ceiling was read",
			ErrThermalCeilingUnknown, s.Hwmon, want)
	}
	// Two sensors carrying one label is a multi-socket board, not a licence to
	// pick the friendlier one. They have to agree, or the ceiling is whichever
	// the enumeration happened to reach first.
	if first := found[0].milli; found[len(found)-1].milli != first {
		return 0, fmt.Errorf("%w: %d sensors are labelled %q and publish %d..%d millidegrees, so there is no single ceiling",
			ErrThermalCeilingUnknown, len(found), want, first, found[len(found)-1].milli)
	}
	milli := found[0].milli
	// Floored, not rounded: a sensor that trips at 82.85C is a ceiling of 82,
	// and rounding would let a curve name an 83 the machine has already passed.
	celsius := int(milli / 1000)
	if stated := profile.Thermal.MaxTempC; stated > 0 && stated != celsius {
		return 0, fmt.Errorf("%w: the profile states %dC but the sensor labelled %q publishes %d.%03dC",
			ErrThermalCeilingUnknown, stated, want, milli/1000, milli%1000)
	}
	return celsius, nil
}
