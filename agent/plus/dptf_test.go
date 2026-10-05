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

	s, err := Collect(root)
	require.NoError(t, err)
	require.NotNil(t, s.DPTF)
	assert.Equal(t, map[string]float64{"x86_pkg_temp": 46.0}, s.DPTF.Zones)
}

// TestCollectDPTFIgnoresUnrelatedFilesUnderClassThermal guards the glob against
// the non-zone attributes that live beside the zone directories in sysfs.
func TestCollectDPTFIgnoresUnrelatedFilesUnderClassThermal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "class", "thermal", "cooling_device0", "type"), "x86_pkg_temp\n")
	assert.Empty(t, collectDPTF(root))
}
