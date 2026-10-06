//go:build testing

package plus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wirelessFixture is the brief's fixture. Columns 2..4 (link, level, noise)
// carry the '.' marker and noise is the bare -256 this NAS prints, which is the
// shape both halves of the quality-marker rule have to accept.
const wirelessFixture = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
  wlp0s20f3: 0000   57.  -53.  -256        0      0      0      6      8        0
`

func TestParseWirelessExtractsSignalAndRetry(t *testing.T) {
	got := parseWireless(wirelessFixture)
	require.NotNil(t, got)
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, -53, got.SignalDbm)
	assert.Equal(t, uint64(6), got.Retry)
}

func TestParseWirelessReturnsNilWithoutDataRow(t *testing.T) {
	assert.Nil(t, parseWireless("Inter-| sta-|\n face | tus |\n"))
}

func TestApplyIwLinkExtractsBitrateAndFreq(t *testing.T) {
	s := &WiFiStats{Iface: "wlp0s20f3"}
	applyIwLink(s, "\tfreq: 5180.0\n\tsignal: -53 dBm\n\ttx bitrate: 780.0 MBit/s VHT-MCS 8\n")
	assert.Equal(t, 780.0, s.TxMBit)
	assert.Equal(t, 5180, s.FreqMHz)
}

// wirelessLiveFixture is this NAS's own table, captured from /proc/net/wireless.
//
// Retry is 751 and misc is 4, which is the whole point of the fixture: the two
// counters differ by two orders of magnitude, so a parser that shifted one
// column lands on 4 (or on beacon's 0) and cannot pass by coincidence.
const wirelessLiveFixture = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70.  -39.  -256        0      0      0    751      4        0
`

func TestParseWirelessReadsLiveRow(t *testing.T) {
	got := parseWireless(wirelessLiveFixture)
	require.NotNil(t, got)
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, -39, got.SignalDbm)
	assert.Equal(t, uint64(751), got.Retry)
	// iw is a separate input; the table alone can say nothing about the rate.
	assert.Zero(t, got.TxMBit)
	assert.Zero(t, got.FreqMHz)
}

// wirelessQualityClearedFixture renders the same live row with the quality
// markers CLEARED, which is what net/wireless/wext-proc.c prints when the driver
// leaves IW_QUAL_QUAL_UPDATED, IW_QUAL_LEVEL_UPDATED and IW_QUAL_NOISE_UPDATED
// unset: %3d followed by ' ' instead of '.'. It is the same eleven columns and
// the same three numbers, only the marker byte differs, so a parser must accept
// it and return byte-identical readings.
const wirelessQualityClearedFixture = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70   -39  -256        0      0      0    751      4        0
`

// TestParseWirelessAcceptsClearedQualityMarkers is the paired assertion with
// TestParseWirelessAcceptsNoiseMarkerSet: the marker byte is a per-driver flag,
// not a fixed character, so both values have to parse to the same numbers.
func TestParseWirelessAcceptsClearedQualityMarkers(t *testing.T) {
	cleared := parseWireless(wirelessQualityClearedFixture)
	marked := parseWireless(wirelessLiveFixture)
	require.NotNil(t, cleared)
	require.NotNil(t, marked)
	assert.Equal(t, marked, cleared,
		"the quality marker byte is a driver flag, not part of the value")
	assert.Equal(t, -39, cleared.SignalDbm)
	assert.Equal(t, uint64(751), cleared.Retry)
}

// wirelessNoiseMarkerSetFixture is the third marker combination: noise set,
// link and level clear. A parser that assumes noise is always bare -- which is
// what this machine happens to print -- silently drops the whole section on any
// driver that does set IW_QUAL_NOISE_UPDATED.
const wirelessNoiseMarkerSetFixture = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70   -39  -256.       0      0      0    751      4        0
`

func TestParseWirelessAcceptsNoiseMarkerSet(t *testing.T) {
	got := parseWireless(wirelessNoiseMarkerSetFixture)
	require.NotNil(t, got)
	assert.Equal(t, -39, got.SignalDbm)
	assert.Equal(t, uint64(751), got.Retry)
}

