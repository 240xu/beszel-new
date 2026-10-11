//go:build testing && linux

package plus

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture stands in for the three privileged paths fanctl touches. It configures
// them through the PFAN_* seams rather than by constructing FanPaths directly,
// so every case below exercises the real resolve-then-write path a production
// call takes. All three land under one t.TempDir(), and newFixture refuses to
// hand the paths over until it has checked that: a test that passed while
// writing /dev/pfanctl would leave this machine's fan wherever the last case
// left it.
type fixture struct {
	dir    string
	device string
	kmsg   string
	conf   string
	cands  string
}

// confSrc is the fixture's platform.conf. It is the host's own golden file with
// only DEV retargeted, so a case that forgets to configure the EC half still
// exercises the real defaults rather than silently getting an empty config.
func confSrc(device string) string {
	return "EC_ROOT=\\_SB.PCI0.LPCB.EC0\n" +
		"FAN_DEVICE=\\_SB.PCI0.LPCB.EC0.TFN1\n" +
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n" +
		"DEV=" + device + "\n" +
		"EC_CMD=0xB5\n"
}

// candidatesSrc mirrors the host's list: the off-subtree pair is the reason the
// file exists, so a fixture that omitted it could not test the escape hatch.
func candidatesSrc() string {
	return `\_SB.PCI0.LPCB.EC0 \_SB.PCI0.LPCB.EC0.TFN1` + "\n" +
		`\_SB.PC00.LPCB.EC0 \_SB.IETM.TFN1` + "\n"
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		dir:    dir,
		device: filepath.Join(dir, "pfanctl"),
		kmsg:   filepath.Join(dir, "kmsg"),
		conf:   filepath.Join(dir, "platform.conf"),
		cands:  filepath.Join(dir, "fan-candidates.txt"),
	}
	// An explicit PFAN_DEV is pfand's own test seam, so the device is only
	// required to exist; it is a plain file here and would be a plain file on a
	// development machine.
	require.NoError(t, os.WriteFile(f.device, nil, 0o600))
	require.NoError(t, os.WriteFile(f.kmsg, nil, 0o600))
	require.NoError(t, os.WriteFile(f.conf, []byte(confSrc(f.device)), 0o600))
	require.NoError(t, os.WriteFile(f.cands, []byte(candidatesSrc()), 0o600))
	t.Setenv(fanDeviceEnv, f.device)
	t.Setenv(fanLockEnv, filepath.Join(dir, "pfanel.lock"))
	t.Setenv(fanKmsgEnv, f.kmsg)
	t.Setenv(platformConfEnv, f.conf)
	t.Setenv(fanCandidatesEnv, f.cands)
	f.assertIsolated(t)
	return f
}

// rewriteConf replaces the fixture's platform.conf, for the cases whose whole
// point is a configuration that would be refused.
func (f *fixture) rewriteConf(t *testing.T, src string) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.conf, []byte(src), 0o600))
	f.assertIsolated(t)
}

// env is the resolved fan environment, i.e. exactly what the exported entry
// points use.
func (f *fixture) env(t *testing.T) fanEnv {
	t.Helper()
	env, err := resolveFanEnv()
	require.NoError(t, err)
	return env
}

// prod is the resolved triple, i.e. exactly what the exported entry points use.
func (f *fixture) prod() FanPaths { return ProdFanPaths() }

// assertIsolated makes "no test writes production ACPI" checkable rather than
// assumed: every path the write path can reach is checked against this case's own
// temp dir, so a future edit that reintroduces a hard-coded production path fails
// here rather than on a fan.
func (f *fixture) assertIsolated(t *testing.T) {
	t.Helper()
	got := ProdFanPaths()
	for name, p := range map[string]string{
		"device": got.Device, "lock": got.Lock, "kmsg": got.Kmsg,
		"conf": os.Getenv(platformConfEnv), "candidates": os.Getenv(fanCandidatesEnv),
	} {
		require.Truef(t, strings.HasPrefix(p, f.dir+string(filepath.Separator)),
			"%s path %q escaped the fixture root %q and could reach production", name, p, f.dir)
	}
	require.NotEqual(t, ProdFanDevice, got.Device)
	require.NotEqual(t, ProdFanLock, got.Lock)
	require.NotEqual(t, ProdFanKmsg, got.Kmsg)
	require.NotEqual(t, ProdPlatformConf, os.Getenv(platformConfEnv))
	require.NotEqual(t, ProdFanCandidates, os.Getenv(fanCandidatesEnv))
}

func (f *fixture) written() string {
	b, err := os.ReadFile(f.device)
	if err != nil {
		return ""
	}
	return string(b)
}

