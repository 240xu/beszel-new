//go:build testing

package plus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidLevelsAccepted(t *testing.T) {
	for _, lv := range []int{0, 25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100} {
		assert.NoError(t, ValidateLevel(lv), "level %d must be accepted", lv)
	}
}

func TestInvalidLevelsRejected(t *testing.T) {
	for _, lv := range []int{-1, 1, 7, 24, 26, 33, 99, 101, 1000} {
		assert.Error(t, ValidateLevel(lv), "level %d must be rejected", lv)
	}
}

// The whitelist is a closed set, so its members and its non-members are both
// pinned here. Off-by-one neighbours matter most: 24 and 26 sit either side of
// the first real step, and a guard loosened by one would let them through.
func TestWhitelistBoundaries(t *testing.T) {
	accepted := map[int]bool{0: true, 25: true, 100: true}
	rejected := []int{-1, 1, 24, 26, 99, 101}
	for lv := range accepted {
		assert.NoError(t, ValidateLevel(lv), "%d is a step and must be accepted", lv)
	}
	for _, lv := range rejected {
		assert.Error(t, ValidateLevel(lv), "%d is not a step and must be rejected", lv)
	}
}

// EC 0x77 == 0 hands control back to the firmware curve; it does NOT stop the
// fan. Anything treating 0 as "off" risks a stopped fan under load.
func TestZeroIsAutoNotOff(t *testing.T) {
	require.NoError(t, ValidateLevel(0))
	assert.Equal(t, "auto", LevelLabel(0))
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
