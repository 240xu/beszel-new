//go:build testing

package hub

import "github.com/henrygd/beszel/internal/fanapi"

// SetFanSeams points the plus fan routes at fakes and returns a function that
// puts the real ones back. These routes reach a fan, a thermal zone and a systemd
// unit, so a test that could reach any of them would depend on the machine it ran
// on. build takes the audit sink rather than a fixed Deps value, so the trail
// under test is the one the routes write to.
//
// TESTING ONLY: nothing reads these from the environment, so no operator can
// reach this switch.
func SetFanSeams(build func(func(fanapi.Event)) fanapi.Deps, pfand func(string) error) func() {
	oldDeps, oldAction := fanDeps, pfandAction
	fanDeps, pfandAction = build, pfand
	return func() { fanDeps, pfandAction = oldDeps, oldAction }
}