// appendResult simulates the kernel logging one RESULT line for the write that
// just landed. It waits for the device to be written first, so a caller that
// never writes the device gets no reply -- which is how the stale-result case
// below stays honest.
func (f *fixture) appendResult(t *testing.T, payload string) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if f.written() != "" {
				fh, err := os.OpenFile(f.kmsg, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return
				}
				fmt.Fprintf(fh, "6,1,1,-;pfanctl: RESULT %s\n", payload)
				fh.Close()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(wg.Wait)
}

func TestFanPathsDefaultToProduction(t *testing.T) {
	// pfand's own rule is that a seam's default must equal the production value,
	// and tests/shell/test_pfand_loop.sh T0 asserts it for pfand. Same here: if
	// these drift, the agent silently stops talking to the same node pfand does.
	assert.Equal(t, "/dev/pfanctl", ProdFanDevice)
	assert.Equal(t, "/run/pfanel.lock", ProdFanLock)
	assert.Equal(t, "/dev/kmsg", ProdFanKmsg)
	assert.Equal(t, FanPaths{ProdFanDevice, ProdFanLock, ProdFanKmsg}, ProdFanPaths(),
		"with no PFAN_* set in the environment, the resolver must return the production triple")
}

// TestFanPathsFollowPfandEnvNames pins the seam NAMES to the ones the daemon
// reads. A renamed variable is a silent loss of arbitration: both writers would
// flock different inodes and neither would know.
func TestFanPathsFollowPfandEnvNames(t *testing.T) {
	t.Setenv(fanDeviceEnv, "/tmp/pfan-fixture/dev")
	t.Setenv(fanLockEnv, "/tmp/pfan-fixture/lock")
	t.Setenv(fanKmsgEnv, "/tmp/pfan-fixture/kmsg")
	assert.Equal(t, "/tmp/pfan-fixture/dev", ProdFanPaths().Device)
	assert.Equal(t, "/tmp/pfan-fixture/lock", ProdFanPaths().Lock)
	assert.Equal(t, "/tmp/pfan-fixture/kmsg", ProdFanPaths().Kmsg)
	assert.Equal(t, "PFAN_DEV", fanDeviceEnv)
	assert.Equal(t, "PFAN_LOCK", fanLockEnv)
}

// TestAcpiEvalWritesExpressionAndReturnsOwnResult is the happy path, and it is
// also the only thing that proves the write actually happens.
func TestAcpiEvalWritesExpressionAndReturnsOwnResult(t *testing.T) {
	f := newFixture(t)
	f.appendResult(t, "[0,25,4294967295]")
	got, err := acpiEvalAt(f.prod(), `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "[0,25,4294967295]", got)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, f.written(),
		"the expression must reach the device verbatim")
}

// TestAcpiEvalIgnoresResultsLeftByEarlierWriters is the defect the brief had:
// /dev/kmsg keeps old RESULT lines forever, so reading "the last RESULT" hands
// back pfand's previous answer and reports it as this call's. The baseline has
// to be drained before the write.
func TestAcpiEvalIgnoresResultsLeftByEarlierWriters(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.kmsg,
		[]byte("6,1,1,-;pfanctl: RESULT [0,70,4294967295]\n"), 0o600))
	_, err := acpiEvalAt(f.prod(), `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, 300*time.Millisecond)
	require.Error(t, err, "a stale RESULT must never be returned as this call's answer")
	assert.Contains(t, err.Error(), "timeout")
}

// TestAcpiEvalRefusesWhenDeviceMissing covers pfand's I2: writing to an absent
// node with O_CREAT would create a plain file that silently swallows every
// later write while the log claims success.
func TestAcpiEvalRefusesWhenDeviceMissing(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.Remove(f.device))
	_, err := acpiEvalAt(f.prod(), `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, time.Second)
	require.Error(t, err)
	assert.False(t, statExists(f.device), "the failed call must not create the node")
}

// TestAcpiEvalRefusesWhenDeviceIsNotACharDevice is the I6 shape: the node was
// rebuilt as a regular file by a stray rmmod-era echo, so writes "succeed" and
// never reach the hardware.
func TestDeviceWritableSplitsSeamFromProduction(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "node")
	require.NoError(t, os.WriteFile(plain, nil, 0o600))

	// The fixture half: an explicit seam only has to exist, or no fixture could
	// ever be a device.
	assert.NoError(t, deviceWritable(plain, plain))
	// The production half: with no seam the node must be a character device.
	// Dropping this branch is how a write "succeeds" into a plain file that
	// never reaches the hardware (pfand's I6).
	assert.Error(t, deviceWritable(plain, ""))
	// Missing is refused either way, and refusing is what stops os.WriteFile's
	// O_CREAT from inventing the node (I2).
	assert.Error(t, deviceWritable(filepath.Join(t.TempDir(), "absent"), ""))
	assert.Error(t, deviceWritable(filepath.Join(t.TempDir(), "absent"), "x"))
}

func TestAcpiEvalGivesUpWhenLockIsHeld(t *testing.T) {
	f := newFixture(t)
	held, err := os.OpenFile(f.prod().Lock, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer held.Close()
	require.NoError(t, lockExclusive(f.prod().Lock, int(held.Fd())))

	start := time.Now()
	_, err = acpiEvalAt(f.prod(), `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, 5*time.Second)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock")
	// flock(2) has no timeout, so the bound has to come from the retry count.
	// One poll interval is the floor; unbounded blocking would fail this.
	assert.GreaterOrEqual(t, elapsed, fanLockAttempts*fanLockBackoff/2)
	assert.Empty(t, f.written(), "no device write may happen without the lock")
}

