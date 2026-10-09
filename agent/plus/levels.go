package plus

import (
	"fmt"
	"strconv"
)

// validLevels is the closed duty whitelist: the leading field of each entry in
// the firmware's own _FPS table, where the 0 entry is the curve handover.
var validLevels = map[int]bool{
	0: true, 25: true, 30: true, 40: true, 50: true, 60: true,
	70: true, 80: true, 85: true, 90: true, 95: true, 100: true,
}

// ValidateLevel rejects any duty outside that whitelist.
func ValidateLevel(level int) error {
	if !validLevels[level] {
		return fmt.Errorf(
			"invalid fan level %d; allowed: 0,25,30,40,50,60,70,80,85,90,95,100", level)
	}
	return nil
}

// LevelLabel renders a level for display; 0 is the firmware curve, never "off".
func LevelLabel(level int) string {
	if level == 0 {
		return "auto"
	}
	return strconv.Itoa(level) + "%"
}
