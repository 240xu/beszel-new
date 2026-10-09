//go:build !linux

package plus

import (
	"os"
	"time"
)

// Production paths are declared here too, so that a caller reading them on a
// non-Linux host gets the same answer rather than a compile error.
const (
	ProdFanDevice = "/dev/pfanctl"
	ProdFanLock   = "/run/pfanel.lock"
	ProdFanKmsg   = "/dev/kmsg"
)

const (
	fanDeviceEnv = "PFAN_DEV"
	fanLockEnv   = "PFAN_LOCK"
	fanKmsgEnv   = "PFAN_KMSG"
)

type FanPaths struct {
	Device string
	Lock   string
	Kmsg   string
}

func ProdFanPaths() FanPaths {
	envOr := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	return FanPaths{
		Device: envOr(fanDeviceEnv, ProdFanDevice),
		Lock:   envOr(fanLockEnv, ProdFanLock),
		Kmsg:   envOr(fanKmsgEnv, ProdFanKmsg),
	}
}

// Every entry point below answers with ErrFanControlUnsupported rather than one of
// the rejection reasons: nothing was refused here, the duty was never programmable.
func AcpiEval(string, time.Duration) (string, error) { return "", ErrFanControlUnsupported }

func SetFanLevel(int) error { return ErrFanControlUnsupported }

func GetFanLevel() (int, error) { return 0, ErrFanControlUnsupported }

func ReadEC76() (int, error) { return 0, ErrFanControlUnsupported }

// PfandActive has no portable answer: systemctl would have to be run, and this
// package may not fork. False means "not known to be running", which is the
// direction that lets a write through, so callers that gate on it must also
// gate on the platform.
func PfandActive() bool { return false }