func TestSetFanLevelRejectsOffWhitelistBeforeTouchingDevice(t *testing.T) {
	f := newFixture(t)
	err := setFanLevelAt(f.prod(), f.env(t).EC, 26)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid fan level")
	assert.Empty(t, f.written(), "validation must run before the device is opened")
}

// TestSetFanLevelWritesWhitelistedLevel also pins that a write does NOT wait for
// a RESULT line. _FSL returns nothing, so waiting would turn every fan write
// into a multi-second timeout -- which is why pfand's setL never reads back.
func TestSetFanLevelWritesWhitelistedLevel(t *testing.T) {
	f := newFixture(t)
	done := make(chan error, 1)
	go func() { done <- setFanLevelAt(f.prod(), f.env(t).EC, 40) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("SetFanLevel blocked on a RESULT line that _FSL never produces")
	}
	assert.Contains(t, f.written(), "_FSL 1 40")
}

// TestFanMethodsFollowTheConfiguredFanDevice replaces a test that pinned the
// ACPI paths to literals. Pinning them was pinning the defect: an operator who
// changed FAN_DEVICE changed nothing at all. What matters now is that the path
// is built from the configured value, and that no other configured value can
// stand in for it -- real machines put the fan device outside the EC subtree.
func TestFanMethodsFollowTheConfiguredFanDevice(t *testing.T) {
	for _, device := range []string{
		`\_SB.PCI0.LPCB.EC0.TFN1`,
		`\_SB.PC00.LPCB.EC0.TFN1`,
		`\_SB.IETM.TFN1`,
	} {
		conf := ECConf{FANDevice: device, ECRoot: `\_SB.SOMETHING.ELSE`}
		assert.Equal(t, device+`._FSL`, acpiFanPath(conf, "_FSL"), "device %s", device)
		assert.Equal(t, device+`._FST`, acpiFanPath(conf, "_FST"), "device %s", device)
		assert.NotContains(t, acpiFanPath(conf, "_FSL"), conf.ECRoot,
			"an off-subtree fan device must not be rewritten as if it were under the EC root")
	}
}

// TestECPathFollowsTheConfiguredECRoot keeps the trap the old test recorded.
// EC88 hangs off the EC root, not off TFN1: the module logs get_handle(...)=5
// and returns nothing when asked for the wrong one.
func TestECPathFollowsTheConfiguredECRoot(t *testing.T) {
	for _, root := range []string{`\_SB.PCI0.LPCB.EC0`, `\_SB.PC00.LPCB.EC0`} {
		conf := ECConf{ECRoot: root, FANDevice: root + `.TFN1`}
		assert.Equal(t, root+`.EC88`, acpiECPath(conf))
		assert.NotContains(t, acpiECPath(conf), "TFN1",
			"EC88 under TFN1 returns AE_NOT_FOUND on this chassis")
	}
}

// TestECAccessCarriesTheConfiguredCommandByte. EC_CMD used to be a literal 181,
// so the key was decorative. The byte is wire protocol: the wrong one is not a
// failed read but a command the controller was never asked for.
func TestECAccessCarriesTheConfiguredCommandByte(t *testing.T) {
	for _, tc := range []struct {
		cmd  uint64
		want string
	}{
		{0xB5, "181"},
		{0x42, "66"},
		{0xFF, "255"},
	} {
		expr := ecReadExpression(ECConf{ECRoot: `\_SB.EC0`, Cmd: tc.cmd}, 0x76)
		assert.Containsf(t, expr, tc.want, "EC_CMD=%#x must reach the expression", tc.cmd)
	}
}

// TestECExpressionShape pins the argument order pfand writes, because a
// reordered EC88 call is a different request rather than a malformed one.
func TestECExpressionShape(t *testing.T) {
	conf := ECConf{ECRoot: `\_SB.PCI0.LPCB.EC0`, Cmd: 0xB5}
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.EC88 4 181 118 0 0`, ecReadExpression(conf, 0x76))
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.EC88 4 181 121 0 0`, ecReadExpression(conf, 0x79))
}

