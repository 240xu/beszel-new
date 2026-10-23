//go:build mips || mipsle || mips64 || mips64le

package pfanel

import "github.com/henrygd/beszel/agent/plus"

// modernc.org/sqlite has no mips port (its libc package has no newFile on these
// architectures), so the history database cannot be opened here at all. The
// release matrix still ships these targets; returning ErrOpen keeps the caller's
// existing "opened failed, drop the sample, warn once" path instead of deleting
// the architecture or inventing a fan reading.
func LatestSample(string) (*plus.FanSample, error) {
	return nil, ErrOpen
}
