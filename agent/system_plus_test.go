//go:build testing

package agent

import (
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/agent/plus"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortCacheTimeMs is a non-default interval, i.e. a request the plus gate
// must reject.
const shortCacheTimeMs = defaultDataCacheTimeMs / 10

// TestUpdatePlusStatsLeavesPlusNilUntilGateHits pins the first half of the
// contract: nothing is cached before the gate has run, so a record must not
// claim a snapshot that does not exist yet.
func TestUpdatePlusStatsLeavesPlusNilUntilGateHits(t *testing.T) {
	agent := &Agent{}
	var stats system.Stats

	agent.updatePlusStats(shortCacheTimeMs, &stats)

	assert.Nil(t, stats.Plus, "plus must stay absent when the gate has never hit")
	assert.Nil(t, agent.systemInfo.Plus, "the cache must stay empty before the gate hits")
}

// TestUpdatePlusStatsGatesOnDefaultIntervalAndBackfillsCache pins both halves at
// once. The hub replaces each record's stats blob wholesale
// (internal/hub/systems/system.go), so a gated-out real-time request must still
// carry the cached snapshot. assert.Same covers the whole contract: without the
// backfill Plus would be nil, and without the gate Collect would run again and
// hand back a different pointer.
func TestUpdatePlusStatsGatesOnDefaultIntervalAndBackfillsCache(t *testing.T) {
	agent := &Agent{}

	var gated system.Stats
	agent.updatePlusStats(defaultDataCacheTimeMs, &gated)
	require.NotNil(t, gated.Plus, "plus must be populated when the gate hits")
	require.NotNil(t, agent.systemInfo.Plus, "the gate must populate the cache")

	var ungated system.Stats
	agent.updatePlusStats(shortCacheTimeMs, &ungated)
	assert.Same(t, agent.systemInfo.Plus, ungated.Plus,
		"a gated-out request must reuse the cached snapshot instead of dropping plus")
	assert.Same(t, gated.Plus, ungated.Plus,
		"the snapshot must not be re-collected outside the default interval")
}

// TestInfoPlusSurvivesCborRoundTrip guards the Info cache slot on the wire: key
// 28 has to decode back, and must not be swallowed by another field's key.
func TestInfoPlusSurvivesCborRoundTrip(t *testing.T) {
	original := system.Info{
		Plus: &plus.Stats{Fan: &plus.FanStats{EC76Pct: 61, RPMEst: 2450, RPMEstFlag: true}},
	}
	b, err := cbor.Marshal(original)
	require.NoError(t, err)

	var decoded system.Info
	require.NoError(t, cbor.Unmarshal(b, &decoded))
	require.NotNil(t, decoded.Plus)
	require.NotNil(t, decoded.Plus.Fan)
	assert.Equal(t, 61, decoded.Plus.Fan.EC76Pct)
	assert.Equal(t, uint16(2450), decoded.Plus.Fan.RPMEst)
	assert.True(t, decoded.Plus.Fan.RPMEstFlag)
}
