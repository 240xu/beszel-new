//go:build testing

package plus

import (
	"testing"

	jsonv2 "encoding/json/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C6 regression: PocketBase v0.40.5 stores these structs with encoding/json/v2,
// where omitempty keeps a numeric zero. The contract is "absent means unknown",
// so an unset optional scalar must not reach the wire as 0. These tests marshal
// with v2 on purpose: the v1 assertions elsewhere cannot see this difference.
func TestOptionalScalarsStayAbsentUnderJSONv2(t *testing.T) {
	t.Run("wifi: an empty stat has no keys", func(t *testing.T) {
		b, err := jsonv2.Marshal(&WiFiStats{Iface: "wlp0s20f3"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"iface":"wlp0s20f3"}`, string(b),
			"unread signal/tx/freq/retry must be absent, not four zeros")
	})

	t.Run("wifi: an explicit zero still encodes as zero", func(t *testing.T) {
		zero, zeroF, zeroU := 0, 0.0, uint64(0)
		b, err := jsonv2.Marshal(&WiFiStats{SignalDbm: &zero, TxMBit: &zeroF, Retry: &zeroU})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"signal_dbm":0`, "a real zero is a measurement")
		assert.Contains(t, string(b), `"tx_mbit":0`)
		assert.Contains(t, string(b), `"retry":0`)
	})

	t.Run("netall: an unknown speed and mtu are absent", func(t *testing.T) {
		b, err := jsonv2.Marshal(&NetIfaceStats{OperState: "up"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"rx_bytes":0,"tx_bytes":0,"oper_state":"up"}`, string(b),
			"rx/tx bytes stay mandatory and zero; speed/mtu must not appear")
	})

	t.Run("netall: a real speed encodes", func(t *testing.T) {
		speed := 1000
		b, err := jsonv2.Marshal(&NetIfaceStats{SpeedMBit: &speed})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"speed_mbit":1000`)
	})
}
