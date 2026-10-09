//go:build linux

package plus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// systemd starts pfand through bash, so it appears as an argument, not argv[0].
const pfanDaemon = "/usr/local/bin/pfand"

// PfandActive reports whether the curve daemon is running, so Task 15 can tell
// "pfand owns the curve" (ErrPfandActive) from a lost race. It does not refuse
// here: that is Task 14's policy, and asking would tie setFanLevelAt's tests to
// whether pfand runs on the build host. /proc, not systemctl, which would fork;
// a daemon on its way out still shows, so the answer errs toward "running".
func PfandActive() bool { return pfandActiveIn("/proc") }

// pfandActiveIn takes the proc root for the same reason FanPaths does.
func pfandActiveIn(procRoot string) bool {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false
	}
	me := strconv.Itoa(os.Getpid())
	for _, e := range entries {
		if !isPidName(e.Name()) || e.Name() == me {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		for _, arg := range strings.Split(string(raw), "\x00") {
			if strings.HasSuffix(arg, pfanDaemon) || arg == "pfand" {
				return true
			}
		}
	}
	return false
}

// isPidName keeps directories like "self" and "net" out; only numbers name a
// process we can attribute.
func isPidName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
