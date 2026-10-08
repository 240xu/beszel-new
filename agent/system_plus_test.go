//go:build testing

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/agent/pfanel"
	"github.com/henrygd/beszel/agent/plus"
	"github.com/henrygd/beszel/internal/entities/smart"
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
// fanIntPtr builds the *int FanStats uses to keep a real 0 distinct from an
// unknown duty.
func fanIntPtr(v int) *int { return &v }

func TestInfoPlusSurvivesCborRoundTrip(t *testing.T) {
	original := system.Info{
		Plus: &plus.Stats{Fan: &plus.FanStats{
			EC76Pct: fanIntPtr(61), RPMEst: 2450, RPMEstFlag: true, SampleTsS: 1791333981,
		}},
	}
	b, err := cbor.Marshal(original)
	require.NoError(t, err)

	var decoded system.Info
	require.NoError(t, cbor.Unmarshal(b, &decoded))
	require.NotNil(t, decoded.Plus)
	require.NotNil(t, decoded.Plus.Fan)
	// EC76Pct is a pointer so a real 0 duty stays distinct from an unknown one, and
	// that distinction has to survive the cache encoding as well as the JSON wire --
	// a cbor round trip that flattened the pointer would turn "duty 61" back into a
	// value no consumer could tell from "duty not recorded".
	require.NotNil(t, decoded.Plus.Fan.EC76Pct, "the duty pointer must survive cbor")
	assert.Equal(t, 61, *decoded.Plus.Fan.EC76Pct)
	assert.Equal(t, uint16(2450), decoded.Plus.Fan.RPMEst)
	assert.True(t, decoded.Plus.Fan.RPMEstFlag)
	assert.Equal(t, int64(1791333981), decoded.Plus.Fan.SampleTsS)

	// And a nil pointer must round trip as nil rather than as a pointer to 0.
	b, err = cbor.Marshal(system.Info{
		Plus: &plus.Stats{Fan: &plus.FanStats{EC76Pct: nil, RPMEstFlag: true}},
	})
	require.NoError(t, err)
	var unknown system.Info
	require.NoError(t, cbor.Unmarshal(b, &unknown))
	require.NotNil(t, unknown.Plus)
	require.NotNil(t, unknown.Plus.Fan)
	assert.Nil(t, unknown.Plus.Fan.EC76Pct, "an unknown duty must not decode as 0")
}

// ---------------------------------------------------------------------------
// nvmePlusInput (agent/system.go) — the producer side of the NVMe projection.
//
// nvmePlusInput is the only place between upstream's smartctl output and
// agent/plus, and it is where a mistake is invisible downstream: collectNvme's
// all-or-nothing check turns a renamed attribute into a missing panel and a
// wrong field width turns every counter into 0, and neither crashes or reports
// an error. The tests below drive it through the real production type —
// SmartDataMap is an exported field, so updatePlusStats' call path is the one
// under test, with no seam and no stand-in between.
// ---------------------------------------------------------------------------

// nvmeValueSentinel is written into every SmartAttribute.Value of the synthetic
// fixture. Upstream never sets Value for an NVMe health log, so a reader that
// took Value instead of RawValue would otherwise be untestable; Value is also
// uint16, so such a reader truncates every real counter as well. The sentinel
// is distinct from every RawValue below so it cannot pass as a real reading.
const nvmeValueSentinel = 4242

// nvmeProjectedAttrs are the attribute names agent/plus consumes. Transcribed
// from nvme.go's nvmeAttrs, which package agent cannot import; the test that
// compares against upstream's own parser output is what keeps the two honest.
var nvmeProjectedAttrs = []string{
	"AvailableSpare", "PercentageUsed", "DataUnitsRead", "DataUnitsWritten",
	"PowerOnHours", "PowerCycles", "UnsafeShutdowns", "MediaErrors",
	"ControllerBusyTime", "CriticalCompTime",
}

