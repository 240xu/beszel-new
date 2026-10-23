//go:build linux

package plus

// ACPI fan control for this chassis. /dev/pfanctl is a single-slot device whose
// reply is only observable in the kernel log, so a caller that skips the shared
// lock reads whichever writer spoke last. pfand, pfan and pfanel all flock
// PFAN_LOCK around every write; this file takes that same lock, so arbitration
// is the one already running rather than a second, private one. The three paths
// are seams whose defaults are pfand's own, per its rule that a seam's default
// must equal its production value.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

// Production paths, read off pfand / pfanel.py rather than from any document.
const (
	ProdFanDevice = "/dev/pfanctl"
	ProdFanLock   = "/run/pfanel.lock"
	ProdFanKmsg   = "/dev/kmsg"
)

// Seam names. Two are pfand's verbatim; PFAN_KMSG is ours because pfand shells
// out to dmesg while this file reads the device directly, a fork not being
// permitted here, so it needs a path seam instead of a command seam.
const (
	fanDeviceEnv = "PFAN_DEV"
	fanLockEnv   = "PFAN_LOCK"
	fanKmsgEnv   = "PFAN_KMSG"
)

// Seam names for the two configuration files, same PFAN_ prefix.
const (
	platformConfEnv  = "PFAN_PLATFORM_CONF"
	fanCandidatesEnv = "PFAN_CANDIDATES"
)

// The wiring this fork shipped with, used only when the host has no platform
// file. As data rather than literals, one test can prove that wiring the four
// keys changed nothing here.
const (
	chassisECRoot    = `\_SB.PCI0.LPCB.EC0`
	chassisFanDevice = `\_SB.PCI0.LPCB.EC0.TFN1`
	chassisECMethod  = "EC88"
	chassisECCmd     = 0xB5
)

// ec88LeadingArg is the fixed first argument of the EC88 signature. pfand sends
// 4 on every call; it is not a count of what we send, and is the same both ways.
const ec88LeadingArg = 4

// ecFanDutyReg is the register pfanel.py reads; a property of the firmware, not
// of the backend that reaches it.
const ecFanDutyReg = 0x76

// FanPaths is the injectable triple, threaded the way Collect threads its root.
type FanPaths struct {
	Device string
	Lock   string
	Kmsg   string
}

// fanEnv is one fan operation's whole world. Resolved together on purpose: the
// device and the ACPI object must come from the same read of the config.
type fanEnv struct {
	Paths FanPaths
	EC    ECConf
}

// ProdFanPaths resolves the seams; empty falls back to production.
func ProdFanPaths() FanPaths {
	return FanPaths{
		Device: envOr(fanDeviceEnv, ProdFanDevice),
		Lock:   envOr(fanLockEnv, ProdFanLock),
		Kmsg:   envOr(fanKmsgEnv, ProdFanKmsg),
	}
}

// firstNonEmpty walks in order, which is pfand's own precedence.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// chassisECConf is the wiring of a host that was never configured.
func chassisECConf() ECConf {
	return ECConf{
		ECRoot:    chassisECRoot,
		FANDevice: chassisFanDevice,
		Dev:       ProdFanDevice,
		Cmd:       chassisECCmd,
		Backend:   ECBackendACPI,
	}
}

// loadCandidates returns the host's pairing list, or none. The only caller asks
// whether a pairing is backed, and "could not check" must not read as "fine".
func loadCandidates() []FanCandidate {
	raw, err := os.ReadFile(envOr(fanCandidatesEnv, ProdFanCandidates))
	if err != nil {
		return nil
	}
	cands, err := ParseFanCandidates(string(raw))
	if err != nil {
		return nil
	}
	return cands
}

// loadECConf reads the machine's wiring. An absent file is not a
// misconfiguration: there is no operator intent to honour. One that exists and
// cannot be used is refused, because falling back would reproduce the bug this
// fixes -- an operator's edit silently ignored.
func loadECConf() (ECConf, error) {
	path := envOr(platformConfEnv, ProdPlatformConf)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return chassisECConf(), nil
		}
		return ECConf{}, fmt.Errorf("read platform config %s: %w", path, err)
	}
	p, err := ParsePlatformConf(string(raw))
	if err != nil {
		return ECConf{}, err
	}
	conf, err := p.EC()
	if err != nil {
		return ECConf{}, err
	}
	// The chassis's pairing hangs under its own root, so it needs no 2nd file.
	if conf.NeedsCandidates() {
		if err := conf.ValidatePairing(loadCandidates()); err != nil {
			return ECConf{}, err
		}
	}
	return conf, nil
}

// resolveFanEnv runs before every exported entry point, so a refused wiring is
// refused before the device is opened: a bad edit stops a write, not redirects it.
func resolveFanEnv() (fanEnv, error) {
	conf, err := loadECConf()
	if err != nil {
		return fanEnv{}, err
	}
	return fanEnv{
		Paths: FanPaths{
			Device: firstNonEmpty(os.Getenv(fanDeviceEnv), conf.Dev, ProdFanDevice),
			Lock:   envOr(fanLockEnv, ProdFanLock),
			Kmsg:   envOr(fanKmsgEnv, ProdFanKmsg),
		},
		EC: conf,
	}, nil
}

