package plus

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// validLevels is the duty ladder as the panel is handed it: the leading field
// of each entry in the firmware's own _FPS table, minus zero, which _FSL
// forwards to an EC fan-duty write with no case for it. It is no longer the
// gate -- a write is admitted by the intersection of the profile and the table
// -- but it is what the panel's mirror is parsed out of, so the chain stays
// fixture -> here -> the TypeScript copy.
var validLevels = map[int]bool{
	25: true, 30: true, 40: true, 50: true, 60: true,
	70: true, 80: true, 85: true, 90: true, 95: true, 100: true,
}

// DutySet is a resolved whitelist; the zero value admits nothing.
type DutySet struct {
	duties []int
}

// DutySetFrom builds a validated duty set from explicit duties. It is used by
// tests and by callers that already hold a resolved list; production resolution
// stays in ResolveDutySet.
func DutySetFrom(duties []int) DutySet {
	return dutySetFrom(duties)
}

// dutySetFrom sorts and deduplicates, so the two views of a set cannot drift.
func dutySetFrom(duties []int) DutySet {
	sorted := append([]int(nil), duties...)
	sort.Ints(sorted)
	uniq := make([]int, 0, len(sorted))
	for i, d := range sorted {
		if i == 0 || d != sorted[i-1] {
			uniq = append(uniq, d)
		}
	}
	return DutySet{duties: uniq}
}

// Has reports whether the duty is in the set.
func (d DutySet) Has(level int) bool {
	i := sort.SearchInts(d.duties, level)
	return i < len(d.duties) && d.duties[i] == level
}

// String renders the set, saying "none resolved" rather than nothing: an empty
// list reads like a decision, which an unresolved set is not.
func (d DutySet) String() string {
	if len(d.duties) == 0 {
		return "none resolved"
	}
	parts := make([]string, len(d.duties))
	for i, v := range d.duties {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// ResolveDutySet intersects what this machine is calibrated for with what its
// firmware publishes. Both halves are required: a profile alone admits a duty
// the firmware never offered, and the table alone admits whatever it enumerates.
func ResolveDutySet() (DutySet, error) {
	return resolveDutySet(prodSource())
}

func resolveDutySet(s Source) (DutySet, error) {
	profile, err := s.Profile()
	if err != nil {
		return DutySet{}, err
	}
	static := profile.Fan.StaticDuties
	if len(static) == 0 {
		return DutySet{}, fmt.Errorf("%w: the profile for this machine names no static duty", ErrDutySetUnknown)
	}
	steps := collectFanSteps(s.Sysfs)
	if len(steps) == 0 {
		return DutySet{}, fmt.Errorf("%w: no fan table under %s, so the firmware half is unknown",
			ErrDutySetUnknown, s.Sysfs)
	}
	published := make(map[int]bool, len(steps))
	for _, st := range steps {
		published[st.Percent] = true
	}
	var both []int
	for _, d := range static {
		if published[d] {
			both = append(both, d)
		}
	}
	return dutySetFrom(both), nil
}

// ValidateDuty rejects a duty outside the set; ValidateLevel is the same gate
// under the name the rest of the tree already calls.
func ValidateDuty(level int, set DutySet) error {
	if !set.Has(level) {
		return fmt.Errorf("invalid fan level %d; allowed: %s", level, set)
	}
	return nil
}

// ValidateLevel admits a duty only against the intersection this machine
// resolved, reading the machine per call rather than caching: a cache answers
// for a machine that has since been re-profiled.
func ValidateLevel(level int) error {
	set, err := ResolveDutySet()
	if err != nil {
		return fmt.Errorf("%s: %w", err, ErrDutySetUnknown)
	}
	return ValidateDuty(level, set)
}

// LevelLabel renders a level for display. A zero that arrives here was read back
// from the hardware rather than chosen, and it is reported as the duty it is:
// naming it a mode would claim a handover that no write on this stack performs.
func LevelLabel(level int) string {
	return strconv.Itoa(level) + "%"
}