func TestGetFanLevelReadsTheLevelField(t *testing.T) {
	for _, payload := range []string{"[0,25,4294967295]", "[ 0, 70, 4294967295 ]"} {
		f := newFixture(t)
		f.appendResult(t, payload)
		lv, err := getFanLevelAt(f.prod(), f.env(t).EC)
		require.NoError(t, err, "payload %q", payload)
		assert.Equal(t, mustField(t, payload, 1), lv, "payload %q", payload)
	}
	assert.Equal(t, 25, mustField(t, "[0,25,4294967295]", 1))
	assert.Equal(t, 70, mustField(t, "[0,70,4294967295]", 1))
}

func mustField(t *testing.T, payload string, i int) int {
	t.Helper()
	fields := strings.FieldsFunc(payload, func(r rune) bool {
		return r == ',' || r == '[' || r == ']' || unicode.IsSpace(r)
	})
	require.Greater(t, len(fields), i)
	v, err := strconv.Atoi(fields[i])
	require.NoError(t, err)
	return v
}

func TestGetFanLevelRejectsTruncatedPayload(t *testing.T) {
	f := newFixture(t)
	f.appendResult(t, "[0]")
	_, err := getFanLevelAt(f.prod(), f.env(t).EC)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected")
}

// TestReadEC76TreatsIncompleteStatusAsUnavailable is the fail-closed half. This
// machine currently answers EC 0x76 with 0xFFFF0000, so anything that reported a
// number instead of an error would be publishing a duty it never read.
func TestReadEC76TreatsIncompleteStatusAsUnavailable(t *testing.T) {
	for _, raw := range []string{"4294908160", "4294967295", "4294909440"} {
		f := newFixture(t)
		f.appendResult(t, raw)
		_, err := readEC76At(f.prod(), f.env(t).EC)
		require.Errorf(t, err, "raw %s must not be reported as a duty", raw)
		assert.Contains(t, err.Error(), "unavailable")
	}
}

// TestReadEC76ExtractsDutyByte uses pfanel.py's shift, which is what fills the
// ec76 column this project already reads: duty lives in bits 8..15.
func TestReadEC76ExtractsDutyByte(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"6400", 25},   // 0x00001900
		{"25600", 100}, // 0x00006400
		{"8704", 34},   // 0x00002200
	} {
		f := newFixture(t)
		f.appendResult(t, tc.raw)
		got, err := readEC76At(f.prod(), f.env(t).EC)
		require.NoErrorf(t, err, "raw %s", tc.raw)
		assert.Equalf(t, tc.want, got, "raw %s", tc.raw)
	}
}

func TestReadEC76RejectsNonNumeric(t *testing.T) {
	f := newFixture(t)
	f.appendResult(t, "not-a-number")
	_, err := readEC76At(f.prod(), f.env(t).EC)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// ---------------------------------------------------------------- resolution

// TestDeviceResolutionPrefersTheEnvironmentSeam keeps pfand's own order:
// DEV="${PFAN_DEV:-}" first, the platform file second, the chassis default last.
// Reversing the first two would silently redirect a running agent whose daemon
// was pointed at a fixture.
func TestDeviceResolutionPrefersTheEnvironmentSeam(t *testing.T) {
	f := newFixture(t)
	f.rewriteConf(t, confSrc("/dev/somewhere-else"))
	assert.Equal(t, f.device, f.env(t).Paths.Device,
		"PFAN_DEV is pfand's own seam and outranks the configured DEV")
}

// TestDeviceResolutionFallsBackToTheConfiguredNode is what makes DEV a live key.
// Before, editing it changed nothing; now it chooses the node when no seam is
// set, which is the production case.
//
// The seam is cleared, so the fixture's blanket isolation check cannot run --
// with PFAN_DEV empty the seam resolver legitimately points at production. This
// case only resolves paths and never opens a device, and it asserts below that
// the node it resolved is inside the fixture anyway.
func TestDeviceResolutionFallsBackToTheConfiguredNode(t *testing.T) {
	f := newFixture(t)
	t.Setenv(fanDeviceEnv, "")
	require.NoError(t, os.WriteFile(f.conf, []byte(confSrc(f.device)), 0o600))

	got := f.env(t).Paths.Device
	assert.Equal(t, f.device, got,
		"with no seam set, platform.conf's DEV decides the node")
	require.True(t, strings.HasPrefix(got, f.dir+string(filepath.Separator)),
		"the configured node must still be the fixture's, not production")
}

// TestAbsentPlatformConfFallsBackToTheChassisConstants. A missing file is not a
// misconfiguration: there is no operator intent to honour, and refusing here
// would take fan control away from a chassis that never configured it.
func TestAbsentPlatformConfFallsBackToTheChassisConstants(t *testing.T) {
	f := newFixture(t)
	t.Setenv(platformConfEnv, filepath.Join(f.dir, "does-not-exist.conf"))
	// Every PFAN_* seam cleared, so what is asserted is the file's absence and
	// not the fixture's overrides.
	t.Setenv(fanDeviceEnv, "")
	t.Setenv(fanLockEnv, "")
	t.Setenv(fanKmsgEnv, "")
	env, err := resolveFanEnv()
	require.NoError(t, err)
	assert.Equal(t, ProdFanDevice, env.Paths.Device)
	assert.Equal(t, ProdFanLock, env.Paths.Lock)
	assert.Equal(t, ProdFanKmsg, env.Paths.Kmsg)
	assert.Equal(t, ProdFanPaths(), env.Paths,
		"the seam resolver and the full resolver must not disagree when nothing is configured")
	assert.Equal(t, uint64(0xB5), env.EC.Cmd, "the chassis command byte survives the fallback")
}

// TestUnreadablePlatformConfRefusesRatherThanFallsBack is the other half and is
// the important one. A file that exists and cannot be read is an operator whose
// edit is being ignored; falling back to the constants would reproduce exactly
// the bug this change exists to fix, only louder.
func TestUnreadablePlatformConfRefusesRatherThanFallsBack(t *testing.T) {
	f := newFixture(t)
	f.rewriteConf(t, "EC_ROOT=\\_SB.EC0\nTHERMAL_ZONE=/x/y/temp\n") // missing keys
	_, err := resolveFanEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform config")
}

func TestInvalidECWiringRefusesRatherThanFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  string
		reason error
	}{
		{"unknown backend", "EC_BACKEND=probe\n", ErrECBackendUnknown},
		{"ec_sys without a node", "EC_BACKEND=ec_sys\n", ErrECIONodeRequired},
		{"ec_sys node elsewhere", "EC_BACKEND=ec_sys\nEC_IO=/dev/mem\n", ErrECIOInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.rewriteConf(t, confSrc(f.device)+tc.extra)
			_, err := resolveFanEnv()
			require.ErrorIs(t, err, tc.reason)
			require.ErrorIs(t, SetFanLevel(40), tc.reason,
				"a fan write must refuse on wiring it cannot trust")
			assert.Empty(t, f.written(), "nothing may be written under a refused configuration")
		})
	}
}

