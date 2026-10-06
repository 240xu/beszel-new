package plus

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// wirelessProcPath is the kernel table this collector reads. It is absolute
// procfs, not a sysfs subpath, so it goes through sourcePath like the fan
// database does: a plain join onto the root would turn it into
// /sys/proc/net/wireless under the production root and read nothing there.
const wirelessProcPath = "/proc/net/wireless"

// dataRowRe matches one data line of /proc/net/wireless.
//
// The layout is not a convention, it is a hardcoded format string in
// net/wireless/wext-proc.c (wext-procfs.c before v3.10), verified byte-identical
// from v3.10 through v6.8:
//
//	seq_printf(seq, "%6s: %04x  %3d%c  %3d%c  %3d%c  %6d %6d %6d %6d %6d   %6d\n",
//		   dev->name, stats->status,
//		   stats->qual.qual,
//		   stats->qual.updated & IW_QUAL_QUAL_UPDATED ? '.' : ' ',
//		   ((__s32) stats->qual.level) -
//		   (stats->qual.updated & IW_QUAL_DBM ? 0x100 : 0),
//		   stats->qual.updated & IW_QUAL_LEVEL_UPDATED ? '.' : ' ',
//		   ((__s32) stats->qual.noise) -
//		   (stats->qual.updated & IW_QUAL_DBM ? 0x100 : 0),
//		   stats->qual.updated & IW_QUAL_NOISE_UPDATED ? '.' : ' ',
//		   stats->discard.nwid, stats->discard.code,
//		   stats->discard.fragment, stats->discard.retries,
//		   stats->discard.misc, stats->miss.beacon);
//
// so there are exactly eleven columns and they never move:
//
//	[0] iface  [1] status  [2] link  [3] level  [4] noise
//	[5] nwid   [6] crypt   [7] frag  [8] retry  [9] misc  [10] beacon
//
// Two properties of that format string decide how the columns may be matched.
//
// The three quality columns are %3d followed by a MARKER CHARACTER that is '.'
// only when the driver flagged that field updated on this read and ' ' when it
// did not. Each is therefore matched as a digit run followed by a single [. ].
// Requiring the dot -- which is what a reading of this machine's own table
// suggests, since iwlwifi/mac80211 sets the flags and prints " 70.  -39. -256" --
// rejects every driver that leaves the flags clear. Forbidding it rejects every
// driver that sets them, which is the mistake the -256 on this NAS invites:
// noise is bare here because IW_QUAL_NOISE_UPDATED happens to be clear, not
// because the kernel never prints a marker there.
//
// Group 5 is retry, the 4th of the five discard counters (nwid, crypt, frag,
// retry, misc); one column to the right is misc, and they are unrelated
// quantities. The tail is pinned with \s*$ so a row whose column count differs
// is refused outright rather than half-parsed into a plausible wrong number.
var dataRowRe = regexp.MustCompile(
	`^\s*(\S+):\s+\S+\s+(-?\d+)[. ]\s+(-?\d+)[. ]\s+(-?\d+)[. ]\s+\d+\s+\d+\s+\d+\s+(\d+)\s+\d+\s+\d+\s*$`)

var (
	// txBitrateRe and freqRe are anchored to the start of a line, because both
	// names appear as a substring of some other line iw can print.
	//
	// "tx bitrate:" sits directly below "rx bitrate:", which carries a
	// different rate and which iw prints FIRST -- on this NAS rx is 1560.0
	// against tx 866.7 at the same instant, so a loose pattern does not
	// under-report by a rounding error, it reports the other direction's number.
	//
	// "freq:" sits at the end of "center freq:", which iw prints before it for a
	// 2.4 GHz association carrying a channel hint, and centre differs from the
	// negotiated frequency on any HT40/160 MHz link. Anchoring is cheaper than
	// being certain about every spelling iw has, because the failure it prevents
	// is a wrong number while the failure it can cause -- an unfamiliar spelling
	// -- is an absent one, and an absent freq_mhz is visible as such.
	//
	// The leading [ \t]* covers iw's indentation; nothing else about the line is
	// assumed, since iw's source was not available to check.
	txBitrateRe = regexp.MustCompile(`(?m)^[ \t]*tx bitrate:[ \t]*([0-9.]+)[ \t]*MBit/s`)
	freqRe      = regexp.MustCompile(`(?m)^[ \t]*freq:[ \t]*([0-9.]+)`)
)

// iwCandidates is where the iw tool is looked for.
//
// The agent runs as a service with a minimal environment, and on this NAS iw is
// /usr/sbin/iw -- outside the default PATH. A bare LookPath("iw") would miss it
// on every production run while every fixture test still passed, and the cost
// would be tx_mbit and freq_mhz silently absent forever.
var iwCandidates = []string{"iw", "/usr/sbin/iw", "/sbin/iw", "/usr/bin/iw"}

