package plus

import (
	"fmt"
	"strconv"
)

// validLevels is the closed duty whitelist: the leading field of each entry in
// the firmware's own _FPS table. Zero is deliberately absent even though that
// table publishes a 0% entry, because _FSL forwards its argument to an EC fan-duty
// write with no case for zero. A zero duty is therefore a request for zero
// percent, and whether the fan keeps turning on that byte is decided by an EC
// curve setting this agent can neither read nor set.
var validLevels = map[int]bool{
	25: true, 30: true, 40: true, 50: true, 60: true,
	70: true, 80: true, 85: true, 90: true, 95: true, 100: true,
}

// ValidateLevel rejects any duty outside that whitelist.
func ValidateLevel(level int) error {
	if !validLevels[level] {
		return fmt.Errorf(
			"invalid fan level %d; allowed: 25,30,40,50,60,70,80,85,90,95,100", level)
	}
	return nil
}

// LevelLabel renders a level for display. A zero that arrives here was read back
// from the hardware rather than chosen, and it is reported as the duty it is:
// naming it a mode would claim a handover that no write on this stack performs.
func LevelLabel(level int) string {
	return strconv.Itoa(level) + "%"
}
