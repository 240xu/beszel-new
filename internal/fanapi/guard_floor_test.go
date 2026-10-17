//go:build testing

package fanapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Manual mode takes pfand's control loop away, and nothing else in the process polls the
// package temperature, so an armed floor is the only thing left that can put the fan on
// the curve's emergency duty. These pin exactly what that floor may do: write past the
// edge, sit quiet below it, and never hand the fan back to its curve.

func TestArmingTheThermalFloorSchedulesItsFirstLook(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	require.NotEmpty(t, f.waits, "an armed floor that scheduled nothing would watch nothing")
	assert.Equal(t, thermalFloorInterval, f.waits[len(f.waits)-1])
}

func TestArmingTheThermalFloorTwiceDoesNotDoubleUp(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	before := len(f.waits)
	f.g.ArmThermalFloor()
	assert.Len(t, f.waits, before,
		"two live timers would write the same duty twice on every tick")
}

func TestTheThermalFloorHoldsTheEmergencyDutyPastTheEdge(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.temp = 85 // EMERG is 80 on the golden curve
	writesBefore := len(f.writeCalls)
	f.g.floorTick()
	c := mustCurve(t)
	require.Greater(t, len(f.writeCalls), writesBefore,
		"past the edge the fan has to be written, or manual mode is an oven")
	assert.Equal(t, c.E, f.writeCalls[len(f.writeCalls)-1],
		"the duty written is the curve's own emergency duty, not whatever came before")
}

func TestTheThermalFloorIsAuditedAsHeld(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.temp = 85
	f.g.floorTick()
	c := mustCurve(t)
	last := f.lastEvent()
	assert.Equal(t, OpThermalFloor, last.Op)
	assert.Equal(t, OutcomeHeld, last.Outcome,
		"a duty was written and proven, so refused would be the lie this package just removed")
	assert.Equal(t, c.E, last.Level, "the trail must name the duty now driving the fan")
}

func TestTheThermalFloorWritesNothingWhileCool(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.temp = 50 // well under EMERG
	writesBefore := len(f.writeCalls)
	f.g.floorTick()
	assert.Len(t, f.writeCalls, writesBefore,
		"a floor that cooled the fan would be fighting the duty the operator chose")
}

func TestTheThermalFloorHoldsWhenTheTemperatureCannotBeRead(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.tempErr = errors.New("sensor gone")
	writesBefore := len(f.writeCalls)
	f.g.floorTick()
	c := mustCurve(t)
	require.Greater(t, len(f.writeCalls), writesBefore,
		"an unreadable sensor is not proof the edge is clear, and pfand holds full speed on that same reading")
	assert.Equal(t, c.E, f.writeCalls[len(f.writeCalls)-1])
}

func TestTheThermalFloorWritesNothingWhenTheCurveCannotBeRead(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.curveErr = errors.New("curve gone")
	writesBefore := len(f.writeCalls)
	f.g.floorTick()
	assert.Len(t, f.writeCalls, writesBefore,
		"no curve means the emergency duty is unknown, so there is no duty it can justify writing")
}

func TestTheThermalFloorStandsDownWhenTheDaemonIsBack(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.pfand = true
	f.temp = 85
	writesBefore := len(f.writeCalls)
	waitsBefore := len(f.waits)
	f.g.floorTick()
	assert.Len(t, f.writeCalls, writesBefore,
		"the daemon owns the fan again, and two writers on one fan is the hazard this guard exists to prevent")
	assert.Len(t, f.waits, waitsBefore,
		"the floor must stand itself down rather than check against the daemon for ever")
	// A wait not rescheduled says only that this tick stopped, not that the floor
	// stopped. The state does, and leaving it armed means every later tick goes on
	// reading a fan the daemon already owns.
	assert.False(t, f.g.ThermalFloorArmed(),
		"the floor must stand itself down rather than check against the daemon for ever)")
}

func TestTheThermalFloorNeverSchedulesARegress(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.temp = 85
	f.g.floorTick()
	assert.True(t, f.g.PendingRegressAt().IsZero(),
		"manual means manual: the floor is a safety net, not a timer that hands the fan back")
}

func TestDisarmingTheThermalFloorStopsIt(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	f.g.DisarmThermalFloor()
	f.temp = 85
	writesBefore := len(f.writeCalls)
	waitsBefore := len(f.waits)
	f.g.floorTick()
	assert.Len(t, f.writeCalls, writesBefore, "a disarmed floor must not write")
	assert.Len(t, f.waits, waitsBefore, "a disarmed floor must not put itself back on the schedule")
}

func TestClosingTheGuardTakesTheFloorWithIt(t *testing.T) {
	f := newFixture(t)
	f.g.ArmThermalFloor()
	require.True(t, f.g.ThermalFloorArmed(), "precondition: the floor must be live before Close")
	f.g.Close()
	assert.False(t, f.g.ThermalFloorArmed(),
		"the floor must not outlive the guard that was watching the fan")
	f.temp = 85
	writesBefore := len(f.writeCalls)
	waitsBefore := len(f.waits)
	f.g.floorTick()
	assert.Len(t, f.writeCalls, writesBefore,
		"after Close no tick may write the emergency duty again")
	assert.Len(t, f.waits, waitsBefore,
		"after Close no tick may put the floor back on the schedule")
}