// wirelessHeaderOnlyFixture is the complete two-line header the kernel prints
// before any device. Line 2 is the trap for a Fields()-based rewrite: it splits
// into 17 tokens, far more than the 11 of a data row, so a length test alone
// lets it through and 17 tokens of legend turns into a bogus reading.
const wirelessHeaderOnlyFixture = `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
`

func TestParseWirelessIgnoresCompleteHeader(t *testing.T) {
	assert.Nil(t, parseWireless(wirelessHeaderOnlyFixture),
		"the 17-token legend line must not be mistaken for a data row")
}

// otherLayouts are tables whose column count is not the eleven the kernel
// prints. The parser must refuse both and must SAY that it refused: refusing is
// safe, refusing silently is the failure this guard exists to prevent.
var otherLayouts = map[string]string{
	// One column more than the kernel emits.
	"extra-column": `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70.  -39.  -256        0      0      0    751      4        0     17
`,
	// Two columns fewer: the retry/misc pair gone.
	"missing-counters": `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70.  -39.  -256        0      0      0
`,
	// Four columns fewer: quality only.
	"quality-only": `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
wlp0s20f3: 0000   70.  -39.  -256
`,
}

func TestParseWirelessRejectsOtherColumnCounts(t *testing.T) {
	for name, table := range otherLayouts {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, parseWireless(table),
				"an unrecognised layout must yield no data, never a shifted reading")
			assert.True(t, wirelessLayoutUnrecognised(table),
				"an unrecognised layout must be reportable, not silent")
		})
	}
}

// TestWirelessLayoutUnrecognisedSeparatesAbsentFromUnknown is the guard's whole
// purpose: "this machine has no wireless table", "the table is empty" and "the
// table has a shape this build does not parse" are three different states, and
// only the last one means the code is out of date.
func TestWirelessLayoutUnrecognisedSeparatesAbsentFromUnknown(t *testing.T) {
	assert.False(t, wirelessLayoutUnrecognised(wirelessLiveFixture),
		"the layout this build parses is not unrecognised")
	assert.False(t, wirelessLayoutUnrecognised(wirelessHeaderOnlyFixture),
		"a header with no device is absent, not unrecognised")
	assert.False(t, wirelessLayoutUnrecognised(""),
		"an empty file is absent, not unrecognised")
	assert.False(t, wirelessLayoutUnrecognised("Inter-| sta-|\n face | tus |\n"),
		"a truncated header is absent, not unrecognised")
	assert.True(t, wirelessLayoutUnrecognised(otherLayouts["extra-column"]))
}

// TestParseWirelessTakesTheFirstDeviceRow pins which row wins when a machine
// has several radios: parseWireless returns on the first match, so the order is
// the kernel's enumeration order and the result is the first device listed.
func TestParseWirelessTakesTheFirstDeviceRow(t *testing.T) {
	two := wirelessHeaderOnlyFixture +
		"wlp0s20f3: 0000   70.  -39.  -256        0      0      0    751      4        0\n" +
		"wlan1: 0000   55.  -61.  -256        0      0      0      2      0        1\n"
	got := parseWireless(two)
	require.NotNil(t, got)
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, -39, got.SignalDbm)
}

// iwLinkLive is this NAS's own `iw dev wlp0s20f3 link` output, captured
// verbatim (leading tab on every indented line).
//
// rx and tx bitrate differ and rx comes FIRST, which is what makes it a
// discrimination fixture: a pattern loose enough to catch "rx bitrate:" takes
// the other number and looks plausible doing it. On this link rx really is the
// larger of the two, so the two spellings are not interchangeable.
const iwLinkLive = "Connected to 7c:33:f9:fe:b3:08 (on wlp0s20f3)\n" +
	"\tSSID: CMCC-Da3Z-5G\n" +
	"\tfreq: 5180.0\n" +
	"\tRX: 369492485 bytes (583361 packets)\n" +
	"\tTX: 157939492 bytes (673300 packets)\n" +
	"\tsignal: -38 dBm\n" +
	"\trx bitrate: 1560.0 MBit/s VHT-MCS 8 160MHz short GI VHT-NSS 2\n" +
	"\ttx bitrate: 1170.0 MBit/s VHT-MCS 6 160MHz short GI VHT-NSS 2\n" +
	"\tbss flags: short-preamble short-slot-time\n" +
	"\tdtim period: 1\n" +
	"\tbeacon int: 100\n"

