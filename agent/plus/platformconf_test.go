//go:build testing

package plus

import (
	"fmt"
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
		// A relative path still ends in the right leaf, so only the "must be a
		// full path" clause can catch it.
		"sys/class/thermal/thermal_zone9/temp",
		// Absolute and correctly named, but too shallow to be a zone file.
		"/temp",
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

// ECConf is the layer that makes the four keys mean something. Until it existed
// they were parsed, validated, covered by tests, and then dropped on the floor:
// an operator who edited FAN_DEVICE saw the file change and the agent keep
// writing to the chassis's hard-coded path. These assert the values survive the
// trip from the file text into the struct the fan code reads.

// TestECConfCarriesTheFourPreviouslyDeadKeys pins the derivation itself, from
// file text to resolved values -- not a round trip through a struct field set by
// the test, which would pass even if the derivation were deleted.
func TestECConfCarriesTheFourPreviouslyDeadKeys(t *testing.T) {
	src := "EC_ROOT=\\_SB.SOMETHING.EC0\n" +
		"FAN_DEVICE=\\_SB.SOMETHING.EC0.TFNX\n" +
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone2/temp\n" +
		"DEV=/dev/otherfan\n" +
		"EC_CMD=0x42\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)

	conf, err := p.EC()
	require.NoError(t, err)

	assert.Equal(t, `\_SB.SOMETHING.EC0`, conf.ECRoot,
		"EC_ROOT decides where EC bytes are read from")
	assert.Equal(t, `\_SB.SOMETHING.EC0.TFNX`, conf.FANDevice,
		"FAN_DEVICE decides where the fan methods are called")
	assert.Equal(t, "/dev/otherfan", conf.Dev,
		"DEV decides which node the expression is written to")
	assert.Equal(t, uint64(0x42), conf.Cmd,
		"EC_CMD decides the command byte sent with every EC register access")
}

// TestECConfDecodesTheCommandByte is here because EC_CMD is the one key whose
// consumer needs a number. A parser that handed back the "0xB5" text would push
// the conversion down into the write path, where a typo becomes a command sent
// to an embedded controller.
func TestECConfDecodesTheCommandByte(t *testing.T) {
	for _, tc := range []struct {
		text string
		want uint64
	}{
		{"0xB5", 0xB5},
		{"0xb5", 0xB5},
		{"0xFF", 0xFF},
		{"0x0", 0},
	} {
		p, err := ParsePlatformConf(setConfKey(golden(t, "platform.conf"), "EC_CMD", tc.text))
		require.NoError(t, err, "EC_CMD=%s must parse", tc.text)
		conf, err := p.EC()
		require.NoError(t, err)
		assert.Equal(t, tc.want, conf.Cmd, "EC_CMD=%s", tc.text)
	}
}

// TestECConfDefaultsToACPIWhenNoBackendIsNamed. Absent means the chassis as it
// was built, not "pick whatever is mounted": an absent key and a probe that
// guesses are different contracts, and the guessing one is the one that can
// reach the wrong controller.
func TestECConfDefaultsToACPIWhenNoBackendIsNamed(t *testing.T) {
	p, err := ParsePlatformConf(golden(t, "platform.conf"))
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)
	assert.Equal(t, ECBackendACPI, conf.Backend)
	assert.Empty(t, conf.IO, "the ACPI backend addresses its object through EC_ROOT, not a node")
}

// TestECConfRefusesAnUnknownBackend. Falling back to ACPI here would turn a
// typo into a working-looking system pointed somewhere other than asked.
func TestECConfRefusesAnUnknownBackend(t *testing.T) {
	for _, backend := range []string{"", "ACPIs", "ecs", "ec-sys", "/dev/null", "auto"} {
		src := golden(t, "platform.conf") + "\nEC_BACKEND=" + backend + "\n"
		p, err := ParsePlatformConf(src)
		require.NoError(t, err, "the key itself is optional and must still parse")
		_, err = p.EC()
		require.Errorf(t, err, "EC_BACKEND=%q must be refused rather than defaulted", backend)
		assert.ErrorIs(t, err, ErrECBackendUnknown)
	}
}

// TestECConfRefusesACandidateBackend is the auto-detection ban, made executable.
// A backend that is discovered by looking at what is mounted cannot be reviewed
// by anyone, and on a machine with two controllers it picks one at random.
func TestECConfRefusesACandidateBackend(t *testing.T) {
	src := golden(t, "platform.conf") + "\nEC_BACKEND=auto\nEC_IO=/sys/kernel/debug/ec/ec0/io\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	_, err = p.EC()
	require.ErrorIs(t, err, ErrECBackendUnknown)
	assert.Contains(t, err.Error(), "auto",
		"the message has to show the operator what was rejected")
}

