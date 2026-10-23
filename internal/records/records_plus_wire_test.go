//go:build testing

package records_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/agent/plus"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/records"
	"github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// The plus wire contract, from the hub's side.
//
// plus data reaches the database through two encodings that share one struct
// field and neither of which the other one covers:
//
//   - agent -> hub is CBOR. internal/hub/systems/system.go's fetchDataViaSSH
//     decodes into a system.CombinedData, so system.Stats.Plus is reached
//     through CBOR's integer keys.
//   - hub -> disk is JSON. createRecords hands the struct straight to
//     PocketBase's JSONField, which persists it through
//     types.JSONRaw.Scan (encoding/json/v2).
//
// A tag that is right for one and wrong for the other is invisible in a
// passing build: fxamacker/cbor falls back to the json tag whenever a cbor tag
// is absent, so deleting the cbor tag does not fail to compile, does not fail
// to decode, and produces a wire that only a byte-level assertion notices. Every
// test below therefore inspects the encoded bytes, never just the struct.
//
// The fourth case is the opposite hazard: a test that only checks "plus is
// there when it should be" also passes over a plus path that never runs. The
// downsampling tests below assert that their own fixtures DO carry plus before
// asserting that the rolled-up record does not, so a fixture that silently
// stopped carrying plus cannot make them pass.
// ---------------------------------------------------------------------------

// plusStatsJSONKey is the JSON name system.Stats.Plus must serialize under.
const plusStatsJSONKey = "plus"

// plusStatsCborKey is the CBOR integer key system.Stats.Plus must serialize
// under, from its `cbor:"41,keyasint,omitempty"` tag.
const plusStatsCborKey = 41

// plusIntPtr returns a pointer to v, for the fields whose pointer type is what
// keeps a real zero distinct from an absent reading.
func plusIntPtr(v int) *int { return &v }

func plusFloatPtr(v float64) *float64 { return &v }

func plusU64Ptr(v uint64) *uint64 { return &v }

// plusFixture builds a plus.Stats with a distinct non-zero value in every
// field, for every section, so a transposition, a truncated width or a dropped
// field shows up as a mismatch instead of as two zeroes that compare equal.
//
// Every omitempty field is deliberately non-zero. A field left at its zero
// value disappears from the encoding, so a fixture full of zeros would let a
// field be dropped by the encoder and still compare equal after a decode.
func plusFixture() *plus.Stats {
	return &plus.Stats{
		DPTF: &plus.DPTFStats{Zones: map[string]float64{
			"B0D4": 41.5, "x86_pkg_temp": 38.25, "B0D1": 33.75,
		}},
		Cpufreq: &plus.CpufreqStats{
			Cores: []plus.CpufreqCore{
				{Idx: 0, FreqMHz: 2194, MinMHz: 800, MaxMHz: 3600},
				{Idx: 1, FreqMHz: 2194, MinMHz: 800, MaxMHz: 3600},
			},
			Governor:     "powersave",
			EPP:          "balance_performance",
			NoTurbo:      plusIntPtr(1),
			HwpBoost:     plusIntPtr(0),
			PstateStatus: "active",
		},
		NetAll: map[string]*plus.NetIfaceStats{
			"eth0": {
				RxBytes: 918273645, TxBytes: 564738291, RxPackets: plusU64Ptr(7012345),
				TxPackets: plusU64Ptr(4890234), OperState: "up", SpeedMBit: plusIntPtr(1000), MTU: plusIntPtr(1500),
			},
			"wlan0": {
				RxBytes: 9182, TxBytes: 5647, RxPackets: plusU64Ptr(71), TxPackets: plusU64Ptr(48),
				OperState: "down", SpeedMBit: plusIntPtr(0), MTU: plusIntPtr(1500),
			},
		},
		Nvme: &plus.NvmeStats{
			Model: "HFM128GD3GX013N-SKhynix", TempC: 31.5, AvailableSparePct: 100,
			PercentageUsedPct: 3, DataUnitsRead: 2440192, DataUnitsWritten: 803115,
			PowerOnHours: 183, PowerCycles: 34, UnsafeShutdowns: 19,
			MediaErrors: 5, ControllerBusyMin: 46, CriticalCompTimeMin: 13,
		},
		WiFi: &plus.WiFiStats{
			SignalDbm: plusIntPtr(-52), TxMBit: plusFloatPtr(144.5), FreqMHz: plusIntPtr(5180),
			Retry: plusU64Ptr(8123), Iface: "wlan0",
		},
		Fan: &plus.FanStats{
			SetLevelPct: plusIntPtr(40), EC76Pct: plusIntPtr(61), RPMEst: 2450,
			RPMEstFlag: true, Gov: "curve", SampleTsS: 1791333981, TempC: 44.5,
		},
		FanSteps: []plus.FanSpeedStep{
			{Percent: 0, RPMEst: 900, NoiseMdb: 22, PowerMw: 1800},
			{Percent: 100, RPMEst: 3100, NoiseMdb: 38, PowerMw: 5200},
		},
	}
}

