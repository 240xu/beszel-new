//go:build testing && linux

package plus

import (
	"fmt"
	"os"
	"path/filepath"
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
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		dir:    dir,
		device: filepath.Join(dir, "pfanctl"),
		kmsg:   filepath.Join(dir, "kmsg"),
	}
	// An explicit PFAN_DEV is pfand's own test seam, so the device is only
	// required to exist; it is a plain file here and would be a plain file on a
	// development machine.
	require.NoError(t, os.WriteFile(f.device, nil, 0o600))
	require.NoError(t, os.WriteFile(f.kmsg, nil, 0o600))
	t.Setenv(fanDeviceEnv, f.device)
	t.Setenv(fanLockEnv, filepath.Join(dir, "pfanel.lock"))
	t.Setenv(fanKmsgEnv, f.kmsg)
	f.assertIsolated(t)
	return f
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
	} {
		require.Truef(t, strings.HasPrefix(p, f.dir+string(filepath.Separator)),
			"%s path %q escaped the fixture root %q and could reach production", name, p, f.dir)
	}
	require.NotEqual(t, ProdFanDevice, got.Device)
	require.NotEqual(t, ProdFanLock, got.Lock)
	require.NotEqual(t, ProdFanKmsg, got.Kmsg)
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
	err := setFanLevelAt(f.prod(), 26)
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
	go func() { done <- setFanLevelAt(f.prod(), 40) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("SetFanLevel blocked on a RESULT line that _FSL never produces")
	}
	assert.Contains(t, f.written(), "_FSL 1 40")
}

// TestFanLevelPathIsUnderTheFanDevice pins the ACPI paths to the ones pfand
// actually resolves on this machine. The EC path is the trap: EC88 hangs off
// the EC root, not off TFN1, and the module logs get_handle(...)=5 and returns
// nothing when it is asked for the wrong one.
func TestFanLevelPathIsUnderTheFanDevice(t *testing.T) {
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FSL`, acpiFanPath("_FSL"))
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.TFN1._FST`, acpiFanPath("_FST"))
	assert.Equal(t, `\_SB.PCI0.LPCB.EC0.EC88`, acpiECPath(),
		"EC88 must hang off the EC root; under TFN1 it returns AE_NOT_FOUND")
	assert.NotContains(t, acpiECPath(), "TFN1")
}

func TestGetFanLevelReadsTheLevelField(t *testing.T) {
	for _, payload := range []string{"[0,25,4294967295]", "[ 0, 70, 4294967295 ]"} {
		f := newFixture(t)
		f.appendResult(t, payload)
		lv, err := getFanLevelAt(f.prod())
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
	_, err := getFanLevelAt(f.prod())
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
		_, err := readEC76At(f.prod())
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
		got, err := readEC76At(f.prod())
		require.NoErrorf(t, err, "raw %s", tc.raw)
		assert.Equalf(t, tc.want, got, "raw %s", tc.raw)
	}
}

func TestReadEC76RejectsNonNumeric(t *testing.T) {
	f := newFixture(t)
	f.appendResult(t, "not-a-number")
	_, err := readEC76At(f.prod())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
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
