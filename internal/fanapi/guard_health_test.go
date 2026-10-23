//go:build testing

package fanapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The floor acts on a single unreadable temperature, so "both reads failed" is
// not what failsafe means. Health must follow what the guard did.
func TestHealthReportsFailsafeWhileTheFloorHoldsOnAnUnreadableTemperature(t *testing.T) {
	f := newFixture(t)
	f.pfand = false
	f.g.ArmThermalFloor()

	f.tempErr = errors.New("sensor gone")
	f.g.floorTick()

	h := f.g.Health(true, false)
	assert.True(t, h.Failsafe, "a held emergency duty is failsafe even though the level read worked")
	assert.False(t, h.BreakerOpen)
	assert.True(t, h.LevelReadable)
	assert.False(t, h.TempReadable)
}

// A real reading below the edge clears failsafe without disarming the floor.
func TestHealthClearsFailsafeOnceTheTemperatureIsReadableBelowTheEdge(t *testing.T) {
	f := newFixture(t)
	f.pfand = false
	f.g.ArmThermalFloor()

	f.tempErr = errors.New("sensor gone")
	f.g.floorTick()
	require.True(t, f.g.Health(true, false).Failsafe)

	f.tempErr = nil
	f.temp = 46
	f.g.floorTick()
	assert.False(t, f.g.Health(true, true).Failsafe, "the hold ended when a real reading came back")
	assert.True(t, f.g.ThermalFloorArmed(), "the floor keeps watching")
}

// Disarming the floor must also drop the hold: a disarmed floor is not holding
// anything, and /state would otherwise keep claiming failsafe.
func TestHealthDropsFailsafeWhenTheFloorIsDisarmed(t *testing.T) {
	f := newFixture(t)
	f.pfand = false
	f.g.ArmThermalFloor()
	f.tempErr = errors.New("sensor gone")
	f.g.floorTick()
	require.True(t, f.g.Health(true, false).Failsafe)

	f.g.DisarmThermalFloor()
	assert.False(t, f.g.Health(true, false).Failsafe)
}

// The breaker is a separate latch: it can be open while the floor holds, and the
// panel needs both facts.
func TestHealthReportsTheBreakerIndependentlyOfTheFloor(t *testing.T) {
	f := newFixture(t)
	f.setErr = errors.New("device gone")

	// Drive the breaker open through the public write path first: a successful
	// emergency hold calls record() and resets the breaker, so the order matters.
	for i := 0; i < breakerThreshold; i++ {
		_, _ = f.g.SetManualLevel(70, confirmed)
	}
	require.True(t, f.g.Health(true, true).BreakerOpen)

	// The floor can then hold on its own, and both facts coexist.
	f.tempErr = errors.New("sensor gone")
	f.pfand = false
	f.g.ArmThermalFloor()
	f.g.floorTick()
	h := f.g.Health(true, false)
	assert.True(t, h.BreakerOpen, "the latch survives the floor's read")
}