// nvmeSyntheticAttrs is the attribute list upstream's parseSmartForNvme emits,
// with two properties a real capture does not give for free: every RawValue is
// distinct, so a transposition moves a number into the wrong field; and four of
// them exceed uint16's 65535, so a reader that went through Value — or merely
// truncated — is visible rather than quietly wrong. Magnitudes stay in the range
// the live drive reports so a passing test is not passing on fantasy numbers.
func nvmeSyntheticAttrs() []*smart.SmartAttribute {
	raw := []struct {
		name  string
		value uint64
	}{
		{"CriticalWarning", 0},
		{"Temperature", 44},
		{"AvailableSpare", 100},
		{"AvailableSpareThreshold", 10},
		{"PercentageUsed", 3},
		{"DataUnitsRead", 2440192},
		{"DataUnitsWritten", 803115},
		{"HostReads", 31882910},
		{"HostWrites", 20514773},
		{"ControllerBusyTime", 46},
		{"PowerCycles", 34},
		{"PowerOnHours", 183},
		{"UnsafeShutdowns", 19},
		{"MediaErrors", 5},
		{"NumErrLogEntries", 7},
		{"WarningTempTime", 11},
		{"CriticalCompTime", 13},
	}
	attrs := make([]*smart.SmartAttribute, 0, len(raw))
	for i, r := range raw {
		attrs = append(attrs, &smart.SmartAttribute{
			ID:       uint16(i + 1),
			Name:     r.name,
			Value:    nvmeValueSentinel,
			RawValue: r.value,
		})
	}
	return attrs
}

// nvmeManagerFixture is a *SmartManager holding one NVMe drive. Temperature is
// 31 while the matching attribute reads 44, so "the composite temperature comes
// from the top-level field, not the attribute" is distinguishable rather than
// assumed — upstream happens to write the same number into both.
func nvmeManagerFixture(attrs []*smart.SmartAttribute) *SmartManager {
	return &SmartManager{SmartDataMap: map[string]*smart.SmartData{
		"NVME-SERIAL-0001": {
			ModelName:   "HFM128GD3GX013N-SKhynix",
			DiskName:    "/dev/nvme0",
			DiskType:    nvmeDiskType,
			Temperature: 31,
			Attributes:  attrs,
		},
	}}
}

// TestNvmePlusInputReadsRawValueNotValue pins the field width. SmartAttribute
// carries the same reading twice — Value (uint16) and RawValue (uint64) — and
// NVMe's own counters are uint64, so reading Value silently truncates
// data_units_read, host_reads and host_writes to their low 16 bits while every
// other field stays plausible. Nothing about the resulting payload looks wrong.
func TestNvmePlusInputReadsRawValueNotValue(t *testing.T) {
	in := nvmePlusInput(nvmeManagerFixture(nvmeSyntheticAttrs()))
	require.NotNil(t, in, "an NVMe drive in the map must produce input")

	for _, attr := range nvmeSyntheticAttrs() {
		got, ok := in.Attrs[attr.Name]
		require.True(t, ok, "%s must be present", attr.Name)
		assert.Equal(t, attr.RawValue, got,
			"%s must carry RawValue; Value is uint16 and would truncate %d",
			attr.Name, attr.RawValue)
	}
	assert.NotEqual(t, uint64(nvmeValueSentinel), in.Attrs["DataUnitsRead"],
		"the fixture's Value sentinel must never surface")
}

// TestNvmePlusInputKeepsUpstreamAttributeNameCasing pins the spelling.
// collectNvme looks names up case-sensitively and drops the whole section when
// one is missing, so folding the case makes all ten lookups miss at once: the
// panel disappears with no error, which is exactly the failure a consumer
// cannot detect from the payload alone.
func TestNvmePlusInputKeepsUpstreamAttributeNameCasing(t *testing.T) {
	in := nvmePlusInput(nvmeManagerFixture(nvmeSyntheticAttrs()))
	require.NotNil(t, in)

	want := make([]string, 0, len(nvmeSyntheticAttrs()))
	for _, attr := range nvmeSyntheticAttrs() {
		want = append(want, attr.Name)
	}
	sort.Strings(want)

	got := make([]string, 0, len(in.Attrs))
	for name := range in.Attrs {
		got = append(got, name)
	}
	sort.Strings(got)

	assert.Equal(t, want, got,
		"attribute names must survive verbatim: collectNvme matches them case-sensitively")
	for _, name := range want {
		assert.NotContains(t, in.Attrs, strings.ToLower(name),
			"%s must not be present lowercased as well", name)
	}
}