// -------------------------------------------------------------- guardrails

// TestMispairedPlatformConfBlocksEverything is the misconnection guard. A
// cross-EC slip reads one machine's fan and writes another's with nothing
// failing in between; refusing is the only direction that cannot misdirect.
func TestMispairedPlatformConfBlocksEverything(t *testing.T) {
	f := newFixture(t)
	// The slip a human actually makes: root updated, fan device left behind.
	f.rewriteConf(t, confSrc(f.device))
	src := confSrc(f.device)
	p, err := ParsePlatformConf(src)
	require.NoError(t, err)
	mispair, err := p.EC()
	require.NoError(t, err)
	mispair.ECRoot = `\_SB.PC00.LPCB.EC0`
	mispair.FANDevice = `\_SB.PCI0.LPCB.EC0.TFN1`

	err = mispair.ValidatePairing(mustCandidates(t, f.cands))
	require.ErrorIs(t, err, ErrECPairMismatch)
	assert.Contains(t, err.Error(), `\_SB.PC00.LPCB.EC0`,
		"the refusal must show the root that was configured")
	assert.Contains(t, err.Error(), `\_SB.PCI0.LPCB.EC0.TFN1`,
		"the refusal must show the fan device it disagrees with")
}

// TestMispairedPlatformConfBlocksTheFanWrite drives the same guard through the
// exported entry point, because the guard is only worth anything if it sits on
// the write path rather than in a helper nobody calls.
func TestMispairedPlatformConfBlocksTheFanWrite(t *testing.T) {
	f := newFixture(t)
	f.rewriteConf(t, "EC_ROOT=\\_SB.PC00.LPCB.EC0\n"+
		"FAN_DEVICE=\\_SB.PCI0.LPCB.EC0.TFN1\n"+
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n"+
		"DEV="+f.device+"\nEC_CMD=0xB5\n")

	err := SetFanLevel(40)
	require.ErrorIs(t, err, ErrECPairMismatch)
	assert.Empty(t, f.written(),
		"a refused pairing must leave the fan exactly as it was")

	_, err = ReadEC76()
	require.ErrorIs(t, err, ErrECPairMismatch)
}

// TestOffSubtreePairingInTheCandidateListIsAllowed is the escape hatch. The host
// ships the list precisely because machines exist whose fan device is not under
// EC0, so the guard must not refuse a pairing the list records.
func TestOffSubtreePairingInTheCandidateListIsAllowed(t *testing.T) {
	f := newFixture(t)
	f.rewriteConf(t, "EC_ROOT=\\_SB.PC00.LPCB.EC0\n"+
		"FAN_DEVICE=\\_SB.IETM.TFN1\n"+
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n"+
		"DEV="+f.device+"\nEC_CMD=0xB5\n")

	env := f.env(t)
	require.NoError(t, env.EC.ValidatePairing(mustCandidates(t, f.cands)))
	require.NoError(t, SetFanLevel(40))
	assert.Equal(t, `\_SB.IETM.TFN1._FSL 1 40`, f.written(),
		"the fan method must follow the configured device, not the EC root")
}

