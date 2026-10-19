//go:build testing

package fanapi

import (
	"errors"
	"testing"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A host without fan control is a permanent fact about the platform, not a
// control path that a cooldown can heal. Charging it to the breaker replaces the
// honest unsupported answer with a breaker_open latch after three tries, and the
// operator is told to wait for a device that will never answer.
func TestAnUnsupportedPlatformIsNotChargedToTheBreaker(t *testing.T) {
	f := newFixture(t)
	f.getErr = plus.ErrFanControlUnsupported

	for i := 0; i < breakerThreshold+2; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
		assert.ErrorIs(t, err, plus.ErrFanControlUnsupported,
			"attempt %d must keep naming the platform, not the breaker", i+1)
		assert.NotErrorIs(t, err, ErrBreakerOpen,
			"a permanent platform fact must not latch a breaker that only a cooldown can clear")
	}

	f.getErr = nil
	res, err := f.g.SetManualLevel(70, confirmed)
	require.NoError(t, err,
		"nothing was wrong with the control path, so the breaker must still be closed")
	assert.True(t, res.Applied)
}

// The counterpart, so the exception cannot be read as "the duty read was
// excused": a read the device actually failed still counts, and still latches.
func TestAFailedDutyReadIsStillChargedToTheBreaker(t *testing.T) {
	f := newFixture(t)
	f.getErr = errors.New("device busy")

	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.SetManualLevel(70, confirmed)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrBreakerOpen, "failure %d is what the count is for", i+1)
	}

	_, err := f.g.SetManualLevel(70, confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen,
		"a control path that will not answer is exactly what the latch is for")
}