// TestNvmePlusInputAgreesWithUpstreamParse is the root guard a transcribed
// fixture cannot be: it runs upstream's own parser over a captured
// smartctl -aj payload and requires the flattened map to equal, name for name
// and value for value, what upstream emitted. A rename or a field-width change
// inside agent/smart.go therefore turns this red on its own, with no copy of the
// list here to fall quietly out of date.
func TestNvmePlusInputAgreesWithUpstreamParse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("test-data", "smart", "nvme0.json"))
	require.NoError(t, err)

	sm := &SmartManager{SmartDataMap: make(map[string]*smart.SmartData)}
	hasData, _ := sm.parseSmartForNvme(data, "")
	require.True(t, hasData, "the captured payload must parse")

	var parsed *smart.SmartData
	for _, d := range sm.SmartDataMap {
		parsed = d
	}
	require.NotNil(t, parsed)

	want := make(map[string]uint64, len(parsed.Attributes))
	for _, attr := range parsed.Attributes {
		want[attr.Name] = attr.RawValue
	}

	in := nvmePlusInput(sm)
	require.NotNil(t, in, "a parsed NVMe drive must produce input")
	assert.Equal(t, want, in.Attrs,
		"the flattened map must equal upstream's own attribute list verbatim")
	for _, name := range nvmeProjectedAttrs {
		assert.Contains(t, in.Attrs, name,
			"collectNvme is all-or-nothing, so a missing name silently empties the section")
	}
}

// TestNvmePlusInputFeedsDistinctValuePerProjectedField carries the whole path
// through agent/plus, so a transposition on the producer side is caught by the
// wire field it actually reaches rather than by the intermediate map alone.
// unsafe_shutdowns and media_errors are the pair most likely to swap.
func TestNvmePlusInputFeedsDistinctValuePerProjectedField(t *testing.T) {
	in := nvmePlusInput(nvmeManagerFixture(nvmeSyntheticAttrs()))
	require.NotNil(t, in)

	stats, err := plus.Collect(t.TempDir(), in, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.Nvme, "the nvme section must survive the projection")

	assert.Equal(t, "HFM128GD3GX013N-SKhynix", stats.Nvme.Model)
	assert.Equal(t, 31.0, stats.Nvme.TempC,
		"composite temp comes from SmartData.Temperature, not the Temperature attribute (44)")
	assert.Equal(t, 100.0, stats.Nvme.AvailableSparePct)
	assert.Equal(t, 3.0, stats.Nvme.PercentageUsedPct)
	assert.Equal(t, uint64(2440192), stats.Nvme.DataUnitsRead)
	assert.Equal(t, uint64(803115), stats.Nvme.DataUnitsWritten)
	assert.Equal(t, uint64(183), stats.Nvme.PowerOnHours)
	assert.Equal(t, uint64(34), stats.Nvme.PowerCycles)
	assert.Equal(t, uint64(19), stats.Nvme.UnsafeShutdowns)
	assert.Equal(t, uint64(5), stats.Nvme.MediaErrors,
		"must not carry unsafe_shutdowns' value")
	assert.Equal(t, uint64(46), stats.Nvme.ControllerBusyMin)
	assert.Equal(t, uint64(13), stats.Nvme.CriticalCompTimeMin)
}

// TestNvmePlusInputPicksLowestSerialNvme covers the one-drive limit honestly.
// The map is keyed by serial, so with more than one NVMe there is no way to
// report both without breaking the wire; the choice is the lexicographically
// first serial rather than whatever order the map happened to iterate in.
// Repeating the call is the point: map order varies between runs, so a single
// assertion cannot tell a sorted choice from a lucky one.
func TestNvmePlusInputPicksLowestSerialNvme(t *testing.T) {
	sm := &SmartManager{SmartDataMap: map[string]*smart.SmartData{}}
	for serial, model := range map[string]string{
		"SERIAL-C": "third", "SERIAL-A": "first", "SERIAL-B": "second",
	} {
		sm.SmartDataMap[serial] = &smart.SmartData{
			ModelName: model, DiskType: nvmeDiskType, Attributes: nvmeSyntheticAttrs(),
		}
	}

	for range 32 {
		in := nvmePlusInput(sm)
		require.NotNil(t, in)
		assert.Equal(t, "first", in.Model, "the choice is by serial, not by map iteration order")
	}
}

// TestNvmePlusInputIgnoresNonNvmeDrives keeps the SATA and mdraid entries that
// share the map out of the projection. They can carry attribute lists shaped
// the same way, so the DiskType filter is the only thing standing between them
// and a panel of plausible NVMe nonsense.
func TestNvmePlusInputIgnoresNonNvmeDrives(t *testing.T) {
	sm := &SmartManager{SmartDataMap: map[string]*smart.SmartData{
		"SATA-1": {ModelName: "sata", DiskType: "sat", Attributes: nvmeSyntheticAttrs()},
		"ATA-1":  {ModelName: "ata", DiskType: "ata", Attributes: nvmeSyntheticAttrs()},
	}}

	assert.Nil(t, nvmePlusInput(sm), "only an NVMe drive may be projected")
}

