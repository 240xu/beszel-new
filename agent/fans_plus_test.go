//go:build testing

package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fallback must be safe when pfanel has no data yet (fresh install, db
// missing): a nil map is the correct outcome, never a panic.
func TestUpdateFansFallbackIsNilSafe(t *testing.T) {
	a := &Agent{}
	stats := system.Stats{}
	assert.NotPanics(t, func() { a.updateFans(&stats) })
}

// TestUpdateFansReportsOnlyKeysHwmonProduced drives updateFans over a fixture tree and
// pins the keys it publishes to the ones the hwmon walker actually built.
//
// It replaces a test that asserted every key equals the literal "cpu_fan_1". That
// literal was never a key of anything: upstream builds keys as chipName + "_" +
// label, or chipName + "_" + fanN when the chip has no label file, so the real keys
// on this host's class of hardware are nct6798_fan1, pwmfan_fan1 and the like. The
// assertion passed here only because this host exposes no hwmon fan*_input node at
// all, leaving the loop body unexecuted; on any machine that does expose one it fails,
// which makes this a defect that ships with the fork rather than a test that guards
// anything. The name below is not special: it was a guess about one chip.
//
// What the test pins instead is the property the name was reaching for. updateFans
// publishes hwmon's own keys and nothing else, so no sensor it did not read can be
// attributed to this collector. A hardcoded key, or a pfanel estimate wearing a
// hardware name, both fail here regardless of which machine runs it.
func TestUpdateFansReportsOnlyKeysHwmonProduced(t *testing.T) {
	root := t.TempDir()
	// Two chips, deliberately named so that neither is "cpu": a chip whose name the
	// key is derived from, and one that carries a label so the label is exercised.
	writeFile(t, filepath.Join(root, "hwmon0", "name"), "nct6798\n")
	writeFile(t, filepath.Join(root, "hwmon0", "fan1_input"), "5100\n")
	writeFile(t, filepath.Join(root, "hwmon0", "fan2_input"), "0\n")
	writeFile(t, filepath.Join(root, "hwmon0", "fan2_label"), "chassis\n")
	writeFile(t, filepath.Join(root, "hwmon1", "name"), "pwmfan\n")
	writeFile(t, filepath.Join(root, "hwmon1", "fan1_input"), "6500\n")

	// getFanSensors is the same seam upstream tests through newFanSensorCache, and
	// the fanPlusInput doc comment in system.go names it as the seam for its own
	// tests. Restoring it keeps this independent of test order.
	restore := getFanSensors
	getFanSensors = newFanSensorCache(root)
	t.Cleanup(func() { getFanSensors = restore })

	a := &Agent{}
	stats := system.Stats{}
	a.updateFans(&stats)

	assert.Equal(t, map[string]uint16{
		"nct6798_fan1":    5100,
		"nct6798_chassis": 0,
		"pwmfan_fan1":     6500,
	}, stats.Fans,
		"the published map is exactly what the hwmon walker read, keyed by chip name and "+
			"label; a key this collector did not read cannot appear here")
}

// TestUpdateFansOnRealHwmonPublishesOnlyDiscoveredKeys runs the same property against
// this machine's actual hwmon tree rather than a fixture.
//
// The check is membership, not value: a fan that is spinning reports a different RPM
// on the two reads this test makes, so comparing numbers would be a race with the
// hardware. Keys are stable, because they come from file names and the chips' own name
// files.
//
// On a host with no fan nodes both sides are empty and this asserts nothing. That is
// stated rather than hidden, and it is why the fixture test above exists: between them
// the property is checked on every machine, which is the opposite of the state this
// file was in.
func TestUpdateFansOnRealHwmonPublishesOnlyDiscoveredKeys(t *testing.T) {
	if hwmonRoot == "" {
		t.Skip("hwmon is unavailable on this platform, so updateFans cannot publish anything")
	}
	discovered, err := readHwmonFans(hwmonRoot)
	if err != nil {
		t.Skipf("cannot enumerate %s here (%v), so there is no tree to compare against", hwmonRoot, err)
	}

	a := &Agent{}
	stats := system.Stats{}
	a.updateFans(&stats)

	for name := range stats.Fans {
		assert.Containsf(t, discovered, name,
			"%q is not a key this machine's hwmon produced, so updateFans published a "+
				"sensor it never read", name)
	}
}

// ---------------------------------------------------------------------------
// Neither of the tests above decides whether the pfanel estimate belongs on the
// native field. They pin nil-safety and the provenance of the keys, and both hold
// whether or not the estimate is ever published there.
//
// A value assertion cannot decide it. updateFans has no injection point and
// hwmonRoot is a build-tag constant, so there is no fixture in which the field is
// observably full on a machine whose hardware does not already fill it. The
// invariant is therefore structural -- no code path may exist that could fill it.
// ---------------------------------------------------------------------------

// nativeFansRef matches any mention of the native field by selector.
//
// nativeFansEscape matches the forms that are a write whatever the file's status:
// an assignment, an assignment through one of the map's keys, and taking the field's
// address. == and != are excluded, so a comparison is not a write.
var (
	nativeFansRef    = regexp.MustCompile(`\.Fans\b`)
	nativeFansEscape = regexp.MustCompile(`(?:\.Fans\b[^=]*=[^=]|&[^\s(]*\.Fans\b)`)
)

// nativeFansReaders names the files outside fans.go that are permitted to READ the
// native map. It is empty, and should stay empty: package agent produces this map
// inside fans.go and has no consumer for it here -- the hub reads it from
// internal/records, and the hub is not this package.
//
// The list exists because a blanket ban is a bad thing to leave unguarded. It fails
// red on a future upstream commit that merely READS the field, which is friction the
// fork would otherwise impose on every rebase. Putting a file here makes that a
// decision recorded in one line, with a justification, rather than a rule rewrite or
// a deletion. It is not a licence: a write or an address-of in a listed file is still
// fatal, and a bare pass-on -- handing the map to a function, which carries no '='
// and no '&' and is indistinguishable from a read here -- is a limit of this scan,
// not something the entry waives.
var nativeFansReaders = map[string]bool{}

// TestNativeFansIsNeverWrittenFromPfanel states the invariant in both forms it
// could be broken in. Producers: fans.go alone may name Stats.Fans, so a fallback
// added to system.go fails as loudly as one added to fans.go -- including the
// rewrite that avoids the word "write" by taking &systemStats.Fans and passing it
// on. Shape: fans.go may not reach for agent/plus or pfanel at all, so the estimate
// cannot be projected in behind a helper.
//
// The rule is a name ban rather than an assignment ban, and that is a trade worth
// stating. The field is a map header, so assignment, update, address-of and
// pass-on are all the same act, and a text scan cannot tell the last two from
// `len(stats.Fans)`. Keying on '=' therefore catches only the form someone writes
// when they mean to write: an earlier version of this guard did exactly that, and
// the pointer form walked straight through it. Banning the name closes all of them;
// the price is nativeFansReaders, which is empty today.
func TestNativeFansIsNeverWrittenFromPfanel(t *testing.T) {
	files := packageSources(t)
	require.Contains(t, files, "fans.go",
		"the scan found no fans.go, so it proved nothing")

	for path, src := range files {
		if path == "fans.go" {
			continue
		}
		reader := nativeFansReaders[path]
		for i, line := range strings.Split(src, "\n") {
			hit := nativeFansRef.MatchString(line)
			if !hit {
				continue
			}
			if reader && !nativeFansEscape.MatchString(line) {
				continue
			}
			assert.Failf(t, "unexpected reference to the native Fans field",
				"%s:%d names Stats.Fans: %s\n"+
					"Only fans.go, which reads the tachometers, may write it, and "+
					"nativeFansReaders currently lists %v, so naming it here fails.\n"+
					"That field is map[string]uint16 and carries no marker, so a key "+
					"there is indistinguishable downstream from a tachometer reading, "+
					"and internal/records averages it into the hub's permanent f column.\n"+
					"An assignment, an update, an address-of and a bare pass-on are all "+
					"the same act, because what is being handled is a map header. If "+
					"this file genuinely only READS the field, add it to "+
					"nativeFansReaders with a line saying why -- a write there is still "+
					"rejected.\n"+
					"The estimate belongs on Stats.Plus.Fan, which carries "+
					"rpm_est_is_estimate and sample_ts_s.", path, i+1,
				strings.TrimSpace(line), nativeFansReaders)
		}
	}

	fans := files["fans.go"]
	for _, banned := range []string{"agent/plus", "pfanel", "FanRPMEst", "plus.", "pfanel."} {
		assert.NotContainsf(t, fans, banned,
			"agent/fans.go must not reference %q: the native field cannot say a "+
				"number is an estimate, so any projection into it launders one", banned)
	}
}

// packageSources maps this package's shipped file names to their contents.
// Test files are left out because they are not shipped; subdirectories are other
// packages, and agent/plus is covered by the second half of the guard above.
func packageSources(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		require.NoErrorf(t, err, "cannot read %s", p)
		out[p] = string(b)
	}
	require.NotEmpty(t, out, "the scan must have read the package's own sources")
	return out
}
