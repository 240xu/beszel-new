//go:build testing

package plus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidLevelsAccepted(t *testing.T) {
	for _, lv := range []int{25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100} {
		assert.NoError(t, ValidateLevel(lv), "level %d must be accepted", lv)
	}
}

func TestInvalidLevelsRejected(t *testing.T) {
	for _, lv := range []int{-1, 0, 1, 7, 24, 26, 33, 99, 101, 1000} {
		assert.Error(t, ValidateLevel(lv), "level %d must be rejected", lv)
	}
}

// The whitelist is a closed set, so its members and its non-members are both
// pinned here. Off-by-one neighbours matter most: 24 and 26 sit either side of
// the first real step, and a guard loosened by one would let them through.
func TestWhitelistBoundaries(t *testing.T) {
	accepted := map[int]bool{25: true, 100: true}
	rejected := []int{-1, 0, 1, 24, 26, 99, 101}
	for lv := range accepted {
		assert.NoError(t, ValidateLevel(lv), "%d is a step and must be accepted", lv)
	}
	for _, lv := range rejected {
		assert.Error(t, ValidateLevel(lv), "%d is not a step and must be rejected", lv)
	}
}

// The firmware's _FSL hands its argument to an EC fan-duty write with no case for
// zero, so a zero duty asks the hardware for zero percent. That is a duty, not a
// handover, and offering it as one is how a stopped fan gets written by a timer.
func TestZeroIsRejectedRatherThanCalledAuto(t *testing.T) {
	assert.Error(t, ValidateLevel(0), "0 is a duty of zero percent, not a curve handover")
	assert.NotEqual(t, "auto", LevelLabel(0),
		"a label may not promise a handover the write does not perform")
}

func TestLevelLabelNeverSaysOff(t *testing.T) {
	for _, lv := range []int{0, 25, 50, 100} {
		l := LevelLabel(lv)
		assert.NotContains(t, l, "off")
		assert.NotContains(t, l, "Off")
		assert.NotContains(t, l, "OFF")
		assert.NotContains(t, l, "关")
	}
}
