//go:build testing

package fanapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The emergency duty is written through SetLevel, which refuses anything outside
// the profile ∩ firmware duty set. A curve whose E is accepted here but refused
// there stores an emergency duty that can never land; its failures then latch the
// breaker, so the floor cannot write at all. Reject it at the same boundary as
// every other machine-dependent value.
func TestACurveWhoseEmergencyDutyIsNotWritableIsRefused(t *testing.T) {
	f := newFixture(t)
	// Resolved set excludes 81..99; E=100 is in it, so pick a curve with E=97.
	f.duties = []int{0, 25, 50, 60, 70, 80, 85, 90, 95, 100}
	c := mustCurve(t)
	c.E = 97
	require.NoError(t, c.Validate(), "97 passes the machine-independent validator")

	before := string(f.curveBytes)
	_, err := f.g.WriteCurve(c, confirmed)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCurveRejected, "the operator is told the curve was refused")
	assert.Equal(t, before, string(f.curveBytes), "a refused curve must not reach the disk")
	assert.Empty(t, f.writeCalls, "no device write may happen for a refused curve")
}

// The control: an E inside the resolved set is accepted and written.
func TestACurveWhoseEmergencyDutyIsWritableIsAccepted(t *testing.T) {
	f := newFixture(t)
	c := mustCurve(t)
	c.E = 95
	require.NoError(t, c.Validate())

	before := string(f.curveBytes)
	_, err := f.g.WriteCurve(c, confirmed)
	require.NoError(t, err)
	assert.NotEqual(t, before, string(f.curveBytes), "an accepted curve must reach the disk")
}