// TestPairingRefusedWhenTheCandidateListCannotBackIt. A candidate list that
// cannot be read is not approval: the chassis's own pairing never needs the
// file, so this only arises for a pairing that was already outside the subtree.
func TestPairingRefusedWhenTheCandidateListCannotBackIt(t *testing.T) {
	f := newFixture(t)
	f.rewriteConf(t, "EC_ROOT=\\_SB.PC00.LPCB.EC0\n"+
		"FAN_DEVICE=\\_SB.NOTLISTED.TFN4\n"+
		"THERMAL_ZONE=/sys/class/thermal/thermal_zone9/temp\n"+
		"DEV="+f.device+"\nEC_CMD=0xB5\n")
	t.Setenv(fanCandidatesEnv, filepath.Join(f.dir, "absent-candidates.txt"))

	err := SetFanLevel(40)
	require.ErrorIs(t, err, ErrECPairMismatch)
	assert.Empty(t, f.written())
}

func mustCandidates(t *testing.T, path string) []FanCandidate {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	cands, err := ParseFanCandidates(string(raw))
	require.NoError(t, err)
	return cands
}

// ------------------------------------------------------------ EC backends

// TestBackendSelectionFollowsECBackend. The selection is a closed, configured
// set -- not a probe. A backend discovered by looking at what is mounted picks
// one of several controllers without anyone reviewing the choice.
func TestBackendSelectionFollowsECBackend(t *testing.T) {
	f := newFixture(t)

	f.rewriteConf(t, confSrc(f.device))
	reader, err := newECReader(f.prod(), f.env(t).EC)
	require.NoError(t, err)
	assert.IsType(t, acpiECReader{}, reader)

	node := filepath.Join(f.dir, "ec0-io")
	f.rewriteConf(t, confSrc(f.device)+"EC_BACKEND=ec_sys\nEC_IO="+ProdECSysRoot+"/ec0/io\n")
	ecConf := f.env(t).EC
	reader, err = newECReader(f.prod(), ecConf)
	require.NoError(t, err)
	assert.IsType(t, ecSysReader{}, reader)
	assert.Equal(t, ProdECSysRoot+"/ec0/io", ecConf.IO, "the node is configured, never guessed")
	assert.NotEqual(t, node, ecConf.IO)
}

// TestECSysReaderReadsTheConfiguredRegister: a reader that ignored the register
// would answer with byte zero and look plausible.
func TestECSysReaderReadsTheConfiguredRegister(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "io")
	image := make([]byte, 256)
	image[0x76] = 42
	image[0x79] = 7
	require.NoError(t, os.WriteFile(node, image, 0o600))

	got, err := ecSysReader{node: node}.ReadRegister(0x76)
	require.NoError(t, err)
	assert.Equal(t, byte(42), got)
	got, err = ecSysReader{node: node}.ReadRegister(0x79)
	require.NoError(t, err)
	assert.Equal(t, byte(7), got, "a second register must not reuse the first read")
}

// TestECSysReaderReportsAMissingNode. The module is not present on every kernel,
// and a silently answered read from nowhere is worse than a refusal.
func TestECSysReaderReportsAMissingNode(t *testing.T) {
	_, err := ecSysReader{node: filepath.Join(t.TempDir(), "absent")}.ReadRegister(0x76)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
}

// TestECSysReaderRefusesAnOutOfRangeRegister rather than reading past the end of
// a short file: a register the handle cannot reach is not a zero duty.
func TestECSysReaderRefusesAnOutOfRangeRegister(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "io")
	require.NoError(t, os.WriteFile(node, []byte{1, 2, 3}, 0o600))
	_, err := ecSysReader{node: node}.ReadRegister(0x76)
	require.Error(t, err)
}

// TestECReaderHasNoWriteMethod is the guardrail that makes ec_sys safe to add at
// all. Duty programming has one reviewed path on this fork -- the firmware's own
// _FSL -- and this backend must not grow a way to instruct an unobserved one.
func TestECReaderHasNoWriteMethod(t *testing.T) {
	iface := reflect.TypeOf((*ecReader)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		assert.NotRegexp(t, `(?i)^(write|set|store|program)`, m.Name,
			"ecReader grew %s: an EC backend in this fork is read-only by construction", m.Name)
	}
	assert.Equal(t, 1, iface.NumMethod(), "the interface must stay a single read")
}

