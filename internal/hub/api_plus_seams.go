//go:build testing

package hub

import "github.com/henrygd/beszel/internal/fanapi"

// SetFanSeams points the fan routes at fakes; api_plus.go says why it exists.
func SetFanSeams(build func(func(fanapi.Event)) fanapi.Deps, pfand func(string) error) func() {
	oldDeps, oldAction := fanDeps, pfandAction
	fanDeps, pfandAction = build, pfand
	return func() { fanDeps, pfandAction = oldDeps, oldAction }
}

// SetGuardSeam redirects the hook declared alongside the routes code and returns
// the restore. The variable itself is declared there rather than here, because
// production code reads it and this file is compiled out of a release build.
func SetGuardSeam(f func(*fanapi.Guard) *fanapi.Guard) func() {
	old := seenGuard
	seenGuard = f
	return func() { seenGuard = old }
}
