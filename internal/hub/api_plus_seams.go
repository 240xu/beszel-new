//go:build testing

package hub

import "github.com/henrygd/beszel/internal/fanapi"

// SetFanSeams points the plus fan routes at fakes and returns a function that
// puts the real ones back.
//
// It exists because these routes reach a fan, a thermal zone and a systemd unit.
// A test that could reach any of those would be a test whose result depends on
// the machine it ran on, and a fan test that can move a fan is not a test.
//
// build takes the audit sink rather than a fixed Deps value, so the trail under
// test is the trail the routes actually write to.
//
// TESTING ONLY: production has no way to call this, and the values it sets are
// not read from the environment, so there is no operator-reachable switch here.
func SetFanSeams(build func(func(fanapi.Event)) fanapi.Deps, pfand func(string) error) func() {
	oldDeps, oldAction := fanDeps, pfandAction
	fanDeps, pfandAction = build, pfand
	return func() { fanDeps, pfandAction = oldDeps, oldAction }
}
