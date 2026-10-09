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

// FanPaths is the injectable triple, threaded the way Collect threads its root.
type FanPaths struct {
	Device string
	Lock   string
	Kmsg   string
}

// ProdFanPaths resolves the seams; an empty one falls back to production.
func ProdFanPaths() FanPaths {
	return FanPaths{
		Device: envOr(fanDeviceEnv, ProdFanDevice),
		Lock:   envOr(fanLockEnv, ProdFanLock),
		Kmsg:   envOr(fanKmsgEnv, ProdFanKmsg),
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
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

// (?m) matters: each read returns one record ending in a newline, and Go's
// default $ anchors to end of text, so without it the capture can never close.
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

func lockExclusive(fd int) error {
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
		ProdFanLock, fanLockAttempts, contended)
}

func withFanLock(paths FanPaths, fn func() error) error {
	lf, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer lf.Close()
	fd := int(lf.Fd())
	if err := lockExclusive(fd); err != nil {
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn()
}

func acpiFanPath(method string) string {
	return `\_SB.PCI0.LPCB.EC0.TFN1.` + method
}

// acpiECPath is the EC root, not the fan device. pfand documents the difference
// explicitly: EC88 under TFN1 returns AE_NOT_FOUND on every call.
func acpiECPath() string { return `\_SB.PCI0.LPCB.EC0.EC88` }

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
// fd to the runtime poller, turning a would-be EAGAIN into an untimed wait and
// handing back the deadline problem this code exists to avoid.
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
// timeout -- which is why pfand's setL never reads back.
func SetFanLevel(level int) error { return setFanLevelAt(ProdFanPaths(), level) }

func setFanLevelAt(paths FanPaths, level int) error {
	if err := ValidateLevel(level); err != nil {
		return err
	}
	expr := fmt.Sprintf("%s 1 %d", acpiFanPath("_FSL"), level)
	return withFanLock(paths, func() error { return writeExpression(paths, expr) })
}

// GetFanLevel reads the programmed level; the reply is [ignored, level, rpm],
// with rpm pinned at 0xFFFFFFFF because no tach is exposed.
func GetFanLevel() (int, error) { return getFanLevelAt(ProdFanPaths()) }

func getFanLevelAt(paths FanPaths) (int, error) {
	out, err := acpiEvalAt(paths, acpiFanPath("_FST")+" 0", 3*time.Second)
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

// ReadEC76 returns the live duty byte out of EC 0x76.
func ReadEC76() (int, error) { return readEC76At(ProdFanPaths()) }

func readEC76At(paths FanPaths) (int, error) {
	out, err := acpiEvalAt(paths, acpiECPath()+" 4 181 118 0 0", 3*time.Second)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse EC 0x76 reply %q: %w", out, err)
	}
	// pfanel.py's shift, which is what fills the ec76 column this project reads.
	// Its only guard is v >= 0xFFFFFFFF; the high half is added here because this
	// machine answers 0xFFFF.... right now, and reporting that as a duty is the
	// silent wrong value to avoid. The extra guard can only lose data.
	if v >= 0xFFFFFFFF || (v>>16)&0xFFFF == 0xFFFF {
		return 0, fmt.Errorf("EC 0x76 read is unavailable (reply %s)", out)
	}
	return int((v >> 8) & 0xFF), nil
}