func TestApplyIwLinkUsesTxBitrateNotRx(t *testing.T) {
	s := &WiFiStats{Iface: "wlp0s20f3"}
	applyIwLink(s, iwLinkLive)
	assert.Equal(t, 1170.0, s.TxMBit, "rx bitrate must not be reported as tx bitrate")
	assert.Equal(t, 5180, s.FreqMHz)
}

// TestApplyIwLinkLeavesTxZeroWithoutTxBitrate covers the disconnected case:
// iw prints "Not connected." on stdout with a zero exit status, so there is no
// tx bitrate line and the field has to stay at its zero fill rather than pick
// up whatever the payload happens to carry.
func TestApplyIwLinkLeavesTxZeroWithoutTxBitrate(t *testing.T) {
	s := &WiFiStats{Iface: "wlan1"}
	applyIwLink(s, "Not connected.\n")
	assert.Zero(t, s.TxMBit)
	assert.Zero(t, s.FreqMHz)
}

// iwLinkWithCenterFreq encodes the 2.4 GHz link report, where iw carries the
// AP's channel and centre frequency as well as the negotiated one.
//
// iw's source could not be fetched in this environment, so the exact spelling
// of those two lines is the one part of this fixture that is modelled rather
// than captured -- and it is modelled defensively. "freq:" as a bare substring
// would match the tail of "center freq:", and the two differ on any HT40 or
// 160 MHz association, so anchoring the pattern to the start of a line is
// cheaper than being certain. What the test guarantees either way is that a
// pattern matching a line ENDING in freq: cannot report the centre as the
// channel, and that the rate still comes from the tx line.
const iwLinkWithCenterFreq = "Connected to 02:11:22:33:44:55 (on wlp0s20f3)\n" +
	"\tSSID: office-2g\n" +
	"\tchannel: 6 (2437 MHz)\n" +
	"\tcenter freq: 2437 (2437 MHz)\n" +
	"\tfreq: 2412\n" +
	"\trx bitrate: 72.2 MBit/s\n" +
	"\ttx bitrate: 65.0 MBit/s HT-MCS 7 short GI\n"

func TestApplyIwLinkPrefersFreqOverCenterFreq(t *testing.T) {
	s := &WiFiStats{Iface: "wlp0s20f3"}
	applyIwLink(s, iwLinkWithCenterFreq)
	assert.Equal(t, 2412, s.FreqMHz, "center freq must not be read as the channel frequency")
	assert.Equal(t, 65.0, s.TxMBit)
}

// TestApplyIwLinkHandlesChineseSSID is here because the SSID is arbitrary
// bytes: a payload that parses the radio fields must not care what precedes
// them, and nothing in the patterns may assume printable ASCII.
func TestApplyIwLinkHandlesNonASCIISsid(t *testing.T) {
	s := &WiFiStats{Iface: "wlp0s20f3"}
	applyIwLink(s, "Connected to 02:11:22:33:44:55 (on wlp0s20f3)\n"+
		"\tSSID: \xe5\xae\xa2\xe6\x88\xb7\xe7\xbd\x91\xe7\xbb\x9c 5G\n"+
		"\tfreq: 5180.0\n"+
		"\tsignal: -70 dBm\n"+
		"\ttx bitrate: 200.0 MBit/s VHT-MCS 4\n")
	assert.Equal(t, 5180, s.FreqMHz)
	assert.Equal(t, 200.0, s.TxMBit)
}

// fakeIw installs an executable named iw that prints body, so the tests decide
// what the radio tool says instead of depending on whatever access point this
// machine happens to be joined to.
//
// The script reaches cat by absolute path: these tests replace PATH outright, so
// a helper resolved through PATH fails with exit 127 and the collector reports
// an empty string -- which reads exactly like "iw said nothing useful".
func fakeIw(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "iw")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nexec /bin/cat <<'IWEOF'\n"+body+"IWEOF\n"), 0o755))
	t.Setenv("PATH", dir)
}