// plusFixtureRecord is a system.Stats whose non-plus fields are also populated,
// so the round trip exercises a realistic record rather than a plus-only one.
func plusFixtureRecord() system.Stats {
	return system.Stats{
		Cpu: 12.5, Mem: 7.75, MemUsed: 4.25, MemPct: 54.8, MemBuffCache: 3.5,
		DiskTotal: 976.5, DiskUsed: 231.25, DiskPct: 23.7,
		NetworkSent: 12.5, NetworkRecv: 87.5, DiskReadPs: 1024.5, DiskWritePs: 2048.75,
		Temperatures: map[string]float64{"cpu": 44.5, "acpi": 31.0},
		Fans:         map[string]uint16{"fan1": 2450},
		WiFi:         map[string]int8{"wlan0": -52},
		Plus:         plusFixture(),
	}
}

// plusTestHub returns a hub plus one 'up' system, which is what
// CreateLongerRecords requires before it will roll anything up.
func plusTestHub(t *testing.T) (core.App, string) {
	t.Helper()
	hub, err := tests.NewTestHub(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(hub.Cleanup)

	user, err := tests.CreateUser(hub, "plus-wire@example.com", "testtesttest")
	require.NoError(t, err)
	sys, err := tests.CreateRecord(hub, "systems", map[string]any{
		"name": "plus-wire-system", "host": "localhost", "port": "45876",
		"status": "up", "users": []string{user.Id},
	})
	require.NoError(t, err)
	return hub, sys.Id
}

// saveSystemStats persists stats the way internal/hub/systems/system.go's
// createRecords does: the struct is handed to the JSONField unchanged and the
// record is saved without validation. Returns the new record's id.
func saveSystemStats(t *testing.T, hub core.App, systemID string, stats system.Stats) string {
	t.Helper()
	collection, err := hub.FindCachedCollectionByNameOrId("system_stats")
	require.NoError(t, err)
	record := core.NewRecord(collection)
	record.Set("system", systemID)
	record.Set("type", "1m")
	record.Set("stats", stats)
	require.NoError(t, hub.SaveNoValidate(record))
	return record.Id
}

// readStatsJSON returns the stats column exactly as it sits in the table, read
// through the same type and query records.go's AverageSystemStats uses, so the
// bytes asserted on are the ones Task 20's sqlite3 check will read and not a
// round trip through PocketBase's own accessor.
func readStatsJSON(t *testing.T, hub core.App, recordID string) []byte {
	t.Helper()
	var row records.StatsRecord
	require.NoError(t, hub.DB().
		NewQuery("SELECT stats FROM system_stats WHERE id = {:id}").
		Bind(map[string]any{"id": recordID}).
		One(&row))
	require.NotEmpty(t, row.Stats)
	return row.Stats
}

// topLevelKeys parses a stored stats blob into its top level keys.
func topLevelKeys(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top), "stored stats must be a JSON object: %s", raw)
	return top
}

// cborKeys decodes an encoded struct into its raw keys. fxamacker/cbor accepts
// a map with an interface key, which is the only form that can hold both an
// integer key and a text key, so this sees a fallback to the json tag as a text
// key rather than as a decode error.
func cborKeys(t *testing.T, data []byte) map[any]cbor.RawMessage {
	t.Helper()
	var keys map[any]cbor.RawMessage
	require.NoError(t, cbor.Unmarshal(data, &keys))
	return keys
}

