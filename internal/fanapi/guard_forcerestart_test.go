//go:build testing

package fanapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A curve reaches the disk before anything runs it, whichever way the daemon is
// running: up, it holds the configuration it sourced at start; down, nothing reads
// the file at all. Reporting either as in force says the change is cooling the
// machine when it is not, and stopping the daemon first is the recommended
// workflow, so that is the arm most likely to be believed.
func TestACurveIsNeverReportedAsInForceBeforeADaemonHasReadIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pfandUp bool
	}{
		{"daemon stopped", false},
		{"daemon running", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.pfand = tc.pfandUp
			res, err := f.g.WriteCurve(mustCurve(t), confirmed)
			require.NoError(t, err)

			assert.True(t, res.Applied,
				"the write landed and was read back; that is what the guard's own field records")
			d := Describe(res)
			assert.True(t, d.NeedsPfandRestart,
				"no daemon has read this file, so nothing is running the fan on it")
			assert.False(t, d.Applied,
				"a panel told applied renders saved, over a control law the machine is not using")
			assert.Equal(t, StatusPendingRestart, d.Status)
			assert.Equal(t, ActionRestartPfand, d.NextAction,
				"the panel has to be able to act on this, or it is only an alarm")
		})
	}
}

// The counterpart, so the change cannot be read as "applied is now always
// false". A manual duty is written straight to the control device and proved by
// a read-back, so it is in force the moment that read-back agrees -- daemon or
// no daemon, because no daemon is what makes the manual path available at all.
func TestAManualDutyIsInForceWithoutADaemon(t *testing.T) {
	f := newFixture(t)
	f.pfand = false
	res, err := f.g.SetManualLevel(60, confirmed)
	require.NoError(t, err)
	d := Describe(res)
	assert.True(t, d.Applied, "the device reported back the duty that was asked for")
	assert.False(t, d.NeedsPfandRestart, "no curve was written, so there is nothing to restart for")
	assert.Equal(t, StatusApplied, d.Status)
}