// TestCollectWiFiReadsFixtureRoot is the path-semantics test: /proc/net/wireless
// is outside sysfs, so the collector has to resolve it through sourcePath. A
// plain join onto the root would produce <root>/proc/net/wireless and read
// nothing, and at runtime on the NAS that would mean /sys/proc/net/wireless --
// a silent empty section.
//
// The fake iw is on PATH to prove the second half: an injected root means "read
// this tree", so the collector must not also shell out to the host's real radio,
// which would splice live access-point numbers into a fixture assertion.
func TestCollectWiFiReadsFixtureRoot(t *testing.T) {
	fakeIw(t, iwLinkLive)
	root := t.TempDir()
	table := sourcePath(root, wirelessProcPath)
	require.Equal(t, filepath.Join(root, wirelessProcPath), table)
	require.NoError(t, os.MkdirAll(filepath.Dir(table), 0o755))
	require.NoError(t, os.WriteFile(table, []byte(wirelessLiveFixture), 0o644))

	got := collectWiFi(root)
	require.NotNil(t, got)
	assert.Equal(t, "wlp0s20f3", got.Iface)
	assert.Equal(t, -39, got.SignalDbm)
	assert.Equal(t, uint64(751), got.Retry)
	assert.Zero(t, got.TxMBit, "an injected root must not consult the host radio")
	assert.Zero(t, got.FreqMHz, "an injected root must not consult the host radio")
}

// TestCollectWiFiReturnsNilWithoutProcTable covers both absent inputs: no table
// under the fixture root, and a table with no device in it.
func TestCollectWiFiReturnsNilWithoutProcTable(t *testing.T) {
	assert.Nil(t, collectWiFi(t.TempDir()))

	root := t.TempDir()
	table := sourcePath(root, wirelessProcPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(table), 0o755))
	require.NoError(t, os.WriteFile(table, []byte(wirelessHeaderOnlyFixture), 0o644))
	assert.Nil(t, collectWiFi(root))
}

// TestCollectWiFiReportsTableWithoutIw pins the degradation rule: a missing iw
// costs the two rate fields and nothing else. Losing signal_dbm and retry
// because a helper binary is absent would turn a partial reading into no
// reading.
func TestCollectWiFiReportsTableWithoutIw(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	table := sourcePath(root, wirelessProcPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(table), 0o755))
	require.NoError(t, os.WriteFile(table, []byte(wirelessLiveFixture), 0o644))

	got := collectWiFi(root)
	require.NotNil(t, got)
	assert.Equal(t, -39, got.SignalDbm)
	assert.Equal(t, uint64(751), got.Retry)
}

// TestIwLinkOutputReadsTheFakeTool is the positive half of the fixture-root
// test: the subprocess call itself works, is bounded, and returns the tool's
// output verbatim.
func TestIwLinkOutputReadsTheFakeTool(t *testing.T) {
	fakeIw(t, iwLinkLive)
	assert.Equal(t, iwLinkLive, iwLinkOutput("wlp0s20f3"))
}

// TestLookIwFindsToolOnPath covers the ordinary case: iw resolves through PATH.
func TestLookIwFindsToolOnPath(t *testing.T) {
	fakeIw(t, iwLinkLive)
	assert.Equal(t, filepath.Join(os.Getenv("PATH"), "iw"), lookIw())
}

// TestLookIwFindsAbsoluteCandidateWithoutPath is the case that decides whether
// this collector works in production at all. The agent runs as a service, its
// PATH does not contain /usr/sbin, and LookPath ignores PATH for an absolute
// candidate. A PATH-only lookup would return "" on every real run and cost
// tx_mbit and freq_mhz while every fixture test still passed.
func TestLookIwFindsAbsoluteCandidateWithoutPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := os.Stat("/usr/sbin/iw"); err != nil {
		assert.Empty(t, lookIw(), "no candidate exists, so nothing may be resolved")
		return
	}
	assert.Equal(t, "/usr/sbin/iw", lookIw())
}

// TestLookIwReturnsEmptyWhenNoCandidateExists covers the last branch: every
// candidate missing has to leave the field empty rather than a stale path from a
// previous lookup.
func TestLookIwReturnsEmptyWhenNoCandidateExists(t *testing.T) {
	saved := iwCandidates
	iwCandidates = []string{"definitely-not-iw", "/nonexistent/iw"}
	t.Cleanup(func() { iwCandidates = saved })
	assert.Empty(t, lookIw())
}