// TestPlusReachesTheStoredStatsJSON is the hub's half of the contract: a record
// carrying plus must put a "plus" key into the stats column on disk, with every
// section and every field of the snapshot still readable from it.
//
// The bytes are read straight out of the table rather than from the saved
// record, because the reader that matters is whoever queries the database next.
func TestPlusReachesTheStoredStatsJSON(t *testing.T) {
	hub, systemID := plusTestHub(t)

	id := saveSystemStats(t, hub, systemID, plusFixtureRecord())
	raw := readStatsJSON(t, hub, id)

	plusRaw, ok := topLevelKeys(t, raw)[plusStatsJSONKey]
	require.True(t, ok,
		"a record carrying plus must persist a %q key, got %s", plusStatsJSONKey, raw)
	assert.NotEqual(t, "null", string(plusRaw),
		"the plus key must hold the snapshot, not null")

	var got system.Stats
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NotNil(t, got.Plus, "plus must survive the round trip off disk")
	assert.Equal(t, plusFixture(), got.Plus,
		"every plus section and field must come back off disk unchanged")

	// The nested WiFi section and the record's own WiFi map are different
	// things under different keys, and the frontend reads both.
	assert.Contains(t, topLevelKeys(t, raw), "wf",
		"the record's own WiFi map must still be present alongside plus")
	var plusStats plus.Stats
	require.NoError(t, json.Unmarshal(plusRaw, &plusStats))
	assert.NotNil(t, plusStats.WiFi, "plus carries its own wifi section")
	assert.Equal(t, "wlan0", plusStats.WiFi.Iface)
}

// TestPlusWireKeysAreUnique pins the two names plus claims on the wire, and
// proves no other field can take either of them.
//
// A duplicate is not a compile error in either encoder. JSON drops one of the
// two silently at marshal time; CBOR lets the later field overwrite the earlier
// one in the map. Neither shows up until a consumer reads a field that quietly
// holds someone else's value, so the check has to be the tag table itself.
func TestPlusWireKeysAreUnique(t *testing.T) {
	typ := reflect.TypeOf(system.Stats{})

	// Two passes, in this order on purpose. Collect Plus's keys first and only
	// then compare the rest of the struct against them: a single pass compares
	// against a zero value for every field declared before Plus, which silently
	// turns "no field claims key 0" into "Cpu claims key 0".
	var plusJSONName, plusCborTag string
	var plusField reflect.StructField
	var plusFound bool
	for i := range typ.NumField() {
		if field := typ.Field(i); field.Name == "Plus" {
			plusField, plusFound = field, true
			plusJSONName = strings.Split(field.Tag.Get("json"), ",")[0]
			plusCborTag = strings.Split(field.Tag.Get("cbor"), ",")[0]
			break
		}
	}
	require.True(t, plusFound, "system.Stats must still carry a Plus field")
	require.NotContains(t, plusCborTag, "keyasint",
		"Plus must ask for an integer key; without it the encoder falls back to the json tag")
	plusCborKey, err := strconv.Atoi(plusCborTag)
	require.NoError(t, err, "Plus must declare an integer cbor key, got %q", plusCborTag)

	assert.Equal(t, plusStatsJSONKey, plusJSONName,
		"Plus must serialize under the documented json key")
	assert.Equal(t, plusStatsCborKey, plusCborKey,
		"Plus must serialize under the documented cbor key")

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Name == plusField.Name {
			continue
		}
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonName != "" && jsonName != "-" {
			assert.NotEqual(t, plusJSONName, jsonName,
				"%s already serializes under the plus json key %q; one of the two fields would be dropped",
				field.Name, plusJSONName)
		}
		cborTag := field.Tag.Get("cbor")
		if cborTag == "" || cborTag == "-" {
			continue
		}
		key, err := strconv.Atoi(strings.Split(cborTag, ",")[0])
		if err != nil {
			// A field whose cbor tag is not an integer encodes under a text key,
			// so it cannot collide with an integer key. Nothing to check.
			continue
		}
		assert.NotEqual(t, plusCborKey, key,
			"%s already claims cbor key %d; the later field would overwrite plus on decode",
			field.Name, key)
	}
}

// TestPlusSurvivesCborIngressRoundTrip walks the whole agent -> hub -> disk path
// in the order production performs it: the agent encodes a CombinedData, the
// hub decodes that CBOR into a CombinedData, and createRecords hands the
// decoded Stats to PocketBase.
//
// Both halves are asserted against bytes. The CBOR half proves the section
// arrives under the integer key, and specifically that no text key appears --
// fxamacker/cbor falls back to the json tag when a cbor tag is missing, which
// decodes cleanly into a struct and is therefore invisible without this.
func TestPlusSurvivesCborIngressRoundTrip(t *testing.T) {
	hub, systemID := plusTestHub(t)

	encoded, err := cbor.Marshal(system.CombinedData{Stats: plusFixtureRecord()})
	require.NoError(t, err)

	keys := cborKeys(t, encoded)
	// Unwrap one level: CombinedData.Stats sits under key 0.
	statsBlob, ok := keys[uint64(0)]
	require.True(t, ok, "CombinedData.Stats must be encoded")
	statsKeys := cborKeys(t, statsBlob)
	_, hasPlus := statsKeys[uint64(plusStatsCborKey)]
	assert.True(t, hasPlus,
		"plus must arrive under cbor key %d, got keys %v", plusStatsCborKey, mapKeys(statsKeys))
	_, hasTextPlus := statsKeys[plusStatsJSONKey]
	assert.False(t, hasTextPlus,
		"a text %q key means the cbor tag was dropped and the encoder fell back to the json tag",
		plusStatsJSONKey)

	// Decode the way fetchDataFromSSH does, then persist the decoded value.
	var decoded system.CombinedData
	require.NoError(t, cbor.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.Stats.Plus)

	raw := readStatsJSON(t, hub, saveSystemStats(t, hub, systemID, decoded.Stats))
	_, ok = topLevelKeys(t, raw)[plusStatsJSONKey]
	require.True(t, ok,
		"plus decoded off the wire must still land in the stats column, got %s", raw)

	var got system.Stats
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, plusFixture(), got.Plus,
		"the snapshot must be identical before and after the whole agent -> hub -> disk trip")
}

