//go:build testing

package hub

import "github.com/henrygd/beszel/internal/fanapi"

// SetFanSeams points the fan routes at fakes; api_plus.go says why it exists.
func SetFanSeams(build func(func(fanapi.Event)) fanapi.Deps, pfand func(string) error) func() {
	oldDeps, oldAction := fanDeps, pfandAction
	fanDeps, pfandAction = build, pfand
	return func() { fanDeps, pfandAction = oldDeps, oldAction }
}

// seenGuard is the seam a route test uses to capture the guard its routes were built
// with. The guard is created inside registerPlusFanRoutes and never handed out, and the
// thermal floor writes nothing until the package is past the emergency edge -- which is
// not something a route test can arrange by waiting. The seam takes and returns the guard
// rather than the routes struct, so its signature stays free of unexported types and the
// external hub_test package can install it.
//
// A seam and not a setting: nothing outside the tests assigns it, and the call site
// checks it is there before using it.
var seenGuard func(*fanapi.Guard) *fanapi.Guard

// SetGuardSeam redirects that hook and returns the restore.
func SetGuardSeam(f func(*fanapi.Guard) *fanapi.Guard) func() {
	old := seenGuard
	seenGuard = f
	return func() { seenGuard = old }
}
