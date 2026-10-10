//go:build testing

package hub

import "github.com/henrygd/beszel/internal/fanapi"

// SetFanSeams points the fan routes at fakes; api_plus.go says why it exists.
func SetFanSeams(build func(func(fanapi.Event)) fanapi.Deps, pfand func(string) error) func() {
	oldDeps, oldAction := fanDeps, pfandAction
	fanDeps, pfandAction = build, pfand
	return func() { fanDeps, pfandAction = oldDeps, oldAction }
}