// flock(2) has no timeout: LOCK_EX blocks until the holder lets go, so a stuck
// pfand would park the agent forever. These bound the wait instead -- pfand holds
// the lock only around one write, so ~1s of retries is generous, and failing
// loudly is the safe direction: the caller simply does not write.
const (
	fanLockAttempts = 20
	fanLockBackoff  = 50 * time.Millisecond
	fanResultPoll   = 50 * time.Millisecond
)

// (?m) matters: each read ends in a newline, and Go's default $ anchors to end
// of text, so without it the capture can never close.
var resultRe = regexp.MustCompile(`(?m)pfanctl: RESULT\s+(.+)$`)

// deviceWritable mirrors pfand's dev_ok: an explicitly configured PFAN_DEV only
// has to exist, because a fixture device is a plain file, while the production
// path must be a character device. Writing to a missing node with O_CREAT would
// create one, and every later write would then be discarded silently while the
// log claimed success (pfand's I2 and I6).
func deviceWritable(path, seam string) error {
	if seam != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s is unusable: %w", path, err)
		}
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is unusable: %w", path, err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("%s is not a character device; writes would succeed and never reach the hardware", path)
	}
	return nil
}

func lockExclusive(path string, fd int) error {
	var contended error
	for i := 0; i < fanLockAttempts; i++ {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EAGAIN) { // EAGAIN and EWOULDBLOCK are one value here
			return fmt.Errorf("flock: %w", err)
		}
		contended = err
		time.Sleep(fanLockBackoff)
	}
	return fmt.Errorf("lock %s is held by another writer after %d attempts: %w",
		path, fanLockAttempts, contended)
}

func withFanLock(paths FanPaths, fn func() error) error {
	lf, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer lf.Close()
	fd := int(lf.Fd())
	if err := lockExclusive(paths.Lock, fd); err != nil {
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn()
}

// acpiFanPath builds a fan method address from the configured device. The method
// name is this package's, never the config's, so a FAN_DEVICE cannot pick a call.
func acpiFanPath(conf ECConf, method string) string {
	return conf.FANDevice + "." + method
}

// acpiECPath is the EC root, not the fan device: pfand documents EC88 under TFN1
// returning AE_NOT_FOUND on every call.
func acpiECPath(conf ECConf) string { return conf.ECRoot + "." + chassisECMethod }

// ecReadExpression is the EC88 call for one register. The trailing zeros are the
// write-argument slots: pfand reads and sets with one shape.
func ecReadExpression(conf ECConf, reg uint64) string {
	return fmt.Sprintf("%s %d %d %d 0 0", acpiECPath(conf), ec88LeadingArg, conf.Cmd, reg)
}

func writeExpression(paths FanPaths, expr string) error {
	if err := deviceWritable(paths.Device, os.Getenv(fanDeviceEnv)); err != nil {
		return err
	}
	if err := os.WriteFile(paths.Device, []byte(expr), 0o200); err != nil {
		return fmt.Errorf("write device: %w", err)
	}
	return nil
}

// kmsg is opened O_NONBLOCK and read with raw unix.Read: os.File would hand the
// fd to the runtime poller, turning a would-be EAGAIN into an untimed wait.
func openKmsg(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	// Rewind to the newest record and drain it, so every later read is a record
	// this call caused: old RESULT lines stay in the buffer forever, so taking
	// "the last RESULT" would hand back pfand's previous answer.
	if _, err := unix.Seek(fd, 0, unix.SEEK_END); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("seek %s: %w", path, err)
	}
	drainKmsg(fd)
	return fd, nil
}

func drainKmsg(fd int) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			return
		}
	}
}

// awaitResult polls for the reply to the write just made, giving up at the
// deadline rather than reporting a value it never saw.
func awaitResult(fd int, expr string, timeout time.Duration) (string, error) {
	buf := make([]byte, 4096)
	deadline := time.Now().Add(timeout)
	for {
		n, err := unix.Read(fd, buf)
		if n > 0 {
			if m := resultRe.FindSubmatch(buf[:n]); m != nil {
				return strings.TrimSpace(string(m[1])), nil
			}
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("timeout waiting for ACPI result: %s", expr)
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			return "", fmt.Errorf("read kmsg: %w", err)
		}
		time.Sleep(fanResultPoll)
	}
}