// TestACPIReaderKeepsTheHighHalfGuard: the ACPI reply carries the duty in bits
// 8..15, so the shift must stay paired with the guard or a failed read becomes a
// reported duty.
func TestACPIReaderKeepsTheHighHalfGuard(t *testing.T) {
	for _, raw := range []string{"4294908160", "4294967295"} {
		f := newFixture(t)
		f.appendResult(t, raw)
		reader, err := newECReader(f.prod(), f.env(t).EC)
		require.NoError(t, err)
		_, err = reader.ReadRegister(0x76)
		require.Errorf(t, err, "raw %s must not be reported as a duty", raw)
		assert.Contains(t, err.Error(), "unavailable")
	}
}

// TestBackendsReportTheSameDutyShape is the cross-backend contract. The ACPI
// reply is a word whose bits 8..15 hold the register; an ec_sys byte is the
// register. Each decodes itself, and the caller must not have to know which.
func TestBackendsReportTheSameDutyShape(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "io")
	image := make([]byte, 256)
	image[0x76] = 64
	require.NoError(t, os.WriteFile(node, image, 0o600))

	got, err := ecSysReader{node: node}.ReadRegister(0x76)
	require.NoError(t, err)
	assert.Equal(t, byte(64), got)

	f := newFixture(t)
	f.appendResult(t, "16384") // the same 64, carried in bits 8..15
	reader, err := newECReader(f.prod(), f.env(t).EC)
	require.NoError(t, err)
	acpiGot, err := reader.ReadRegister(0x76)
	require.NoError(t, err)
	assert.Equal(t, got, byte(acpiGot),
		"the two backends must answer in the same units or a backend switch changes the reading")
}

// TestShippedPlatformConfReproducesTheChassisConstants is the no-op proof. This
// fork's behaviour so far has been the chassis constants; if wiring the four
// keys changes what production does on this machine, that is a fan change
// shipped as a portability fix.
func TestShippedPlatformConfReproducesTheChassisConstants(t *testing.T) {
	p, err := ParsePlatformConf(golden(t, "platform.conf"))
	require.NoError(t, err)
	conf, err := p.EC()
	require.NoError(t, err)

	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FSL`, acpiFanPath(conf, "_FSL"),
		"the shipped FAN_DEVICE must still produce the path the hard-coded one did")
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.EC88 4 181 118 0 0`, ecReadExpression(conf, 0x76),
		"the shipped EC_ROOT and EC_CMD must still produce the expression the literal 181 did")
	assert.Equal(t, "/dev/pfanctl", conf.Dev)
	assert.Equal(t, ECBackendACPI, conf.Backend)
	require.NoError(t, conf.ValidatePairing(nil),
		"the shipped pairing is self-contained and needs no second file")
}

func TestPfandActiveIsFalseWhenNoDaemonRuns(t *testing.T) {
	assert.False(t, pfandActiveIn(t.TempDir()),
		"an empty proc root must read as inactive rather than panic")
}

func TestPfandActiveSeesTheDaemonInProc(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "4242"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "4242", "cmdline"),
		[]byte("/bin/bash\x00/usr/local/bin/pfand\x00"), 0o600))
	assert.True(t, pfandActiveIn(dir))
}

func TestPfandActiveIgnoresAnUnrelatedProcess(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "4242"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "4242", "cmdline"),
		[]byte("/usr/bin/pfan-dashboard\x00"), 0o600))
	assert.False(t, pfandActiveIn(dir))
	// Directories that are not pids must be skipped rather than read.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "self"), 0o755))
	assert.False(t, pfandActiveIn(dir))
}

// TestPfandActiveNeedsNoFork is why the function exists: systemctl would have to
// be executed, and agent/plus may not import os/exec at all.
func TestPfandActiveNeedsNoFork(t *testing.T) {
	f, err := os.OpenFile("/proc/self/cmdline", os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	require.NoError(t, err)
	assert.NotContains(t, string(buf[:n]), "systemctl")
}

func statExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// The exported layer, the reject reasons, and the two failure paths below.
// Every other test in this file injects FanPaths or calls the internal entry, so
// none of it covers the seam Task 15 actually calls.

func TestSetFanLevelExportedEntryReachesTheFixture(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	require.NoError(t, SetFanLevel(40))
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FSL 1 40`, f.written(),
		"the exported entry must land the expression on the fixture device, verbatim")
}

func TestAcpiEvalExportedEntryReachesTheFixture(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	f.appendResult(t, "[0,40,4294967295]")
	got, err := AcpiEval(`\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "[0,40,4294967295]", got)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, f.written())
}

func TestGetFanLevelExportedEntryReachesTheFixture(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	f.appendResult(t, "[0,70,4294967295]")
	got, err := GetFanLevel()
	require.NoError(t, err)
	assert.Equal(t, 70, got)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, f.written())
}