// TestPlusNilOmitsItsKeyOnBothEncoders covers the negative case on both paths.
//
// A consumer distinguishes "this window has no plus data" from "this window has
// plus data and it is all zero" by the key being absent, so a nil Plus that
// serialized as an explicit null would hand every reader an object where the
// contract promises a missing key. Both encoders get checked because they can
// disagree: a tag option is honoured by one and ignored by the other.
func TestPlusNilOmitsItsKeyOnBothEncoders(t *testing.T) {
	t.Run("json on disk", func(t *testing.T) {
		hub, systemID := plusTestHub(t)
		stats := plusFixtureRecord()
		stats.Plus = nil

		raw := readStatsJSON(t, hub, saveSystemStats(t, hub, systemID, stats))

		_, ok := topLevelKeys(t, raw)[plusStatsJSONKey]
		assert.False(t, ok, "a nil plus must leave no key behind, got %s", raw)
		assert.NotRegexp(t, `"`+plusStatsJSONKey+`"\s*:`, string(raw),
			"the document must not mention plus at all, null included")
	})

	t.Run("cbor", func(t *testing.T) {
		encoded, err := cbor.Marshal(system.Stats{Cpu: 12.5})
		require.NoError(t, err)
		keys := cborKeys(t, encoded)
		_, hasInt := keys[uint64(plusStatsCborKey)]
		_, hasText := keys[plusStatsJSONKey]
		assert.False(t, hasInt,
			"a nil plus must leave cbor key %d out, got keys %v", plusStatsCborKey, mapKeys(keys))
		assert.False(t, hasText, "a nil plus must not fall back to a text key either")
	})

	// A non-nil but entirely empty snapshot is the other "nothing to report"
	// case, and plus.Collect always returns one (agent/plus.Collect builds a
	// Stats and fills sections only where collection succeeded), so this is the
	// state a machine with no collectable plus reports. The stored key is
	// absent here too, because every field inside plus.Stats is omitempty and
	// the section encodes as an empty object.
	//
	// It is locked rather than asserted loosely because both absent states mean
	// the same thing to a reader -- no plus data for this window -- and a
	// future field that stops being omitempty would move the line without
	// anybody deciding whether an empty section should survive to the UI. When
	// that happens, decide here first.
	t.Run("empty snapshot", func(t *testing.T) {
		hub, systemID := plusTestHub(t)
		stats := plusFixtureRecord()
		stats.Plus = &plus.Stats{}

		raw := readStatsJSON(t, hub, saveSystemStats(t, hub, systemID, stats))
		_, ok := topLevelKeys(t, raw)[plusStatsJSONKey]
		assert.False(t, ok,
			"an empty plus section has nothing to show and must not persist a key, got %s", raw)
	})
}

