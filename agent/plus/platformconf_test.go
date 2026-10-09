//go:build testing

package plus

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePlatformConfReadsTheHostFile(t *testing.T) {
	p, err := ParsePlatformConf(golden(t, "platform.conf"))
	require.NoError(t, err)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0`, p.ECRoot)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1`, p.FANDevice)
	assert.Equal(t, "/sys/class/thermal/thermal_zone9/temp", p.ThermalZone)
	assert.Equal(t, "/dev/pfanctl", p.Dev)
	assert.Equal(t, "0xB5", p.ECCmd)
}

// TestPlatformKeysAreIndependent is the host's own warning, made executable:
// there are machines whose fan device sits outside the EC subtree, so no code
// may derive one key from the other.
func TestPlatformKeysAreIndependent(t *testing.T) {
	src := "EC_ROOT=\\_SB.PC00.LPCB.EC0\n" +
		"FAN_DEVICE=\\_SB.IETM.TFN1\n" +
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n" +
		"DEV=/dev/pfanctl\nEC_CMD=0xB5\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	assert.Equal(t, `\_SB.PC00.LPCB.EC0`, p.ECRoot)
	assert.Equal(t, `\_SB.IETM.TFN1`, p.FANDevice,
		"the fan device is not under the EC root and must not be rewritten as if it were")
	assert.NotContains(t, p.FANDevice, p.ECRoot)
}

// TestParsePlatformConfToleratesPaddingAndInnerEquals records the reading half
// of the host's generation-time rule. The host readers disagree here: the
// panel-side reader splits on the first equals and strips, while the daemon-side
// reader matches a bare "KEY=" prefix. Refusing a padded line would make this
// parser stricter than the side that actually runs.
func TestParsePlatformConfToleratesPaddingAndInnerEquals(t *testing.T) {
	src := "EC_ROOT = \\_SB.PCI0.LPCB.EC0\n" +
		"FAN_DEVICE=\\_SB.PCI0.LPCB.EC0.TFN1\n" +
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n" +
		"DEV=/dev/pfanctl\n" +
		"EC_CMD=0xB5\n" +
		"TRAILING=/a/b=c=d\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0`, p.ECRoot)
	assert.Equal(t, "/sys/class/thermal/thermal_zone9/temp", p.ThermalZone)
	trailing, _ := p.Get("TRAILING")
	assert.Equal(t, "/a/b=c=d", trailing, "only the first equals separates")
}

func TestParsePlatformConfStripsTrailingComments(t *testing.T) {
	src := "EC_ROOT=\\_SB.PCI0.LPCB.EC0 # the embedded controller\n" +
		"FAN_DEVICE=\\_SB.PCI0.LPCB.EC0.TFN1\n" +
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n" +
		"DEV=/dev/pfanctl\nEC_CMD=0xB5\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0`, p.ECRoot)
}

func TestParsePlatformConfRequiresEveryKey(t *testing.T) {
	for _, key := range []string{"EC_ROOT", "FAN_DEVICE", "THERMAL_ZONE", "DEV", "EC_CMD"} {
		src := removeConfKey(golden(t, "platform.conf"), key)
		_, err := ParsePlatformConf(src)
		require.Errorf(t, err, "removing %s must fail", key)
		assert.Containsf(t, err.Error(), key, "the error must name %s", key)
	}
}

func TestParsePlatformConfRejectsEmptyAndCommentOnly(t *testing.T) {
	for _, src := range []string{"", "\n\n", "# nothing but a header\n"} {
		_, err := ParsePlatformConf(src)
		assert.Errorf(t, err, "must refuse %q", src)
	}
}

// TestThermalZoneMustBeAFilePath is the second half of the host's warning. The
// key names a file, and the index is machine-specific, so a bare zone name or a
// directory both have to be refused rather than opened.
func TestThermalZoneMustBeAFilePath(t *testing.T) {
	for _, zone := range []string{
		"thermal_zone9",
		"x86_pkg_temp",
		"/sys/class/thermal/thermal_zone9",
		"/sys/class/thermal/",
		"9",
	} {
		src := strings.Replace(golden(t, "platform.conf"),
			"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp", "THERMAL_ZONE="+zone, 1)
		_, err := ParsePlatformConf(src)
		assert.Errorf(t, err, "must refuse THERMAL_ZONE=%q", zone)
	}
}

func TestParsePlatformConfRejectsMalformedValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"EC_ROOT", ""},
		{"EC_ROOT", "has space"},
		{"FAN_DEVICE", ""},
		{"DEV", "pfanctl"},
		{"EC_CMD", "B5"},
		{"EC_CMD", "0xZZ"},
		{"THERMAL_ZONE", "/sys/class/thermal/thermal_zone9/temp/"},
	} {
		src := setConfKey(golden(t, "platform.conf"), tc.key, tc.value)
		_, err := ParsePlatformConf(src)
		assert.Errorf(t, err, "must refuse %s=%q", tc.key, tc.value)
	}
}

func TestParseFanCandidatesReadsEveryPair(t *testing.T) {
	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	require.Len(t, cands, 3)
	assert.Equal(t, FanCandidate{ECRoot: `\_SB.PCI0.LPCB.EC0`, FANDevice: `\_SB.PCI0.LPCB.EC0.TFN1`}, cands[0])
	assert.Equal(t, FanCandidate{ECRoot: `\_SB.PC00.LPCB.EC0`, FANDevice: `\_SB.PC00.LPCB.EC0.TFN1`}, cands[1])
	assert.Equal(t, FanCandidate{ECRoot: `\_SB.PC00.LPCB.EC0`, FANDevice: `\_SB.IETM.TFN1`}, cands[2],
		"the off-subtree pair is the reason the file lists both columns")
}

// TestFanCandidateColumnsAreExactlyTwo is the column trap. A three-column line
// is a typo in whichever column is wrong, and reading the first two silently
// drops the part that was meant to change.
func TestFanCandidateColumnsAreExactlyTwo(t *testing.T) {
	for _, line := range []string{
		`\_SB.PCI0.LPCB.EC0 \_SB.PCI0.LPCB.EC0.TFN1 extra`,
		`\_SB.PCI0.LPCB.EC0.TFN1`,
		`\_SB.PCI0.LPCB.EC0`,
	} {
		src := line + "\n"
		_, err := ParseFanCandidates(src)
		assert.Errorf(t, err, "must refuse %q", line)
	}
}

// TestFanCandidatesSkipCommentsAndBlanks is the multi-line shape of the host
// file: nine lines carrying three pairs.
func TestFanCandidatesSkipCommentsAndBlanks(t *testing.T) {
	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	for _, c := range cands {
		assert.NotContains(t, c.ECRoot, "#")
		assert.NotContains(t, c.FANDevice, "#")
		assert.NotEmpty(t, c.ECRoot)
		assert.NotEmpty(t, c.FANDevice)
	}
}

func TestFanCandidatesSplitOnAnyRunOfSpaces(t *testing.T) {
	cands, err := ParseFanCandidates("  \\_SB.A  \\_SB.A.TFN1\t\n\n\\_SB.B\t\\_SB.IETM.TFN1  \n")
	require.NoError(t, err)
	require.Len(t, cands, 2)
	assert.Equal(t, FanCandidate{ECRoot: `\_SB.A`, FANDevice: `\_SB.A.TFN1`}, cands[0])
	assert.Equal(t, FanCandidate{ECRoot: `\_SB.B`, FANDevice: `\_SB.IETM.TFN1`}, cands[1])
}

func TestParseFanCandidatesRejectsEmptyAndCommentOnly(t *testing.T) {
	for _, src := range []string{"", "\n\n", "# only a header\n# and a note\n"} {
		_, err := ParseFanCandidates(src)
		assert.Errorf(t, err, "must refuse %q", src)
	}
}

func TestParseFanCandidatesRejectsValueWithSpaces(t *testing.T) {
	_, err := ParseFanCandidates("\\_SB.A \\_SB.A.TFN1 more\n")
	assert.Error(t, err)
}

// TestPlatformAndCandidatesDisagreeIsNotResolvedHere keeps the two ACPI paths in
// separate records. The host probes each candidate and adopts the first that
// answers, so a candidate list is data, not something to reconcile against a
// configured root.
func TestPlatformAndCandidatesDisagreeIsNotResolvedHere(t *testing.T) {
	p, err := ParsePlatformConf(golden(t, "platform.conf"))
	require.NoError(t, err)
	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	assert.Equal(t, p.FANDevice, cands[0].FANDevice, "the host's first candidate is the one in use here")
	assert.NotEqual(t, p.ECRoot, cands[1].ECRoot,
		"the second candidate is a different machine shape, and the file keeps both")
}

func removeConfKey(src, key string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// setConfKey replaces the whole assignment line, so an empty value is tested as an
// empty value rather than leaving the previous one in place.
func setConfKey(src, key, value string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
			lines[i] = key + "=" + value
		}
	}
	return strings.Join(lines, "\n")
}