// TestECConfECSysNamesItsOwnNode. There is no ec0 by default on this platform,
// and which index a kernel builds is machine state, not a fact about the
// chassis. Guessing one is the exact failure the brief is about.
func TestECConfECSysNamesItsOwnNode(t *testing.T) {
	// Absent, and present-but-blank, are both an operator who named the backend
	// and then did not say which node. Neither may fall back to a guess.
	for _, key := range []string{"", "\nEC_IO=\n", "\nEC_IO=   \n"} {
		src := golden(t, "platform.conf") + "\nEC_BACKEND=ec_sys" + key
		p, err := ParsePlatformConf(src)
		require.NoError(t, err)
		_, err = p.EC()
		require.ErrorIsf(t, err, ErrECIONodeRequired, "with EC_IO spelled %q", key)
		assert.Contains(t, err.Error(), "EC_IO",
			"the refusal has to name the key that would fix it")
	}
}

// TestECConfECSysAcceptsAKernelECNode is the accept side, and it is checked
// against real nodes rather than a pattern that would also accept junk.
func TestECConfECSysAcceptsAKernelECNode(t *testing.T) {
	for _, node := range []string{
		"/sys/kernel/debug/ec/ec0/io",
		"/sys/kernel/debug/ec/ec1/io",
		"/sys/kernel/debug/ec/ec12/io",
	} {
		src := golden(t, "platform.conf") + "\nEC_BACKEND=ec_sys\nEC_IO=" + node + "\n"
		p, err := ParsePlatformConf(src)
		require.NoError(t, err)
		conf, err := p.EC()
		require.NoError(t, err, "%s is a kernel EC node", node)
		assert.Equal(t, ECBackendECSys, conf.Backend)
		assert.Equal(t, node, conf.IO)
	}
}

// TestECConfECSysNodeMustStayInsideTheKernelECDirectory. EC_IO names a physical
// controller, so anything that is not exactly one ec<N> handle must be refused
// rather than opened: the cost of being wrong here is a fan instruction aimed
// at the wrong place, and the file is only ever read, never guessed.
//
// Surrounding whitespace is absent from this list on purpose: ParsePlatformConf
// strips it, so " EC_IO=<node> " names the node either way and no padding can
// smuggle a different path past the shape check.
func TestECConfECSysNodeMustStayInsideTheKernelECDirectory(t *testing.T) {
	for _, node := range []string{
		"ec0/io",
		"/sys/kernel/debug/ec/io",
		"/sys/kernel/debug/ec/ec0",
		"/sys/kernel/debug/ec/ec0/io/extra",
		"/sys/kernel/debug/ec/ecx/io",
		"/sys/kernel/debug/ec/ec0/io/../ec1/io",
		"/dev/mem",
		"/proc/kcore",
		"/sys/kernel/debug/ec/ec0/../ec1/io",
		"/sys/kernel/debug/ec/ec-1/io",
		"/etc/pfan/platform.conf",
	} {
		src := golden(t, "platform.conf") + "\nEC_BACKEND=ec_sys\nEC_IO=" + node + "\n"
		p, err := ParsePlatformConf(src)
		require.NoError(t, err)
		_, err = p.EC()
		assert.ErrorIsf(t, err, ErrECIOInvalid, "EC_IO=%q must be refused", node)
	}
}

// TestECConfResolvesLastAssignmentWins records what a newline inside a value
// actually does, because "can EC_IO smuggle something" is the wrong question to
// ask about it. The file is read line by line, so an embedded newline closes the
// assignment and starts a new, separately validated one. Nothing a value can
// carry gets past the shape check as a side effect of another key; the worst
// case is the operator's own typo producing a different -- still valid -- file.
func TestECConfResolvesLastAssignmentWins(t *testing.T) {
	src := golden(t, "platform.conf") +
		"\nEC_BACKEND=ec_sys\nEC_IO=/sys/kernel/debug/ec/ec0/io\nEC_BACKEND=acpi\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err, "the second assignment is a complete, valid file")
	assert.Equal(t, ECBackendACPI, conf.Backend,
		"a later line replaces an earlier one, and the later one is the operator's last word")
}

// TestECConfIgnoresECIOWhileOnACPI. The key is harmless dead weight on the
// chassis's own backend, and refusing it would make the two spellings of "not
// using ec_sys" behave differently for no reason an operator could see.
func TestECConfIgnoresECIOWhileOnACPI(t *testing.T) {
	src := golden(t, "platform.conf") + "\nEC_IO=/sys/kernel/debug/ec/ec0/io\n"
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)
	assert.Equal(t, ECBackendACPI, conf.Backend)
}

