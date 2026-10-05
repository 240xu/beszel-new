//go:build testing

package plus

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectReturnsNonNilWithEmptyRoot(t *testing.T) {
	s, err := Collect(t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, s)
}

func TestCollectOmitsEmptySections(t *testing.T) {
	s, err := Collect(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, s.DPTF)
	assert.Nil(t, s.Cpufreq)
	assert.Nil(t, s.NetAll)
	assert.Nil(t, s.Nvme)
	assert.Nil(t, s.WiFi)
	assert.Nil(t, s.Fan)
	assert.Nil(t, s.FanSteps)
}

func TestStatsUseUnitSuffixes(t *testing.T) {
	s := Stats{
		DPTF:    &DPTFStats{Zones: map[string]float64{"x86_pkg_temp": 43}},
		Cpufreq: &CpufreqStats{FreqMHz: []uint32{2800}},
		WiFi:    &WiFiStats{SignalDbm: -53, TxMBit: 780, FreqMHz: 5180},
	}
	b, err := json.Marshal(s)
	require.NoError(t, err)
	str := string(b)
	for _, want := range []string{`"x86_pkg_temp"`, `"freq_mhz"`, `"signal_dbm"`, `"tx_mbit"`} {
		assert.Contains(t, str, want)
	}
}