// TestNvmePlusInputReturnsNilWithoutUsableData pins the four early returns to
// nil. Absent input must stay absent rather than becoming a zero struct: the
// NVMe section's two omitempty levels mean "section missing" and "every field
// zero" are different claims, and only the first of them is true here.
func TestNvmePlusInputReturnsNilWithoutUsableData(t *testing.T) {
	cases := []struct {
		name string
		sm   *SmartManager
	}{
		{"nil manager", nil},
		{"nil map", &SmartManager{}},
		{"empty map", &SmartManager{SmartDataMap: map[string]*smart.SmartData{}}},
		{"only a nil entry", &SmartManager{SmartDataMap: map[string]*smart.SmartData{"S1": nil}}},
		{"no nvme drive", &SmartManager{SmartDataMap: map[string]*smart.SmartData{
			"SATA-1": {DiskType: "sat"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Nil(t, nvmePlusInput(tc.sm),
				"without usable upstream data the NVMe section must be absent, not zero")
		})
	}
}

// TestNvmePlusInputSkipsNilAttributePointer covers the one defensive branch
// upstream's parsed list never takes but a hand-built SmartData can: a nil entry
// must be skipped, not dereferenced.
func TestNvmePlusInputSkipsNilAttributePointer(t *testing.T) {
	attrs := append([]*smart.SmartAttribute{nil}, nvmeSyntheticAttrs()...)

	in := nvmePlusInput(nvmeManagerFixture(attrs))
	require.NotNil(t, in)
	assert.Len(t, in.Attrs, len(nvmeSyntheticAttrs()),
		"a nil attribute must be skipped, not counted")
}

// TestNvmePlusInputKeepsLegitimateZeroAttributes pins the contract Task 18's
// `?? 0` substitution rests on. Upstream emits an attribute even when the
// reading is legitimately zero — percentage_used, media_errors and
// critical_comp_time are all zero on a healthy new drive — so a zero has to
// survive as a present key holding 0. Dropping zero-valued attributes would
// not merely lose data: collectNvme's all-or-nothing check would take the whole
// section with it, and the panel would disappear on the healthiest machines.
func TestNvmePlusInputKeepsLegitimateZeroAttributes(t *testing.T) {
	attrs := []*smart.SmartAttribute{
		{Name: "CriticalWarning", RawValue: 0},
		{Name: "Temperature", RawValue: 31},
		{Name: "AvailableSpare", RawValue: 100},
		{Name: "AvailableSpareThreshold", RawValue: 10},
		{Name: "PercentageUsed", RawValue: 0},
		{Name: "DataUnitsRead", RawValue: 100},
		{Name: "DataUnitsWritten", RawValue: 200},
		{Name: "HostReads", RawValue: 300},
		{Name: "HostWrites", RawValue: 400},
		{Name: "ControllerBusyTime", RawValue: 5},
		{Name: "PowerCycles", RawValue: 6},
		{Name: "PowerOnHours", RawValue: 7},
		{Name: "UnsafeShutdowns", RawValue: 8},
		{Name: "MediaErrors", RawValue: 0},
		{Name: "NumErrLogEntries", RawValue: 0},
		{Name: "WarningTempTime", RawValue: 0},
		{Name: "CriticalCompTime", RawValue: 0},
	}
	in := nvmePlusInput(nvmeManagerFixture(attrs))
	require.NotNil(t, in)

	for _, name := range []string{
		"CriticalWarning", "PercentageUsed", "MediaErrors",
		"NumErrLogEntries", "WarningTempTime", "CriticalCompTime",
	} {
		got, ok := in.Attrs[name]
		assert.True(t, ok, "%s must be present even when the reading is zero", name)
		assert.Zero(t, got, "%s must carry its zero through", name)
	}

	stats, err := plus.Collect(t.TempDir(), in, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.Nvme, "a healthy drive reading zero must still get the section")
	assert.Zero(t, stats.Nvme.MediaErrors)
	assert.Zero(t, stats.Nvme.PercentageUsedPct)
}

// ---------------------------------------------------------------------------
// fanPlusInput (agent/system.go) — error grading.
//
// Every pfanel failure used to reach the same slog.Debug line, which is what made
// a lost column invisible: a schema drift that stopped the fan panel dead looked
// exactly like a machine with no pfanel, and neither left a trace worth reading.
// The grading is the fix, so it is asserted here rather than read off a live log.
// ---------------------------------------------------------------------------

// logCapture records the levels a call emits. It implements slog.Handler itself
// rather than parsing text, so the assertion is on the level the code chose.
type logCapture struct{ levels []slog.Level }

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.levels = append(c.levels, r.Level)
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// captureFanPlusInput swaps both seams fanPlusInput reads through — the pfanel
// reader and the default logger — and restores them afterwards, so the cases below
// cannot leak into each other or into the rest of the suite.
func captureFanPlusInput(t *testing.T, read func(string) (*plus.FanSample, error)) *logCapture {
	t.Helper()
	prevRead, prevLog := fanLatestSample, slog.Default()
	t.Cleanup(func() { fanLatestSample, _ = prevRead, prevLog; slog.SetDefault(prevLog) })
	cap := &logCapture{}
	fanLatestSample = read
	slog.SetDefault(slog.New(cap))
	return cap
}

// TestFanPlusInputGradesEveryPfanelFailure walks the four classes LatestSample can
// return plus one it cannot yet produce, because the point of the exercise is that
// the level is a decision per class rather than one default for all of them.
//
// Two of them are silent on purpose: no database and an empty table are what a
// machine without pfanel and a fresh install look like, and logging either once a
// minute would train the reader to ignore the log. ErrOpen is Warn because the
// file is there and unreadable, which may clear by itself. ErrRead is Error because
// pfanel and this reader disagree about the row, which no interval repairs.
func TestFanPlusInputGradesEveryPfanelFailure(t *testing.T) {
	cases := []struct {
		name string
		read func(string) (*plus.FanSample, error)
		want []slog.Level
	}{
		{
			name: "no database at all is not a fault",
			read: func(string) (*plus.FanSample, error) { return nil, nil },
			want: nil,
		},
		{
			name: "an empty samples table is a fresh install",
			read: func(string) (*plus.FanSample, error) { return nil, fmt.Errorf("scan: %w", pfanel.ErrNoRow) },
			want: nil,
		},
		{
			name: "installed but unopenable warns",
			read: func(string) (*plus.FanSample, error) {
				return nil, fmt.Errorf("%w %s: %w", pfanel.ErrOpen, pfanel.DBPath, fs.ErrPermission)
			},
			want: []slog.Level{slog.LevelWarn},
		},
		{
			name: "an unreadable row errors, because that is schema drift",
			read: func(string) (*plus.FanSample, error) {
				return nil, fmt.Errorf("%w %s: %w", pfanel.ErrRead, pfanel.DBPath, errNoSuchColumn)
			},
			want: []slog.Level{slog.LevelError},
		},
		{
			name: "an unclassified failure stays debug rather than being guessed at",
			read: func(string) (*plus.FanSample, error) { return nil, errFuturePfanelClass },
			want: []slog.Level{slog.LevelDebug},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := captureFanPlusInput(t, tc.read)
			got := fanPlusInput()
			assert.Nil(t, got, "a failed read must yield no sample")
			assert.Equal(t, tc.want, cap.levels)
		})
	}
}

// TestFanPlusInputReturnsTheSampleOnSuccess keeps the grading from swallowing the
// happy path: the cases above all return nil, so nothing in them would notice a
// refactor that returned nil unconditionally.
func TestFanPlusInputReturnsTheSampleOnSuccess(t *testing.T) {
	want := &plus.FanSample{TsUnix: 1791333981, Gov: "curve"}
	cap := captureFanPlusInput(t, func(string) (*plus.FanSample, error) { return want, nil })

	got := fanPlusInput()
	assert.Same(t, want, got, "a readable row must pass through untouched")
	assert.Empty(t, cap.levels, "a successful read has nothing to report")
}

var (
	// errNoSuchColumn is what a pfanel that dropped a column produces, spelled the
	// way the driver spells it, so the ErrRead case is the real failure rather than
	// a convenient label for one.
	errNoSuchColumn      = errors.New(`SQL logic error: no such column: "rpm" (1)`)
	errFuturePfanelClass = errors.New("pfanel: some failure mode that does not exist yet")
)
