//go:build testing

package plus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// zonePath builds the sysfs-shaped location of one thermal zone under root.
//
// The fixture root is a sysfs root, the same thing Collect receives in
// production, so the class/thermal prefix lives here rather than inside the
// collector. Fixtures that dropped it would pass against a collector that
// globbed <root>/thermal_zone* and then read nothing at all on the NAS.
func zonePath(root, zone string) string {
	return filepath.Join(root, "class", "thermal", zone)
}

func TestCollectDPTFConvertsMilliCelsius(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone9"), "type"), "x86_pkg_temp\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone9"), "temp"), "43000\n")

	assert.Equal(t, map[string]float64{"x86_pkg_temp": 43.0}, collectDPTF(root))
}

// TestCollectDPTFKeepsSubDegreePrecision pins the milli-to-celsius conversion
// against integer truncation.
//
// Every other fixture in this file divides evenly by 1000 (43000, 20000,
// 44000, 46000), so a collector written as float64(milli/1000) would round
// 41.45 C down to 41 C and keep all of them green. The NAS does not report
// whole degrees: SEN4 and SEN1 read 41450 and 34850 in the audit capture, and
// pluscheck prints values like 44.2 and 51.2. SEN2's -5050 is not observed
// data, it is here to pin the negative side, where Go truncates toward zero.
// The assertions are exact rather than delta-based so a lost millidegree is a
// failure instead of a tolerance nobody bothers to tighten.
func TestCollectDPTFKeepsSubDegreePrecision(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone5"), "type"), "SEN4\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone5"), "temp"), "41450\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone2"), "type"), "SEN1\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone2"), "temp"), "34850\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone3"), "type"), "SEN2\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone3"), "temp"), "-5050\n")

	assert.Equal(t, map[string]float64{
		"SEN4": 41.45,
		"SEN1": 34.85,
		"SEN2": -5.05,
	}, collectDPTF(root))
}

func TestCollectDPTFSkipsZoneWithoutTemp(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "B0D4\n")
	assert.NotContains(t, collectDPTF(root), "B0D4")
}

func TestCollectDPTFSkipsDisabledZoneSentinel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone1"), "type"), "SEN1\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone1"), "temp"), "-274000\n")
	assert.NotContains(t, collectDPTF(root), "SEN1")
}

func TestCollectDPTFRejectsOutOfRangeCelsius(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone2"), "type"), "BAD\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone2"), "temp"), "999999\n")
	assert.NotContains(t, collectDPTF(root), "BAD")
}

// TestCollectDPTFRangeBounds pins both constants of the -50..150 window and
// whether it is open or closed.
//
// dptf.go rejects celsius < -50 || celsius > 150, so the accepted set is
// closed: -50.0 and 150.0 are readings and are kept, one millidegree past
// either bound is not. No earlier fixture sat near a bound, which left the
// window free to become -150..50 or to flip a comparison to <= / >= without
// turning any test red -- and a bound that drifts silently starts discarding
// real silicon, since this NAS idles in the 30s and has been seen in the high
// 50s. The rejected rows also
// double as truncation guards: float64(milli/1000) turns -50001 into -50.0 and
// 150001 into 150.0, both of which the window would then admit.
func TestCollectDPTFRangeBounds(t *testing.T) {
	cases := []struct {
		milli string
		keep  bool
		want  float64
	}{
		{milli: "-50001", keep: false},
		{milli: "-50000", keep: true, want: -50.0},
		{milli: "-274000", keep: false},
		{milli: "20000", keep: true, want: 20.0},
		{milli: "150000", keep: true, want: 150.0},
		{milli: "150001", keep: false},
		{milli: "999999", keep: false},
	}
	for _, tc := range cases {
		t.Run(tc.milli+"m", func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "SEN1\n")
			writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "temp"), tc.milli+"\n")

			got := collectDPTF(root)
			if !tc.keep {
				assert.Empty(t, got, "milli=%s must be rejected", tc.milli)
				return
			}
			assert.Equal(t, map[string]float64{"SEN1": tc.want}, got)
		})
	}
}

// TestCollectDPTFKeepsZoneTypeWithEmbeddedSpace pins the ACPI type verbatim.
// thermal_zone0 on this NAS reports "INT3400 Thermal", so a collector that
// trimmed the file with strings.Fields would report "INT3400" and a zone whose
// only copy of its own name is that string would be lost.
func TestCollectDPTFKeepsZoneTypeWithEmbeddedSpace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "INT3400 Thermal\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "temp"), "20000\n")

	assert.Equal(t, map[string]float64{"INT3400 Thermal": 20.0}, collectDPTF(root))
}