// TestPlusIsDroppedByDownsampling pins the accepted gap.
//
// AverageSystemStatsSlice accumulates field by field and does not mention Plus,
// so no rolled-up record can carry one: 10m, 20m, 120m and 480m all get their
// stats from that function. That is a known and accepted consequence of putting
// plus in the existing stats column instead of a second collection, and this
// test exists so the next reader does not mistake it for an oversight and
// "fix" the upstream file.
//
// It asserts on both the slice and the database-reading wrapper, because the
// wrapper is what production calls and it could start populating the field on
// its own.
func TestPlusIsDroppedByDownsampling(t *testing.T) {
	t.Run("slice", func(t *testing.T) {
		input := []system.Stats{
			{Cpu: 10, Plus: plusFixture()},
			{Cpu: 20, Plus: plusFixture()},
			{Cpu: 30, Plus: plusFixture()},
		}
		for i := range input {
			require.NotNil(t, input[i].Plus, "fixture must actually carry plus")
		}

		result := records.AverageSystemStatsSlice(input)
		assert.Nil(t, result.Plus,
			"downsampling does not accumulate plus; a rolled-up record must not claim one")
		assert.Equal(t, 20.0, result.Cpu,
			"the rest of the average must still be computed")

		// A mixed batch, where some records carry plus and some do not, is what
		// a real rollup window looks like while an agent is still upgrading.
		mixed := records.AverageSystemStatsSlice([]system.Stats{
			{Cpu: 10, Plus: plusFixture()}, {Cpu: 20},
		})
		assert.Nil(t, mixed.Plus, "a partial batch must not leak plus into the average either")
	})

	t.Run("database", func(t *testing.T) {
		hub, systemID := plusTestHub(t)

		var ids records.RecordIds
		for range 3 {
			id := saveSystemStats(t, hub, systemID, plusFixtureRecord())
			raw := readStatsJSON(t, hub, id)
			require.Contains(t, topLevelKeys(t, raw), plusStatsJSONKey,
				"the stored record must carry plus, or this test proves nothing")
			var rec records.RecordIds
			hub.DB().NewQuery("SELECT id FROM system_stats WHERE id={:id}").
				Bind(map[string]any{"id": id}).All(&rec)
			ids = append(ids, rec...)
		}
		require.Len(t, ids, 3)

		averaged := records.NewRecordManager(hub).AverageSystemStats(hub.DB(), ids)
		require.NotNil(t, averaged)
		assert.Nil(t, averaged.Plus,
			"the averaging wrapper reads plus off disk and must not hand it on")
	})
}

// TestPlusAbsentFromRolledUpRecords is the database-level half of the accepted
// gap: whatever the shorter records hold, the longer record the hub writes does
// not hold plus.
//
// Every tier is exercised, and each one first asserts that its own shorter
// records really do carry plus. Without that, "the rolled-up record has no plus"
// would be satisfied by a fixture that had quietly stopped producing any, which
// is the same vacuous pass as a mutation that survives.
func TestPlusAbsentFromRolledUpRecords(t *testing.T) {
	tiers := []struct {
		name    string
		shorter string
		longer  string
		count   int
		age     time.Duration
	}{
		// 1m -> 10m is the hop where a live plus snapshot stops being live.
		{name: "10m", shorter: "1m", longer: "10m", count: 9, age: 2 * time.Minute},
		{name: "120m", shorter: "20m", longer: "120m", count: 6, age: 30 * time.Minute},
		{name: "480m", shorter: "120m", longer: "480m", count: 4, age: 200 * time.Minute},
	}

	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			hub, systemID := plusTestHub(t)

			created := time.Now().UTC().Add(-tier.age)
			for range tier.count {
				rec, err := tests.CreateRecord(hub, "system_stats", map[string]any{
					"system":  systemID,
					"type":    tier.shorter,
					"created": created.Format(types.DefaultDateLayout),
					"stats":   plusFixtureRecord(),
				})
				require.NoError(t, err)
				_, carried := topLevelKeys(t, readStatsJSON(t, hub, rec.Id))[plusStatsJSONKey]
				require.True(t, carried,
					"each seeded %s record must carry plus, or the rolled-up result proves nothing",
					tier.shorter)
			}

			records.NewRecordManager(hub).CreateLongerRecords()

			longer, err := hub.FindAllRecords("system_stats", dbx.HashExp{
				"system": systemID, "type": tier.longer,
			})
			require.NoError(t, err)
			require.Len(t, longer, 1, "%s must have rolled up from %d %s records",
				tier.longer, tier.count, tier.shorter)

			raw := readStatsJSON(t, hub, longer[0].Id)
			_, ok := topLevelKeys(t, raw)[plusStatsJSONKey]
			assert.False(t, ok,
				"%s records carry no plus; this is the accepted gap, not an oversight", tier.longer)

			// The rollup still has to work. A record that came out empty
			// because the averaging broke would satisfy the assertion above.
			var rolled system.Stats
			require.NoError(t, json.Unmarshal(raw, &rolled))
			assert.Equal(t, 12.5, rolled.Cpu, "the %s rollup must still average cpu", tier.longer)
		})
	}
}

// mapKeys renders a decoded CBOR map's keys for a failure message.
func mapKeys(m map[any]cbor.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if s, ok := k.(string); ok {
			out = append(out, strconv.Quote(s))
			continue
		}
		out = append(out, fmt.Sprintf("#%v", k))
	}
	return out
}