// The misconnection guard. EC_ROOT and FAN_DEVICE are independent keys because
// real machines differ, so nothing may derive one from the other. The price of
// that independence is that a copy-paste slip can pair two different
// controllers, and this is the check that catches it.

// TestECConfAcceptsAFanHangingOffItsOwnEC is the normal shape: the fan device
// sits under the EC root, so the two keys provably name one controller.
func TestECConfAcceptsAFanHangingOffItsOwnEC(t *testing.T) {
	p, err := ParsePlatformConf(golden(t, "platform.conf"))
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)
	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	assert.NoError(t, conf.ValidatePairing(cands))
	assert.False(t, conf.NeedsCandidates(),
		"the shipped pairing must not make production depend on a second file")
}

// TestECConfAcceptsTheDocumentedOffSubtreePairing. The host ships a candidate
// list precisely because machines exist whose fan device is not under EC0, so a
// pairing outside the subtree is legitimate when the host lists it.
func TestECConfAcceptsTheDocumentedOffSubtreePairing(t *testing.T) {
	src := setConfKey(golden(t, "platform.conf"), "EC_ROOT", `\_SB.PC00.LPCB.EC0`)
	src = setConfKey(src, "FAN_DEVICE", `\_SB.IETM.TFN1`)
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)
	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	assert.True(t, conf.NeedsCandidates(),
		"a caller has to know this pairing will consult the list before reading it")
	assert.NoError(t, conf.ValidatePairing(cands))
}

// TestECConfRefusesTwoDifferentControllers is the failure the guard exists for.
// EC88 reads a byte from EC_ROOT while _FSL programs FAN_DEVICE; if those name
// different controllers the agent would read one machine's duty and write
// another's -- with no error anywhere in between.
func TestECConfRefusesTwoDifferentControllers(t *testing.T) {
	// The slip a human actually makes: root updated, fan device not.
	src := setConfKey(golden(t, "platform.conf"), "EC_ROOT", `\_SB.PC00.LPCB.EC0`)
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)

	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	err = conf.ValidatePairing(cands)
	require.ErrorIs(t, err, ErrECPairMismatch)
	assert.Contains(t, err.Error(), `\_SB.PC00.LPCB.EC0`,
		"the refusal must show the root that was configured")
	assert.Contains(t, err.Error(), `\_SB.PCI0.LPCB.EC0.TFN1`,
		"the refusal must show the fan device it disagrees with")
}

// TestECConfRefusesAPairingNoCandidateBacks is the closed-world half: outside
// the subtree and absent from the list, there is no evidence the pairing is a
// real machine rather than a typo. Silence is not consent.
func TestECConfRefusesAPairingNoCandidateBacks(t *testing.T) {
	src := setConfKey(golden(t, "platform.conf"), "FAN_DEVICE", `\_SB.SOMETHINGELSE.TFN9`)
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)

	cands, err := ParseFanCandidates(golden(t, "fan-candidates.txt"))
	require.NoError(t, err)
	assert.ErrorIs(t, conf.ValidatePairing(cands), ErrECPairMismatch)
	assert.ErrorIs(t, conf.ValidatePairing(nil), ErrECPairMismatch,
		"an unreadable candidate list cannot be read as approval")
}

// TestECConfRefusesTheFanDeviceAsItsOwnRoot. EC88 under TFN1 answers
// AE_NOT_FOUND on this chassis, so naming the same object for both keys is a
// configuration that cannot work even though neither value is malformed.
func TestECConfRefusesTheFanDeviceAsItsOwnRoot(t *testing.T) {
	src := setConfKey(golden(t, "platform.conf"), "EC_ROOT", `\_SB.PCI0.LPCB.EC0.TFN1`)
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)
	assert.ErrorIs(t, conf.ValidatePairing(nil), ErrECPairMismatch)
}

// TestECConfErrorsAreDistinct. A switch on the reason is how a caller decides
// between "fix your config" and "this chassis has no such EC", and two reasons
// sharing a value collapse into one.
func TestECConfErrorsAreDistinct(t *testing.T) {
	all := []error{
		ErrECBackendUnknown,
		ErrECIONodeRequired,
		ErrECIOInvalid,
		ErrECPairMismatch,
	}
	require.Len(t, all, 4)
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			assert.Falsef(t, a == b, "reasons %d and %d must not share a value", i, j)
		}
	}
	for _, reason := range all {
		wrapped := fmt.Errorf("resolve EC wiring: %w", reason)
		assert.ErrorIs(t, wrapped, reason, "a reason that stops being findable once wrapped is not a reason")
	}
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