// collectWiFi reports the radio state of the first wireless device the kernel
// lists. root is the sysfs mount point, so the table is resolved through
// sourcePath rather than joined onto it.
//
// The iw lookup runs only for the production root. An injected root means "read
// this tree", and the host's radio is not part of it: without the gate every
// fixture assertion about tx_mbit would be answered by whichever access point
// this machine was joined to at that moment.
//
// Degradation is one-directional. No table, or a table with no device, yields
// nil and the section is absent. A table without iw still yields signal_dbm and
// retry. Anything a consumer reads here is measured on this machine at read
// time; nothing is differenced, and Retry is the driver's cumulative retries
// counter, so a consumer wanting a retry RATE must take its own delta.
func collectWiFi(root string) *WiFiStats {
	b, err := os.ReadFile(sourcePath(root, wirelessProcPath))
	if err != nil {
		return nil
	}
	s := parseWireless(string(b))
	if s == nil {
		return nil
	}
	if rootIsProduction(root) {
		applyIwLink(s, iwLinkOutput(s.Iface))
	}
	return s
}

// parseWireless returns the first data row of the table, or nil when there is
// none it can read.
//
// SignalDbm is the kernel's printed level column. That column is a dBm value
// only because the driver sets IW_QUAL_DBM, which the table does not report --
// the flag shifts the stored number by 0x100 and the printed column is already
// the offset value either way. A driver that leaves the flag clear publishes a
// relative quality number under the same wire name, so signal_dbm is dBm by
// driver convention rather than by this package's guarantee.
func parseWireless(content string) *WiFiStats {
	for _, line := range strings.Split(content, "\n") {
		m := dataRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		sig, _ := strconv.Atoi(m[3])
		retry, _ := strconv.ParseUint(m[5], 10, 64)
		return &WiFiStats{Iface: m[1], SignalDbm: sig, Retry: retry}
	}
	return nil
}

// wirelessLayoutUnrecognised reports a table that holds a device row this build
// refused to parse, which is different from holding no device at all.
//
// The column count is fixed by a format string, so this should never fire, and
// that is the point: the failure it reports is a layout that mainline no longer
// produces, a patched vendor kernel that widened the format, or a bug in this
// parser. Every one of those otherwise looks exactly like "this NAS has no
// wireless", because a parser that matches nothing returns nil either way.
// Refusing is the safe behaviour; refusing silently is the failure.
func wirelessLayoutUnrecognised(content string) bool {
	if parseWireless(content) != nil {
		return false
	}
	return hasWirelessDeviceRow(content)
}

// hasWirelessDeviceRow looks for anything shaped like a data row WITHOUT
// committing to the eleven-column layout, which is the only way a foreign layout
// can still be noticed.
//
// Five tokens is the floor the kernel's own format can produce: interface,
// status and the three quality columns, with every counter optional. The
// threshold stays below any plausible layout on purpose -- it is here to notice
// a table this parser cannot read, and a count that tracks the current layout
// would stop noticing the moment the layout changed, which is the one case that
// matters.
//
// The kernel's two header lines are ruled out by requiring a first token ending
// in ':' -- "Inter-|" and "face" end in '|' -- not by counting tokens. Counting
// would be the wrong tool for exactly the case that matters here: the legend
// line splits into 17 tokens, more than a data row's 11, so a token-count test
// is the thing most likely to admit it.
func hasWirelessDeviceRow(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && strings.HasSuffix(f[0], ":") {
			return true
		}
	}
	return false
}

// applyIwLink fills the two radio attributes iw knows about. A missing or
// unparsable line leaves the field at its zero fill, which is what an
// unassociated interface produces: iw prints "Not connected." and exits zero,
// so there is no error to distinguish from a parse miss.
func applyIwLink(s *WiFiStats, out string) {
	if m := txBitrateRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			s.TxMBit = v
		}
	}
	if m := freqRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			s.FreqMHz = int(v)
		}
	}
}

// iwLinkOutput asks iw for one interface's link and returns its stdout.
//
// Errors collapse to an empty string on purpose: an interface that has just
// gone down, an iw that is not installed and an iw that timed out all cost the
// same two fields, and reporting any of them differently would mean inventing a
// value. The timeout keeps a wedged netlink query from stalling the whole
// collection pass.
func iwLinkOutput(iface string) string {
	bin := lookIw()
	if bin == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "dev", iface, "link").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// lookIw resolves the first usable iw candidate, or "" when none exists.
func lookIw() string {
	for _, c := range iwCandidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// rootIsProduction reports whether root names this machine rather than a fixture
// tree. It is the rule sourcePath applies when it declines to re-root an
// absolute path, stated separately so a test can hold the two to each other
// rather than leaving them as two copies of one fact.
//
// It cannot be written in terms of sourcePath and still be worth testing: the
// agreement TestRootIsProductionAgreesWithSourcePath asserts would then be an
// identity, so any future edit to either rule alone would pass silently.
func rootIsProduction(root string) bool {
	return root == "" || root == ProdRoot
}