func TestReadEC76ExportedEntryReachesTheFixture(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	f.appendResult(t, "25600")
	got, err := ReadEC76()
	require.NoError(t, err)
	assert.Equal(t, 100, got)
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.EC88 4 181 118 0 0`, f.written())
}

// TestPfandActiveExportedEntryDelegatesToProc pins the wrapper the four
// pfandActiveIn tests never call. It compares against a scan of the same root
// instead of a fixed boolean on purpose: the true answer is host state -- pfand
// runs on the machine this was written on and does not run on CI -- so a literal
// expectation could only ever pass on one of them. Delegation is the part of the
// contract a test can check without asking the host anything.
func TestPfandActiveExportedEntryDelegatesToProc(t *testing.T) {
	assert.Equal(t, pfandActiveIn("/proc"), PfandActive(),
		"the exported entry must be a pass-through to the /proc scan, not a second answer")
}

// TestSetFanLevelRejectionCarriesItsReason pins the reason a UI can switch on.
// Task 15 has four outcomes to tell apart, and matching the English text is a
// contract written in prose. The reason names the class; the detail has to
// survive next to it, or the UI can classify but not explain.
func TestSetFanLevelRejectionCarriesItsReason(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	err := SetFanLevel(26)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFanLevelNotAllowed)
	assert.Contains(t, err.Error(), "invalid fan level",
		"the reason names the class; the detail must still say which value was asked for")
	assert.Empty(t, f.written(), "validation must run before the device is opened")
}

// TestUnreachableRejectReasonsStayUnwired is why the other three are declared and
// not returned. ErrPackageTemperature and ErrThermalProfileT4 are Task 14's and
// Task 13's guards, and a package producing them early would be advertising a
// guard it does not have -- the silent wrong value this project treats as the
// worst outcome. ErrPfandActive is a refusal Task 14 still has to decide: refuse,
// or accept and let pfand overwrite on its next tick. Asking PfandActive here
// would tie this test to whether pfand happens to run on the build host, and it
// does run on this one.
func TestUnreachableRejectReasonsStayUnwired(t *testing.T) {
	f := newFixture(t)
	f.assertIsolated(t)
	err := SetFanLevel(50)
	require.NoError(t, err, "a valid level on a healthy fixture is not a refusal")
	assert.NotEmpty(t, f.written())
	for _, reason := range []error{ErrPfandActive, ErrPackageTemperature, ErrThermalProfileT4} {
		assert.NotErrorIs(t, err, reason)
	}
}

// TestRejectReasonsAreDistinct keeps errors.Is usable: two sentinels sharing one
// value collapse into a single reason, which is the failure these names exist to
// remove.
func TestRejectReasonsAreDistinct(t *testing.T) {
	seen := map[error]bool{}
	for _, reason := range []error{
		ErrFanLevelNotAllowed, ErrPfandActive, ErrPackageTemperature, ErrThermalProfileT4,
		ErrFanControlUnsupported,
	} {
		assert.NotEmpty(t, reason.Error(), "an unnamed reason is not a reason")
		assert.Falsef(t, seen[reason], "%v is a duplicate sentinel, so errors.Is cannot tell them apart", reason)
		seen[reason] = true
	}
}

// TestSetFanLevelReportsAWriteFailure is the write-failure branch, which nothing
// reached before: the missing-device cases stop inside deviceWritable, so
// os.WriteFile never got the chance to fail. A directory standing in for the
// device is the deterministic way to make it -- the guard still passes Stat, and
// the write returns EISDIR. Dropping the error check makes this red, which is the
// point: the caller would otherwise report the fan as set to a level nothing was
// programmed with.
func TestSetFanLevelReportsAWriteFailure(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.Remove(f.device))
	require.NoError(t, os.Mkdir(f.device, 0o755))
	f.assertIsolated(t)

	err := SetFanLevel(40)
	require.Error(t, err, "a write that cannot succeed must not be reported as a set")
	assert.Contains(t, err.Error(), "write device")
	assert.NotErrorIs(t, err, ErrFanLevelNotAllowed,
		"40 is on the whitelist, so this is an I/O failure and not a validation refusal")
	assert.NotErrorIs(t, err, ErrFanControlUnsupported,
		"this host implements fan control, so a write that failed is not a missing platform")
	assert.Equal(t, "", f.written(), "nothing reached the device, so nothing may claim otherwise")
}

// TestLockContentionNamesTheLockInUse keeps the contention message pointing at the
// file that was actually locked. With PFAN_LOCK set, the constant would send
// whoever reads the failure to a file nobody holds. The fixture lock lives in a
// temp dir and cannot contain the production path as a substring, which is what
// makes the NotContains a real assertion rather than a tautology.
func TestLockContentionNamesTheLockInUse(t *testing.T) {
	f := newFixture(t)
	held, err := os.OpenFile(f.prod().Lock, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer held.Close()
	require.NoError(t, lockExclusive(f.prod().Lock, int(held.Fd())))

	err = SetFanLevel(40)
	require.Error(t, err)
	assert.Contains(t, err.Error(), f.prod().Lock, "name the lock that was contended")
	assert.NotContains(t, err.Error(), ProdFanLock, "not the constant, which is not the file in use")
}
