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
