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

func TestUpdateFansOnlyEverReportsOurOwnSensor(t *testing.T) {
	a := &Agent{}
	stats := system.Stats{}
	a.updateFans(&stats)
	// Either nil (pfanel unavailable) or populated; both valid, but if present
	// the key must be ours rather than an unrelated hwmon fan.
	for name := range stats.Fans {
		assert.Equal(t, "cpu_fan_1", name)
	}
}

// ---------------------------------------------------------------------------
// Both tests above pass whether or not the estimate is published through the
// native field: on this platform updateFans finds no hwmon fan*_input node, so
// it returns before writing anything. They pin nil-safety, not the decision.
//
// The decision needs a guard that goes red when the fallback comes back, and a
// value assertion cannot supply one: updateFans has no injection point and
// hwmonRoot is a build-tag constant, so there is no fixture in which the field
// is observably full. The invariant is therefore structural -- no code path may
// exist that could fill it.
// ---------------------------------------------------------------------------

// fansWrite matches an assignment to the native field, through the field itself
// or through one of its keys. == is excluded so a comparison is not a write.
var fansWrite = regexp.MustCompile(`\.Fans\s*(\[[^\]]*\])?\s*=[^=]`)

// TestNativeFansIsNeverWrittenFromPfanel states the invariant in both forms it
// could be broken in. Producers: nothing in package agent may assign
// Stats.Fans outside fans.go, so a fallback added to system.go fails as loudly
// as one added to fans.go. Shape: fans.go may not reach for agent/plus or
// pfanel at all, so the estimate cannot be projected in behind a helper.
func TestNativeFansIsNeverWrittenFromPfanel(t *testing.T) {
	files := packageSources(t)
	require.Contains(t, files, "fans.go",
		"the scan found no fans.go, so it proved nothing")

	for path, src := range files {
		if path == "fans.go" {
			continue
		}
		for i, line := range strings.Split(src, "\n") {
			if !fansWrite.MatchString(line) {
				continue
			}
			assert.Failf(t, "unexpected write to the native Fans field",
				"%s:%d writes Stats.Fans: %s\n"+
					"That field is map[string]uint16 and carries no marker, so a key is "+
					"indistinguishable downstream from a tachometer reading, and "+
					"internal/records averages it into the hub's permanent f column. The "+
					"estimate belongs on Stats.Plus.Fan, which carries rpm_est_is_estimate "+
					"and sample_ts_s.", path, i+1, strings.TrimSpace(line))
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
