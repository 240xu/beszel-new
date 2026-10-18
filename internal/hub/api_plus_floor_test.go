//go:build testing

package hub_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Manual mode removes the only control loop in the process: pfand is what reads the
// package temperature. These pin that the guard takes a thermal floor over at exactly
// that moment and stands it down when the daemon comes back, so the fan is never left
// unwatched by both.
//
// Each test starts with the daemon running, so the startup arm cannot have fired and
// every assertion is about the mode endpoint. The one startup test says so in its name.

var errFanRigBusy = errors.New("unit busy")

func withDaemonUp(r *fanRig)   { r.pfandUp = true }
func withDaemonDown(r *fanRig) { r.pfandUp = false }

func TestManualModeArmsTheThermalFloorTheStartupWouldNotHave(t *testing.T) {
	h := newFanHarness(t, withDaemonUp)
	require.False(t, h.guard.ThermalFloorArmed(),
		"precondition: the daemon is running, so nothing should be holding a floor")

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.True(t, h.guard.ThermalFloorArmed(),
		"stopping the daemon took the fan's only control loop away, so the guard must take it over")
}

func TestAutoModeStandsTheThermalFloorDown(t *testing.T) {
	h := newFanHarness(t, withDaemonUp)

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, h.guard.ThermalFloorArmed(), "precondition: manual armed the floor")

	rec = h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "auto", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.False(t, h.guard.ThermalFloorArmed(),
		"a floor left armed beside a running daemon is a second writer on one fan")
}

func TestAFailedStopLeavesNoThermalFloor(t *testing.T) {
	h := newFanHarness(t, withDaemonUp, func(r *fanRig) { r.pfandFail = errFanRigBusy })

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	assert.False(t, h.guard.ThermalFloorArmed(),
		"the daemon was never stopped, so a floor would be a second writer on a fan pfand still owns")
}

func TestStartingWithTheDaemonDownArmsTheThermalFloor(t *testing.T) {
	// A restart takes the pending state with it, so the arm cannot be remembered: it
	// has to be rebuilt from the device, and the device saying the daemon is down is
	// the only fact that survives the process that died holding the old state.
	h := newFanHarness(t, withDaemonDown)

	assert.True(t, h.guard.ThermalFloorArmed(),
		"a hub that starts with no daemon must still be watching the fan")
}

// The state read has to see the same floor the writes arm and disarm, so a panel
// is never told one thing while the guard believes another.

func TestStateThermalFloorDisarmedByDefault(t *testing.T) {
	h := newFanHarness(t, withDaemonUp)
	require.False(t, h.guard.ThermalFloorArmed(),
		"precondition: the daemon is running and nothing has armed a floor")

	rec := h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.False(t, decodeBody(t, rec)["thermal_floor_armed"].(bool),
		"with no armed floor, state must not report one")
}

func TestStateThermalFloorArmedAfterManualMode(t *testing.T) {
	h := newFanHarness(t, withDaemonUp)

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, h.guard.ThermalFloorArmed(), "precondition: manual armed the floor")

	rec = h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.True(t, decodeBody(t, rec)["thermal_floor_armed"].(bool),
		"state is the panel-visible side of the same floor the write armed")
}

// A start that fails leaves the daemon down, so the floor that stands in for it
// stays armed. Disarming on the mode switch rather than on the daemon actually
// running would leave the fan watched by nobody.
func TestAFailedStartKeepsTheThermalFloorArmed(t *testing.T) {
	h := newFanHarness(t, withDaemonUp)

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, h.guard.ThermalFloorArmed(), "precondition: manual armed the floor")

	h.rig.pfandFail = errFanRigBusy
	rec = h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "auto", "confirm": true})
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	assert.True(t, h.guard.ThermalFloorArmed(),
		"the daemon never started, so the floor is still the only thing watching the fan")

	rec = h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, decodeBody(t, rec)["thermal_floor_armed"].(bool),
		"state must report the floor the failed start left standing")
}
