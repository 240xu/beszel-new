//go:build testing

package hub_test

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/fanapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plus-refusals.ts is the closed set the panel branches on, in a language that cannot import
// Go, so it is a mirror. Nobody checks a mirror and it becomes a second whitelist: a code
// added to fanapi is refused with a reason the panel drops, and a code added to the panel is
// a branch on a refusal fanapi never made.
//
// Neither side is derived from the other — fanapi's is the authority, the TypeScript list was
// written by hand — so this cannot decay into a constant compared with itself. That is what
// pins it: a code corrupted inside fanapi disagrees with a list nobody recomputed.
const refusalMirror = "../site/src/lib/plus-refusals.ts"

var rejectCodeArray = regexp.MustCompile(`(?s)FAN_REJECT_CODES\s*=\s*\[(.*?)\]`)

// An empty parse facing an empty list is the failure this guard exists to prevent.
func mirrorCodes(t *testing.T, source string) []string {
	t.Helper()
	block := rejectCodeArray.FindStringSubmatch(source)
	require.Len(t, block, 2, "FAN_REJECT_CODES is unreadable in the mirror, so the mirror cannot be checked")

	var codes []string
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(stripLineComments(block[1]), -1) {
		codes = append(codes, m[1])
	}
	require.NotEmpty(t, codes, "FAN_REJECT_CODES parsed to nothing, so the mirror cannot be checked")
	return codes
}

func stripLineComments(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(s, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestThePanelMirrorNamesExactlyTheCodesTheGuardPublishes(t *testing.T) {
	source, err := os.ReadFile(refusalMirror)
	require.NoError(t, err, "the mirror must exist for this comparison to mean anything")

	assert.Equal(t, fanapi.Codes(), mirrorCodes(t, string(source)),
		"the panel's closed set and fanapi's disagree; one of them was edited alone")
	assert.Equal(t, slices.Sorted(maps.Keys(fanWireCodes)), slices.Sorted(slices.Values(fanapi.Codes())),
		"the refusal walk reads this set, and only for codes a route emits; a helper that dropped or renamed an entry nothing reaches would be read by nothing else")
}

// Every comparison here is one list against another, so a reader that ignored its input and
// returned a fixed answer would agree with the mirror forever. An edited copy shows it move.
func TestTheMirrorReaderFollowsItsInput(t *testing.T) {
	source, err := os.ReadFile(refusalMirror)
	require.NoError(t, err)

	before := mirrorCodes(t, string(source))
	edited := strings.Replace(string(source), `"curve_rejected"`, `"mutant_only_code"`, 1)
	require.NotEqual(t, string(source), edited, "the edit did not land, so this proves nothing")

	after := mirrorCodes(t, edited)
	assert.NotEqual(t, before, after, "the reader ignored its input; it would agree with any mirror")
	assert.Contains(t, after, "mutant_only_code")
}
