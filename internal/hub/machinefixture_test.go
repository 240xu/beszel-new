//go:build testing

package hub

import (
	"os"
	"path/filepath"
	"testing"
)

// The guard's write path asks the machine which duties and which thermal
// ceiling are this machine's, so a test binary in another package has to be
// told which machine it is on. The fixture is the calibrated one the agent
// package's own tests resolve against, shared rather than copied so the two
// suites cannot disagree about what this machine looks like.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..", "agent", "plus", "testdata", "profile")
	for key, value := range map[string]string{
		"PFAN_PROFILES":   filepath.Join(root, "profiles.json"),
		"PFAN_DMI_ROOT":   filepath.Join(root, "machine", "dmi"),
		"PFAN_HWMON_ROOT": filepath.Join(root, "machine", "hwmon"),
		"PFAN_SYSFS":      filepath.Join(root, "machine", "sysfs"),
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
