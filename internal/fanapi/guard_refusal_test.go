//go:build testing

package fanapi

import (
	"fmt"
	"testing"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The breaker counts failures of the control path. A curve this stack refused is
// not one: nothing was written, so there is no fault to count. Counting it turns
// a correction the operator is entitled to make into a two minute lockout, and
// the operator sees a refusal they cannot act on.
func TestARefusedCurveIsNotChargedToTheBreaker(t *testing.T) {
	f := newFixture(t)
	f.writeErr = fmt.Errorf("refusing to write an invalid curve: %w", plus.ErrCurveInvalid)
	c := mustCurve(t)

	for i := 0; i < breakerThreshold+2; i++ {
		_, err := f.g.WriteCurve(c, confirmed)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrCurveRejected,
			"the operator is told the curve was refused, not that the fan stopped answering")
	}

	f.writeErr = nil
	_, err := f.g.WriteCurve(c, confirmed)
	assert.NotErrorIs(t, err, ErrBreakerOpen,
		"a refusal by the validator is the guard working; the latch is for a device "+
			"that would not take the write")
}

// The counterpart, so the change cannot be read as "the breaker was softened":
// a write the device actually rejects still counts, and still latches.
func TestAFailedWriteIsStillChargedToTheBreaker(t *testing.T) {
	f := newFixture(t)
	f.writeErr = fmt.Errorf("read-only file system")

	for i := 0; i < breakerThreshold; i++ {
		_, err := f.g.WriteCurve(mustCurve(t), confirmed)
		require.Error(t, err)
	}

	_, err := f.g.WriteCurve(mustCurve(t), confirmed)
	assert.ErrorIs(t, err, ErrBreakerOpen,
		"a control path that will not take the write is exactly what the latch is for")
}
