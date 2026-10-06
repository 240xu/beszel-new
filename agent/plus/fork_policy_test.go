//go:build testing

package plus

// Fork policy for agent/plus.
//
// Task 6 pinned "the NVMe collector never forks smartctl" as a structural
// invariant. Task 7 originally weakened it to exempt wifi.go and discovered, by
// being audited, that the weakened form could not hold: splitting the program
// name into two literals, forking some other tool, or widening the exemption
// list all passed the whole suite. The lesson was that an exemption list is
// data, and data can be edited.
//
// So this file states the rule with no exemption mechanism at all. There is no
// map, no allowlist, and no skip condition in the loop below: a file either may
// import os/exec or may not, and the answer is the same for every file. The
// three ways the old rule was defeated are structurally impossible here because
// none of them is expressible -- the rule is a literal comparison inside an
// unconditional loop, not a lookup.
//
// Three independent checks, because one is easy to reason your way around:
//
//	compiledFiles      asks the go tool which files the build actually
//	                   includes, so the scan cannot be narrowed by adding a
//	                   file, renaming one, or gating one behind a build tag.
//	TestPlusPackageIsForkFree     asserts on those files' import lists.
//	TestPlusClosureCannotFork     asserts on the transitive import closure, which
//	                   also covers a fork moved into a subdirectory.
//	TestOnlyThisFileForksInTests  keeps the go tool invocation below from
//	                   spreading to other test files.
//
// os/exec in a *_test.go file is out of scope for the production invariant --
// running the test suite is not what ships -- so TestOnlyThisFileForksInTests
// covers that side separately rather than leaving it implicit.

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compiledFiles lists the non-test .go files the compiler includes for the
// current build configuration.
//
// It deliberately does not read the directory. Directory walking is how the
// previous version of this rule could be narrowed: skip a subdirectory, or skip
// anything that does not end in .go, and the assertion quietly covers less while
// still passing. Asking the go tool instead means the scan and the build agree.
func compiledFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{range .GoFiles}}{{.}} {{end}}", ".").Output()
	require.NoError(t, err,
		"the go tool must be runnable: this is the structural check, and skipping it silently "+
			"would leave the rule as prose that never executes")
	names := strings.Fields(string(out))
	require.NotEmpty(t, names, "no package source found -- the assertion proved nothing")
	return names
}

// TestPlusPackageIsForkFree is the invariant. The two assertions are inside the
// loop with no condition, so there is no per-file verdict to widen.
func TestPlusPackageIsForkFree(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range compiledFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoErrorf(t, err, "cannot parse %s", name)
		for _, imp := range file.Imports {
			assert.NotEqualf(t, `"os/exec"`, imp.Path.Value,
				"%s imports os/exec. No file in agent/plus may fork, whatever the program is "+
					"called and however the name is spelled: NVMe reuses upstream's parse, and "+
					"everything else reads sysfs or netlink.", name)
			assert.NotEqualf(t, `"syscall"`, imp.Path.Value,
				"%s imports syscall. syscall is the raw-fork entry point, so banning it stops "+
					"os/exec being bypassed rather than obeyed.", name)
		}
	}
}

// TestPlusClosureCannotFork covers what the file scan cannot: a fork hidden in a
// subdirectory of agent/plus, or reached through a helper the package imports.
// Neither appears in this package's own import lists, so only the closure sees
// them.
//
// syscall is deliberately not asserted here. On Linux the standard library pulls
// it in through time and slices whether or not anything asks for it, so its
// presence carries no signal. os/exec does, and it is absent.
func TestPlusClosureCannotFork(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "the go tool must be runnable")
	deps := strings.Fields(string(out))
	require.NotEmpty(t, deps, "an empty closure would prove nothing")
	for _, dep := range deps {
		assert.NotEqual(t, "os/exec", dep,
			"agent/plus depends on os/exec through %s; the fork would run in production "+
				"while living in no file of this package", dep)
	}
}

// TestOnlyThisFileForksInTests bounds the one permitted fork, which exists only
// so the two checks above can ask the go tool.
//
// It asserts the exact list rather than that it is non-empty. Asserting only
// non-empty is what let the previous exemption list be widened in silence: any
// addition kept the test green.
func TestOnlyThisFileForksInTests(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var forking []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoErrorf(t, err, "cannot parse %s", name)
		for _, imp := range file.Imports {
			if imp.Path.Value == `"os/exec"` {
				forking = append(forking, name)
			}
		}
	}
	assert.Equal(t, []string{"fork_policy_test.go"}, forking,
		"only this file may run the go tool, and only for the two structural checks above")
}