// TestRootIsProductionAgreesWithSourcePath keeps the two rules that decide
// "am I reading this machine or a fixture?" from drifting apart: a root that
// stops re-rooting the procfs path is a root that must not skip the live radio,
// and vice versa.
func TestRootIsProductionAgreesWithSourcePath(t *testing.T) {
	for _, root := range []string{ProdRoot, "", t.TempDir(), "/tmp/fixture", "/sys/class"} {
		rerooted := sourcePath(root, wirelessProcPath)
		assert.Equal(t, rerooted == wirelessProcPath, rootIsProduction(root),
			"root %q: the iw gate and the path re-rooting must agree", root)
	}
	assert.True(t, rootIsProduction(ProdRoot))
	assert.True(t, rootIsProduction(""))
	assert.False(t, rootIsProduction(t.TempDir()))
}

// TestCollectOmitsWiFiWhenTableIsAbsent checks the wiring in Collect: a root
// with no table must leave the section nil rather than an empty struct, because
// an absent key and a key full of zeros mean different things to a consumer.
func TestCollectOmitsWiFiWhenTableIsAbsent(t *testing.T) {
	s, err := Collect(t.TempDir(), nil)
	require.NoError(t, err)
	assert.Nil(t, s.WiFi)
}

// TestCollectIncludesWiFiFromFixtureTable is the section-level counterpart of
// TestCollectWiFiReadsFixtureRoot: the payload has to reach Stats.WiFi.
func TestCollectIncludesWiFiFromFixtureTable(t *testing.T) {
	fakeIw(t, iwLinkLive)
	root := t.TempDir()
	table := sourcePath(root, wirelessProcPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(table), 0o755))
	require.NoError(t, os.WriteFile(table, []byte(wirelessLiveFixture), 0o644))

	s, err := Collect(root, nil)
	require.NoError(t, err)
	require.NotNil(t, s.WiFi)
	assert.Equal(t, -39, s.WiFi.SignalDbm)
	assert.Equal(t, uint64(751), s.WiFi.Retry)
}

// TestCollectWiFiReadsProductionRoot is the only test here that can fail for the
// reason pluscheck exists to catch: a collector can satisfy every fixture and
// still read nothing at runtime, because the fixture root is not the production
// root.
//
// The fixture-root tests re-root /proc/net/wireless under a temp directory, so
// they cannot tell a correct resolution from a plain filepath.Join -- both
// produce the same path once the root is a temp dir, and the two only diverge
// when the root is /sys, where the join yields /sys/proc/net/wireless and finds
// nothing. That divergence is asserted here against the real root, and the skip
// keeps it honest on a host with no wireless table at all rather than turning
// the fixture suite into a hardware requirement.
func TestCollectWiFiReadsProductionRoot(t *testing.T) {
	if _, err := os.Stat(wirelessProcPath); err != nil {
		t.Skipf("%s is not present on this host: nothing to read for real", wirelessProcPath)
	}
	got := collectWiFi(ProdRoot)
	require.NotNil(t, got, "%s exists, so a nil section means the path did not resolve", ProdRoot)
	assert.NotEmpty(t, got.Iface)
	assert.Equal(t, wirelessProcPath, sourcePath(ProdRoot, wirelessProcPath),
		"the production root must not re-root the procfs path")
}

// TestWifiCollectorForksOnlyIw is the other side of the bargain struck in
// TestNvmeCollectorNeverForks: that test allows exactly one file in this package
// to import os/exec, and this one says what the permission is for.
//
// A behavioural fixture cannot see the fork at all -- the radio tool is
// substituted for tests -- so this checks the two things a source check can
// still be honest about: that the file forks something, and that the only
// programs it can name are the radio tool. A smartctl smuggled in here would
// satisfy both assertions, which is why the name is searched for directly as
// well; the point is that the exception is auditable, not that it is airtight.
func TestWifiCollectorForksOnlyIw(t *testing.T) {
	src, err := os.ReadFile("wifi.go")
	require.NoError(t, err)
	text := string(src)

	assert.Contains(t, text, `"os/exec"`, "the permission to fork is for the radio tool")
	assert.NotContains(t, text, "smartctl", "the NVMe parse stays upstream's; iw is the only fork")

	for _, c := range iwCandidates {
		assert.Contains(t, []string{"iw", "/usr/sbin/iw", "/sbin/iw", "/usr/bin/iw"}, c,
			"iwCandidates gained %q, which is a second program this package may run", c)
	}
}