// acpiEvalAt evaluates one expression under the shared lock and returns the
// reply the kernel logged for it.
func acpiEvalAt(paths FanPaths, expr string, timeout time.Duration) (string, error) {
	var result string
	err := withFanLock(paths, func() error {
		fd, err := openKmsg(paths.Kmsg)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		if err := writeExpression(paths, expr); err != nil {
			return err
		}
		result, err = awaitResult(fd, expr, timeout)
		return err
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// AcpiEval evaluates one ACPI expression under the shared lock.
func AcpiEval(expr string, timeout time.Duration) (string, error) {
	return acpiEvalAt(ProdFanPaths(), expr, timeout)
}

// SetFanLevel programs the duty percentage. It writes and returns: _FSL returns
// nothing, so waiting for a RESULT line would turn every fan write into a
// timeout, which is why pfand's setL never reads back.
func SetFanLevel(level int) error {
	env, err := resolveFanEnv()
	if err != nil {
		return err
	}
	return setFanLevelAt(env.Paths, env.EC, level)
}

func setFanLevelAt(paths FanPaths, conf ECConf, level int) error {
	if err := ValidateLevel(level); err != nil {
		return fmt.Errorf("%s: %w", err, ErrFanLevelNotAllowed)
	}
	expr := fmt.Sprintf("%s 1 %d", acpiFanPath(conf, "_FSL"), level)
	return withFanLock(paths, func() error { return writeExpression(paths, expr) })
}

// GetFanLevel reads the programmed level; the reply is [ignored, level, rpm],
// with rpm pinned at 0xFFFFFFFF because no tach is exposed.
func GetFanLevel() (int, error) {
	env, err := resolveFanEnv()
	if err != nil {
		return 0, err
	}
	return getFanLevelAt(env.Paths, env.EC)
}

func getFanLevelAt(paths FanPaths, conf ECConf) (int, error) {
	out, err := acpiEvalAt(paths, acpiFanPath(conf, "_FST")+" 0", 3*time.Second)
	if err != nil {
		return 0, err
	}
	// Comma separated inside brackets; Fields alone would leave
	// "[0,25,4294967295]" as one field and reject a perfectly good reply.
	fields := strings.FieldsFunc(out, func(r rune) bool {
		return r == ',' || r == '[' || r == ']' || unicode.IsSpace(r)
	})
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected _FST payload: %q", out)
	}
	level, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("parse _FST level from %q: %w", out, err)
	}
	return level, nil
}

// ecReader reads one byte of an embedded controller. No write method on purpose:
// this fork's only reviewed way to move a fan is the firmware's _FSL on an
// observed controller, and ec_sys can write too. Leaving writes out is what stops
// a portable backend meaning a second, unreviewed way to instruct hardware.
type ecReader interface {
	ReadRegister(reg uint64) (byte, error)
}

// newECReader picks the named backend. No probing fallback: a backend chosen by
// what happens to be mounted is a choice no one reviewed, made when a register
// read decides a reported value.
func newECReader(paths FanPaths, conf ECConf) (ecReader, error) {
	switch conf.Backend {
	case ECBackendACPI:
		return acpiECReader{paths: paths, conf: conf}, nil
	case ECBackendECSys:
		return ecSysReader{node: conf.IO}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrECBackendUnknown, string(conf.Backend))
	}
}

// acpiECReader evaluates the EC88 method. It owns decoding that reply, whose
// layout is a property of the method rather than of the register asked for.
type acpiECReader struct {
	paths FanPaths
	conf  ECConf
}

func (r acpiECReader) ReadRegister(reg uint64) (byte, error) {
	out, err := acpiEvalAt(r.paths, ecReadExpression(r.conf, reg), 3*time.Second)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse EC 0x%X reply %q: %w", reg, out, err)
	}
	// pfanel.py's shift, which is what fills the ec76 column this project reads.
	// Its only guard is v >= 0xFFFFFFFF; the high half is added here because this
	// machine answers 0xFFFF.... right now, and reporting that as a duty is the
	// silent wrong value to avoid. The extra guard can only lose data.
	if v >= 0xFFFFFFFF || (v>>16)&0xFFFF == 0xFFFF {
		return 0, fmt.Errorf("EC 0x%X read is unavailable (reply %s)", reg, out)
	}
	return byte((v >> 8) & 0xFF), nil
}

// ecSysReader reads the kernel's ec_sys handle: open, seek, read. It uses os
// rather than x/sys/unix so R-62's pinned syscall list need not grow.
//
// The kernel documents the node as indexable without saying whether the index is
// the file position or the buffer; this implements the position reading.
// Unverified here -- ec_sys is not loaded on this host -- hence opt-in only.
type ecSysReader struct{ node string }

func (r ecSysReader) ReadRegister(reg uint64) (byte, error) {
	f, err := os.Open(r.node)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", r.node, err)
	}
	defer f.Close()
	if _, err := f.Seek(int64(reg), io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek %s to register %d: %w", r.node, reg, err)
	}
	var b [1]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return 0, fmt.Errorf("read %s at register %d: %w", r.node, reg, err)
	}
	return b[0], nil
}

// ReadEC76 returns the live duty byte out of EC 0x76.
func ReadEC76() (int, error) {
	env, err := resolveFanEnv()
	if err != nil {
		return 0, err
	}
	return readEC76At(env.Paths, env.EC)
}

func readEC76At(paths FanPaths, conf ECConf) (int, error) {
	reader, err := newECReader(paths, conf)
	if err != nil {
		return 0, err
	}
	v, err := reader.ReadRegister(ecFanDutyReg)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}