// TestCollectDPTFPreservesUnderscoreZoneType covers "iwlwifi_1" and
// "pch_cannonlake", the other two shape classes the real machine produces.
func TestCollectDPTFPreservesUnderscoreZoneType(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone8"), "type"), "iwlwifi_1\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone8"), "temp"), "44000\n")

	assert.Equal(t, map[string]float64{"iwlwifi_1": 44.0}, collectDPTF(root))
}

// TestCollectReadsDPTFThroughCollect is the wiring guard for the production
// path: it asserts the fixture root Collect hands down is a sysfs root, so the
// same call shape as Collect("/sys") reaches class/thermal/thermal_zone*.
func TestCollectReadsDPTFThroughCollect(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone9"), "type"), "x86_pkg_temp\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone9"), "temp"), "46000\n")

	s, err := Collect(root, nil)
	require.NoError(t, err)
	require.NotNil(t, s.DPTF)
	assert.Equal(t, map[string]float64{"x86_pkg_temp": 46.0}, s.DPTF.Zones)
}

// TestCollectDPTFIgnoresUnrelatedFilesUnderClassThermal guards the glob against
// the non-zone attributes that live beside the zone directories in sysfs.
//
// The decoy carries a complete, readable type and temp on purpose. An earlier
// version of this fixture only wrote type, so a collector that widened the glob
// to class/thermal/* picked the decoy up, found no temp, skipped it, and
// returned an empty map anyway -- the test passed while the glob was wrong.
// With the temp present, an over-wide glob yields a non-empty map and the
// assertion below fails. A sibling thermal_zone alongside it keeps the
// expectation a positive one: the decoy has to be excluded, not merely
// unusable.
func TestCollectDPTFIgnoresUnrelatedFilesUnderClassThermal(t *testing.T) {
	root := t.TempDir()
	decoy := filepath.Join(root, "class", "thermal", "cooling_device0")
	writeFile(t, filepath.Join(decoy, "type"), "x86_pkg_temp\n")
	writeFile(t, filepath.Join(decoy, "temp"), "50000\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "INT3400 Thermal\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "temp"), "20000\n")

	assert.Equal(t, map[string]float64{"INT3400 Thermal": 20.0}, collectDPTF(root))
}

// --- M15 / M16 follow-ups from the Task 3 re-review --------------------------
// Fixtures only; dptf.go is untouched. Both minors were adjudicated as
// non-blocking, but neither had a test pinning it, so each could have been
// deleted without turning anything red.
//
// M15 -- the empty-type guard. collectDPTF skips a zone when its type file is
// unreadable *or* empty (`err != nil || name == ""`, dptf.go:40). No fixture
// covered the second half: every type-writing fixture in this file supplies a
// real name, so removing `|| name == ""` kept all eight DPTF tests green. The
// re-review confirmed the consequence is not cosmetic -- with the guard gone a
// blank type writes the key "" into the map, which marshals as {"":41.45} and
// reaches the UI as an unnamed reading. The fixtures below use a type file that
// exists and is readable but holds no name, which is what reaches that branch.
func TestCollectDPTFSkipsZoneWithEmptyType(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "\n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "temp"), "41450\n")

	assert.Empty(t, collectDPTF(root))
}

// TestCollectDPTFSkipsZoneWithWhitespaceOnlyType covers the other shape of
// "empty": spaces rather than a bare newline. readTrimmed trims, so the value
// reaching the guard is "" and the guard is what rejects it. This stands in for
// the sysfs case of a type attribute that exists but was never populated.
func TestCollectDPTFSkipsZoneWithWhitespaceOnlyType(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "   \n")
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "temp"), "41450\n")

	assert.Empty(t, collectDPTF(root))
}

// TestCollectDPTFReturnsNilWhenNothingUsable covers M16, the bare `return nil`
// after the loop (dptf.go:62). The re-review established it has zero behaviour
// effect, because Collect gates on `len(d) > 0` and treats a nil map and an
// empty one identically -- so this test pins intent rather than behaviour. Both
// zones below match the thermal_zone* glob, so the early len(matches) == 0
// return at dptf.go:34 must NOT fire; the fixture fails if the collector's
// notion of "nothing to report" ever stops distinguishing "no zone directories"
// from "zone directories that were all unusable".
func TestCollectDPTFReturnsNilWhenNothingUsable(t *testing.T) {
	root := t.TempDir()
	// Matches the glob, has a name, but no temp.
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone0"), "type"), "SEN1\n")
	// Matches the glob and has a temp, but no name.
	writeFile(t, filepath.Join(zonePath(root, "thermal_zone1"), "temp"), "41450\n")

	assert.Nil(t, collectDPTF(root))
}
