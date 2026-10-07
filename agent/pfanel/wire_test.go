//go:build testing

package pfanel

import (
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The omitempty contract on plus.FanStats, asserted from this package.
//
// These belong next to the collector that feeds FanStats rather than in
// agent/plus/fan_test.go only because that file was out of scope for this round;
// nothing here depends on pfanel's sqlite read, only on the wire encoding of the
// type it produces. Move them if the split is ever revisited.
//
// Each of the three assertions below kills a mutation that the whole suite used to
// survive, because the property they state is invisible to a round-trip check:
//
//   - dropping omitempty from rpm_est, which then emits "rpm_est":0 where the
//     contract says the key is absent;
//   - adding omitempty to sample_ts_s, which drops the one staleness signal
//     whenever it is 0;
//   - encoding a nil pointer as CBOR null instead of omitting the key. The Info
//     cache is CBOR, and the struct carries json tags only, so the omitempty
//     rules there are fxamacker/cbor's fallback to the json tag -- a property no
//     existing test asserted at all.
//
// Absent-versus-null is the whole point of the three-state contract on level, ec76
// and rpm: absent means "the column was NULL, unknown", and null on the wire would
// be a fourth state that no client type in the plan models.

// marshalFanStats encodes a FanStats the way the agent publishes it.
func marshalFanStats(t *testing.T, s *plus.FanStats) map[string]any {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TestRPMEstZeroOmitsTheKeyNotZero is the "no estimate available" state. rpm_est is
// omitempty, so a zero estimate must be an ABSENT key: a consumer reads absence as
// "nothing to show" and a literal 0 as a reading, which would render as "fan
// stopped" -- a claim no reading on this platform can support.
func TestRPMEstZeroOmitsTheKeyNotZero(t *testing.T) {
	fan := &plus.FanStats{RPMEstFlag: true, RPMEst: 0, SampleTsS: 1791337448}
	got := marshalFanStats(t, fan)

	assert.NotContains(t, got, "rpm_est",
		"rpm_est is 0 meaning 'no estimate available', which is omitempty, so the key must be "+
			"absent rather than present-and-zero")
	// The rest of the struct still ships: omitempty is per field, not a whole-struct
	// bail-out, and the sample is only useless to a consumer if its time went too.
	assert.Equal(t, float64(1791337448), got["sample_ts_s"])
	assert.Equal(t, true, got["rpm_est_is_estimate"])

	// A real estimate is present, so absence is meaningful rather than constant.
	withValue := marshalFanStats(t, &plus.FanStats{RPMEstFlag: true, RPMEst: 3200})
	assert.Equal(t, float64(3200), withValue["rpm_est"])
}

// TestSampleTsSIsAlwaysPresent pins the other direction: sample_ts_s carries NO
// omitempty, so the key exists on every sample even at 0. It is the only signal a
// consumer has that the reading is current, and a collector that quietly stopped
// refreshing is indistinguishable from a healthy one without it.
func TestSampleTsSIsAlwaysPresent(t *testing.T) {
	got := marshalFanStats(t, &plus.FanStats{RPMEstFlag: true, SampleTsS: 0})

	assert.Contains(t, got, "sample_ts_s",
		"sample_ts_s has no omitempty and is read with every row, so it must be present even at 0")
	assert.Equal(t, float64(0), got["sample_ts_s"])

	zero, ok := got["sample_ts_s"]
	require.True(t, ok)
	assert.NotNil(t, zero, "a present sample_ts_s must be a number, not null")
}

// TestNilPointersOmitTheirKeyInCbor is the same three-state contract on the CBOR
// path the Info cache actually uses. plus.FanStats carries json tags only, so this
// holds only because fxamacker/cbor falls back to the json tag when no cbor tag is
// present (encode.go, "field omission": If "cbor" key is absent, Marshal uses
// "json" key). Without that fallback every NULL would encode as CBOR null and the
// omitempty contract would exist only on the JSON path.
func TestNilPointersOmitTheirKeyInCbor(t *testing.T) {
	raw, err := cbor.Marshal(&plus.FanStats{
		RPMEstFlag: true,
		SampleTsS:  1791337448,
		// SetLevelPct, EC76Pct and Gov all left nil / zero.
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, cbor.Unmarshal(raw, &got))

	for _, key := range []string{"set_level_pct", "ec76_pct", "gov", "rpm_est"} {
		assert.NotContainsf(t, got, key,
			"%s must be an absent CBOR key for a nil pointer or zero value, not cbor null: "+
				"absent is the wire form of 'the column was NULL', and null is a fourth state "+
				"no client type models", key)
	}
	assert.Equal(t, uint64(1791337448), got["sample_ts_s"],
		"sample_ts_s has no omitempty and must survive the CBOR path")
	assert.Equal(t, true, got["rpm_est_is_estimate"])
}

// TestRealValuesEncodeInCbor is the other half: absence has to be caused by the nil
// pointer, not by the field simply never being written. A pointer to 0 must emit
// the key with the value 0, which is the third state the contract exists to keep.
func TestRealValuesEncodeInCbor(t *testing.T) {
	zero, one := 0, 1
	raw, err := cbor.Marshal(&plus.FanStats{
		SetLevelPct: &zero, // a recorded 0, not a NULL
		EC76Pct:     &one,
		RPMEstFlag:  true,
		RPMEst:      3200,
		SampleTsS:   1791337448,
		Gov:         "curve",
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, cbor.Unmarshal(raw, &got))

	assert.Equal(t, uint64(0), got["set_level_pct"],
		"a non-nil pointer to 0 must be emitted, or 'recorded as 0' collapses into 'unknown'")
	assert.Equal(t, uint64(1), got["ec76_pct"])
	assert.Equal(t, uint64(3200), got["rpm_est"])
	assert.Equal(t, "curve", got["gov"])
}
