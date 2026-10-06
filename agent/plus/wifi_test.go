//go:build testing

package plus

// Cross-platform contract tests for the WiFi wire shape. No Linux-only symbol may
// appear here: it would pass every test on this host, then break the GOOS=windows
// vet. platform_scope_test.go holds the split.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWiFiZeroValuesAreOmitted pins the presence contract where Task 17 reads it:
// the three keys must be absent rather than zero, since that zero is the absence of
// a reading, not a measurement.
func TestWiFiZeroValuesAreOmitted(t *testing.T) {
	t.Run("a complete payload carries all five keys", func(t *testing.T) {
		b, err := json.Marshal(&WiFiStats{
			SignalDbm: -42, TxMBit: 780, FreqMHz: 5180, Retry: 1146, Iface: "wlp0s20f3",
		})
		require.NoError(t, err)
		assert.JSONEq(t,
			`{"signal_dbm":-42,"tx_mbit":780,"freq_mhz":5180,"retry":1146,"iface":"wlp0s20f3"}`,
			string(b))
	})

	t.Run("association without station statistics carries two", func(t *testing.T) {
		b, err := json.Marshal(&WiFiStats{FreqMHz: 5180, Iface: "wlp0s20f3"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"freq_mhz":5180,"iface":"wlp0s20f3"}`, string(b),
			"iface and freq_mhz are the proof of association, so they survive")
		assert.NotContains(t, string(b), `"signal_dbm":0`, "an unread RSSI must not reach the wire as a zero")
		assert.NotContains(t, string(b), `"tx_mbit":0`, "an unread bitrate must not reach the wire as a zero")
		assert.NotContains(t, string(b), `"retry":0`, "an unread counter must not reach the wire as a zero")
	})

	t.Run("a value of zero sends no key at all", func(t *testing.T) {
		b, err := json.Marshal(&WiFiStats{})
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(b),
			"every field carries omitempty, as TestNvmeZeroValuesAreOmitted requires of NvmeStats")
	})
}

// /proc/net/wireless marker bytes -- characterisation, not production coverage.
// Task 7's first implementation read this file and ran iw; both are gone, and this
// is why dropping them was safe. Each quality column is %3d plus a marker byte, '.'
// for a set IW_QUAL_*_UPDATED bit and ' ' for a clear one, confirmed with cat -A:
//
//	wlp0s20f3: 0000   70.  -37.  -256        0      0      0   1187      4        0
//	                        ^^  ^^  ^^^ space
//
// Markers vary per driver and poll, so accept both: (-?\d+)[. ]. Not [\s.] (\s eats
// the row separator), not an unescaped dot.

func wirelessRow(link, level, noise string) string {
	return wirelessRowNamed("wlp0s20f3", link, level, noise)
}

func wirelessRowNamed(name, link, level, noise string) string {
	return name + ": 0000   " + link + "  " + level + "  " + noise +
		"        0      0      0   1187      4        0"
}

const wirelessQuality = `(-?\d+)[. ]`

func wirelessColumns(pat, row string) []int {
	matches := regexp.MustCompile(pat).FindAllStringSubmatch(row, -1)
	cols := make([]int, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		cols = append(cols, v)
	}
	return cols
}

// wifiRetryIndex is where retry sits in the extracted run -- status, three quality
// columns, then nwid, crypt, frag, retry, misc -- and the fixture puts 1187 there.
const wifiRetryIndex = 7

func wirelessMultiLine() string {
	return wirelessRowNamed("wlp0s20f3", "70.", "-37.", "-256 ") + "\n" +
		wirelessRowNamed("wlp1s20f3", "72.", "-41.", "-256 ")
}

func TestWirelessQualityAcceptsBothMarkerKinds(t *testing.T) {
	for name, row := range map[string]string{
		"as this host prints it":       wirelessRow("70.", "-37.", "-256 "),
		"no column marked":             wirelessRow("70 ", "-37 ", "-256 "),
		"only the noise column marked": wirelessRow("70 ", "-37 ", "-256."),
		"all three columns marked":     wirelessRow("70.", "-37.", "-256."),
		"wide noise, marked":           wirelessRow("70.", "-37.", "-256000."),
		"wide noise, unmarked":         wirelessRow("70.", "-37.", "-256000 "),
	} {
		t.Run(name, func(t *testing.T) {
			cols := wirelessColumns(wirelessQuality, row)
			require.Len(t, cols, 9, "status, three quality columns, then five counters")
			assert.Equal(t, 1187, cols[wifiRetryIndex], "retry must not shift with the marker kind")
			assert.Equal(t, 4, cols[wifiRetryIndex+1], "misc must not shift with the marker kind")
		})
	}
}

// TestWirelessQualityMultiLine separates [. ] from [\s.], which one row cannot: the
// two agree there. \s matches the newline ending a row, so the variant latches
// row one's trailing counter onto the next row and extracts a column the kernel
// never printed. Later rows shift by one, and retry then reads nwid on 1187.
func TestWirelessQualityMultiLine(t *testing.T) {
	cols := wirelessColumns(wirelessQuality, wirelessMultiLine())
	require.Len(t, cols, 18, "two rows of nine columns each")
	assert.Equal(t, []int{0, 70, -37, -256, 0, 0, 0, 1187, 4}, cols[:9], "row one")
	assert.Equal(t, []int{1187, 4}, cols[9+wifiRetryIndex:], "row two retry and misc stay put")

	loose := wirelessColumns(`(-?\d+)[\s.]`, wirelessMultiLine())
	assert.Len(t, loose, 19, "one invented column from the row separator")
	assert.Equal(t, 1187, loose[9+wifiRetryIndex+1], "row two retry moves one position right")
	assert.Equal(t, 0, loose[9+wifiRetryIndex],
		"so the index the correct pattern reads 1187 at returns nwid instead: a plausible zero, "+
			"on a link that has never stopped retrying, with nothing to notice it")
}

// TestWirelessQualityWildcardDot measures the mutation the pattern exists to
// prevent. An unescaped dot does not truncate a long value -- the digit run is
// greedy -- but it matches inside the interface name, so wlp0s20f3 adds three
// spurious columns and every index after them shifts.
func TestWirelessQualityWildcardDot(t *testing.T) {
	row := wirelessRow("70.", "-37.", "-256000.")

	correct := wirelessColumns(wirelessQuality, row)
	require.Len(t, correct, 9)
	assert.Equal(t, -256000, correct[3], "a six-digit noise column survives intact")
	assert.Equal(t, 1187, correct[wifiRetryIndex], "retry")

	wildcard := wirelessColumns(`(-?\d+).`, row)
	assert.Len(t, wildcard, 12,
		"an unescaped dot is a wildcard, so it also matches digits inside the interface name")
	assert.Equal(t, []int{0, 20, 3}, wildcard[:3],
		"three spurious columns from wlp0s20f3, which is what shifts every index after them")
	assert.Equal(t, -256000, wildcard[6],
		"the noise value, three positions right of where the correct pattern puts it")
	assert.Equal(t, 1187, wildcard[10], "and retry likewise shifted by three")
	assert.Equal(t, 0, wildcard[wifiRetryIndex],
		"the consequence: retry read at the index the correct pattern uses returns nwid, which "+
			"on a healthy link is 0 -- indistinguishable from a legitimately zero retry, and "+
			"nothing in the code reports a problem")

	// A non-zero frag shows the shift landing on a plausible wrong field.
	varred := "wlp0s20f3: 0000   70.  -37.  -256000.    0      0      7   1187      4        0"
	require.Equal(t, 1187, wirelessColumns(wirelessQuality, varred)[wifiRetryIndex])
	assert.Equal(t, 7, wirelessColumns(`(-?\d+).`, varred)[wifiRetryIndex+2],
		"under the wildcard the same read returns frag, not retry")
}
