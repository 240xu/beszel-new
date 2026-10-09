//go:build testing

package plus

// Fork policy for the plus side of the agent.
//
// Task 6 pinned "the NVMe collector never forks smartctl" as a structural
// invariant. Task 7 originally weakened it to exempt wifi.go and discovered, by
// being audited, that the weakened form could not hold: splitting the program
// name into two literals, forking some other tool, or widening the exemption
// list all passed the whole suite. The lesson was that an exemption list is
// data, and data can be edited.
//
// So this file states the rule with no exemption mechanism at all. There is no
// map, no allowlist, and no per-file verdict: inside the scope below, a file
// either may import os/exec or may not, and the answer is the same for every
// file.
//
// WHY THE SCOPE IS A PACKAGE SET AND NOT ONE DIRECTORY
//
// Task 9 added agent/pfanel, which reads pfanel's sqlite history and hands the
// row to agent/plus as a plus.FanSample. It is a SIBLING of agent/plus and it
// imports agent/plus, so it is outside agent/plus's own directory and above it
// in the graph -- the closure check looks downward, at what agent/plus depends
// on, and this package is what depends on it. Every check below used to be
// bounded by agent/plus alone, so a live fork dropped into agent/pfanel compiled,
// ran, and left the whole suite green. No assertion was wrong; the invariant's
// reach had moved out from under it.
//
// The scope is therefore agent/plus plus every sibling package that imports
// agent/plus, computed by governedPackages below rather than written down. A
// future plus-side collector that consumes plus samples is covered the moment it
// exists, with no edit here and no way to be quietly forgotten.
//
// Deliberately outside that scope, and why:
//
//   - agent, agent/utils, agent/zfs fork on purpose: beszel runs smartctl and
//     similar tools to collect NVMe, ZFS and power data. Pushing them in would
//     have made this rule false the moment the sqlite driver was needed, and a
//     rule that has to be exempted is the Task 7 failure again.
//   - agent/tools/pluscheck is a development harness, not shipped collector code.
//   - internal/entities/system translates plus structs onto the hub wire; it
//     holds no collector logic to fork from.
//
// WHAT COUNTS AS A FORK, GIVEN pfanel NEEDS os/exec ITSELF
//
// agent/pfanel must import modernc.org/sqlite, and that driver reaches os/exec
// through modernc.org/libc, which implements C system() with exec.Command. So
// "os/exec is in the closure" is not by itself a finding, and a closure ban
// would be a permanent false positive rather than a rule.
//
// The distinction drawn here is OWNERSHIP, not name and not package list:
//
//	os/exec may enter a collector's closure only by way of a package outside
//	this module.
//
// Any package in this module that directly imports os/exec is code we wrote, in
// this repository, in a file this same test can name -- so it is a fork, and the
// assertion fires whether the fork sits in the collector, in a helper of ours it
// imports, or one level down behind a build tag. A route that has to cross into
// a third-party module first is attributed to that module instead: libc uses
// os/exec for system(), a SELECT never reaches it, and policing it is not an
// invariant we can hold or that the split was built to hold.
//
// Four checks over that scope:
//
//	TestPlusPackageIsForkFree      the files the production build compiles, so a
//	                              fork cannot hide behind a new file, a renamed
//	                              one, or a build tag the scan ignores.
//	TestCollectorClosureCannotFork the attribution rule above, over the whole
//	                              transitive closure rather than our own files.
//	TestOnlyThisFileForksInTests   the directory rather than the build, so it
//	                              also sees test files and tagged files,
//	                              including the go tool call in this file.
//	TestCollectorPackagesAreLeaves no subdirectory, which none of the three
//	                              above can see at any depth.
//
// TestCollectorUnixUseIsNamed is the exception, and it is worse than the rest.
// R-62 unblocked agent/plus's use of x/sys/unix so the fan writer could reach
// flock(2) and the device file, and it left the raw `syscall` package banned.
// That split is deliberate: syscall is the legacy surface with the widest
// platform drift, x/sys/unix is the maintained facade this file's own netlink
// code already reached for.
//
// But it is the one rule here that had to degrade into a list. os/exec and
// syscall can be judged by import alone because the import IS the capability.
// A unix symbol is not: importing x/sys/unix for one constant is not the same act
// as calling Flock, and there is no "entry point" to hang the stronger rule on
// the way Task 7 hung it on the NVMe parse. So the rule falls back to naming
// every symbol, exactly -- a new one shows up in a diff here rather than as one
// more line of code, which is the same failure this file exists to make visible.
// Anyone widening it should expect to argue for it in review.
//
// os/exec in a *_test.go file is out of scope for the production invariant --
// running the test suite is not what ships -- so TestOnlyThisFileForksInTests
// bounds that side separately rather than leaving it implicit.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modulePath is this module, read from the go tool so the ownership test below
// never depends on a package path spelled out by hand.
func modulePath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Path}}").Output()
	require.NoError(t, err, "the go tool must be runnable")
	return strings.TrimSpace(string(out))
}

// selfImportPath is the package these tests live in, github.com/henrygd/beszel's
// agent/plus. Derived rather than written down so the scope below is computed
// from where the test actually is.
func selfImportPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}", ".").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// governedPackages is the scope: agent/plus, then every sibling package that
// imports it.
//
// "Sibling" means a directory beside agent/plus, so the set is the plus-side
// collector layer by construction. agent itself is the parent rather than a
// sibling, and agent/tools is a grandparent, so neither can be swept in by
// widening the rule -- both fork legitimately and must stay out (see header).
//
// A sibling counts only if the go tool confirms a package is there: agent holds
// directories that are not packages at all (test-data, tools), and asking the go
// tool keeps the scan and the build in agreement the same way compiledFiles
// does.
func governedPackages(t *testing.T) []string {
	t.Helper()
	self := selfImportPath(t)
	side := self[:strings.LastIndex(self, "/")+1]
	packages := []string{self}

	entries, err := os.ReadDir("..")
	require.NoError(t, err)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == path.Base(self) {
			continue
		}
		if !hasGoFileAtTopLevel(filepath.Join("..", e.Name())) {
			continue
		}
		pkg := side + e.Name()
		if importsPackage(t, pkg, self) {
			packages = append(packages, pkg)
		}
	}
	require.NotEmpty(t, packages, "the policy must govern at least agent/plus")
	return packages
}

// hasGoFileAtTopLevel reports whether dir holds a .go file directly, not at some
// depth. agent/tools holds only agent/tools/pluscheck, and it is a directory of
// harnesses rather than a package; a recursive test would report it as one.
func hasGoFileAtTopLevel(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".go" {
			return true
		}
	}
	return false
}

// importsPackage asks the go tool whether pkg's non-test imports include want.
func importsPackage(t *testing.T, pkg, want string) bool {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{join .Imports \" \"}}", pkg).Output()
	if err != nil {
		return false
	}
	return contains(strings.Fields(string(out)), want)
}

// compiledFiles lists the non-test .go files the compiler includes for pkg in the
// current build configuration, by directory and base name.
//
// It deliberately does not read the directory. Directory walking is how the
// previous version of this rule could be narrowed: skip a subdirectory, or skip
// anything that does not end in .go, and the assertion quietly covers less while
// still passing. Asking the go tool instead means the scan and the build agree.
//
// What it does not give is tag coverage: no -tags, so nothing behind
// `//go:build testing` appears. That is not a hole -- a file the production build
// skips cannot fork in production -- it is a limit on this function, and
// TestOnlyThisFileForksInTests covers the other side.
func compiledFiles(t *testing.T, pkg string) (dir string, names []string) {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}|{{range .GoFiles}}{{.}} {{end}}", pkg).Output()
	require.NoErrorf(t, err, "cannot list %s", pkg)
	d, fileList, _ := strings.Cut(strings.TrimSpace(string(out)), "|")
	dir, names = d, strings.Fields(fileList)
	require.NotEmptyf(t, names, "no package source found for %s -- the assertion proved nothing", pkg)
	return dir, names
}

// TestPlusPackageIsForkFree is the invariant, over every governed package. The two
// assertions are inside the loop with no condition, so there is no per-file
// verdict to widen.
func TestPlusPackageIsForkFree(t *testing.T) {
	fset := token.NewFileSet()
	for _, pkg := range governedPackages(t) {
		dir, names := compiledFiles(t, pkg)
		for _, name := range names {
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
			require.NoErrorf(t, err, "cannot parse %s", name)
			for _, imp := range file.Imports {
				assert.NotEqualf(t, `"os/exec"`, imp.Path.Value,
					"%s imports os/exec. No file on the plus side may fork, whatever the program is "+
						"called and however the name is spelled: NVMe reuses upstream's parse, and "+
						"everything else reads sysfs or netlink.", name)
				assert.NotEqualf(t, `"syscall"`, imp.Path.Value,
					"%s imports syscall. syscall is the raw-fork entry point, so banning it stops "+
						"os/exec being bypassed rather than obeyed.", name)
			}
		}
	}
}

// TestCollectorClosureCannotFork is the attribution rule, and it is what tells our
// fork apart from a dependency's.
//
// The closure is walked one level of import lists wide, and every package that
// directly imports os/exec is required to be outside this module. That catches a
// fork in a collector, in a helper of ours the collector imports, or anywhere
// below either -- none of which any file scan above can see, because the file is
// not the collector's.
//
// It deliberately does not ban os/exec from the closure as a whole. agent/pfanel
// has to import modernc.org/sqlite, and modernc.org/libc implements C system()
// with exec.Command, so the driver arrives already holding os/exec. Attribution
// is what keeps that from being a permanent red: the route crosses into another
// module, and a SELECT never reaches system() anyway.
//
// syscall is deliberately not asserted here. On Linux the standard library pulls
// it in through time and slices whether or not anything asks for it, so its
// presence carries no signal even under attribution.
func TestCollectorClosureCannotFork(t *testing.T) {
	mod := modulePath(t)
	for _, pkg := range governedPackages(t) {
		attributions := importersOf(t, pkg, "os/exec")
		if len(attributions) == 0 {
			// Nothing in the closure imports os/exec, so the attribution rule has
			// no purchase and nothing is being let through: agent/plus reaches no
			// fork of any kind today.
			t.Logf("%s: os/exec is absent from the closure entirely", pkg)
			continue
		}
		t.Logf("%s: os/exec enters the closure via %s", pkg, strings.Join(attributions, " "))
		for _, importer := range attributions {
			assert.Falsef(t, inModule(importer, mod),
				"%s reaches os/exec through %s, which is a package in this module: that is a fork we "+
					"wrote, not a dependency's. Anything outside this module is attributed to that "+
					"module instead (modernc.org/libc uses os/exec for C system()), so the third-party "+
					"driver is not what this rule polices.", pkg, importer)
		}
	}
}

// importersOf lists every package in pkg's transitive closure whose own imports
// include want.
//
// go list's .Imports excludes _test.go files, so the go tool call in this file
// never appears here and the check does not have to excuse itself.
func importersOf(t *testing.T, pkg, want string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}|{{join .Imports \",\"}}", pkg).Output()
	require.NoErrorf(t, err, "cannot walk the closure of %s", pkg)
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		importer, imports, _ := strings.Cut(line, "|")
		if importer == "" {
			continue
		}
		if contains(strings.Split(imports, ","), want) {
			found = append(found, importer)
		}
	}
	return found
}

// inModule reports whether an import path belongs to this module.
func inModule(importPath, mod string) bool {
	return importPath == mod || strings.HasPrefix(importPath, mod+"/")
}

// TestOnlyThisFileForksInTests bounds the one permitted fork, which exists only
// so the checks above can ask the go tool.
//
// It walks every governed directory rather than the build, so it sees test files
// and files behind any build tag. It asserts the exact list rather than that it
// is non-empty. Asserting only non-empty is what let the previous exemption list
// be widened in silence: any addition kept the test green. Exact equality means a
// second forking file -- in agent/pfanel as much as in agent/plus -- is a failure
// someone has to read, not a row to append.
func TestOnlyThisFileForksInTests(t *testing.T) {
	var forking []string
	fset := token.NewFileSet()
	for _, g := range governed(t) {
		entries, err := os.ReadDir(g.dir)
		require.NoError(t, err)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(g.dir, name), nil, parser.ImportsOnly)
			require.NoErrorf(t, err, "cannot parse %s", name)
			for _, imp := range file.Imports {
				if imp.Path.Value == `"os/exec"` {
					forking = append(forking, g.label+"/"+name)
				}
			}
		}
	}
	assert.Equal(t, []string{"plus/fork_policy_test.go"}, forking,
		"only the fork policy itself may run the go tool, and only for the structural checks above")
}

// governedPackage is one entry of the scope, carrying the directory to read
// alongside the import path. dir is relative to this test's working directory.
type governedPackage struct {
	pkg   string
	dir   string
	label string
}

// governed is governedPackages paired with the directories the two directory
// checks have to walk, so a reported file name carries the package it came from.
func governed(t *testing.T) []governedPackage {
	t.Helper()
	self := selfImportPath(t)
	out := []governedPackage{{pkg: self, dir: ".", label: path.Base(self)}}
	for _, pkg := range governedPackages(t) {
		if pkg != self {
			name := path.Base(pkg)
			out = append(out, governedPackage{pkg: pkg, dir: filepath.Join("..", name), label: name})
		}
	}
	return out
}

// TestCollectorPackagesAreLeaves closes the gap the other three share.
//
// All of them look at one package directory: compiledFiles reads .GoFiles, which
// the go tool reports for that directory only, the closure check covers what is
// imported rather than what sits there, and the directory check stops at the top
// level. A fork one or more levels down appears in none of them, so mutation
// testing showed the combination "put the fork in a subdirectory and disable the
// closure check" defeats everything.
//
// So the invariant is stated directly: no governed package has subpackages. Every
// collector belongs in its own directory, which is also what keeps the rebase
// surface flat. A subdirectory is a test failure rather than a place a future
// collector could hide a fork.
func TestCollectorPackagesAreLeaves(t *testing.T) {
	for _, g := range governed(t) {
		entries, err := os.ReadDir(g.dir)
		require.NoError(t, err)
		var subdirs []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			has, err := subdirHasGoFile(filepath.Join(g.dir, e.Name()))
			require.NoErrorf(t, err, "cannot inspect subdirectory %s", e.Name())
			if has {
				subdirs = append(subdirs, e.Name())
			}
		}
		assert.Emptyf(t, subdirs,
			"%s must stay a leaf package: files in a subdirectory appear in neither "+
				"TestPlusPackageIsForkFree nor TestCollectorClosureCannotFork", g.pkg)
	}
}

// subdirHasGoFile looks at any depth, not just the top of dir: a <dir>/*.go glob
// is invisible to a directory holding only another directory, which is how
// a/b/fork.go passed every check. Matching the extension at every depth also stops
// the rule being narrowed by a build tag, the bypass compiledFiles invites.
func subdirHasGoFile(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".go" {
			found = true
		}
		return nil
	})
	return found, err
}

// contains reports whether list holds want, by exact match: substring matching on
// a comma-joined import list would read os/exec as present in some os/exec
// neighbour.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// R-62: agent/plus may call golang.org/x/sys/unix, and only these symbols.
//
// This list is a DEGRADED rule and the file says so above on purpose. Every other
// rule here states an invariant with no exemption mechanism; this one cannot,
// because unix has no "entry point" to hang the rule on. A raw syscall is a
// capability, and a capability is named as a list, so the list is the minimum
// honest form of the rule: a new syscall appears here, in one place, in the
// diff, rather than slipping in as "one more line of code".
//
// It is exact equality rather than containment, for the reason Task 7 already
// paid for: a containment assertion lets the list grow in silence.
//
// Nothing here weakens the bare `syscall` ban above. syscall and
// golang.org/x/sys/unix are asserted apart on purpose -- syscall is the 1990s
// legacy surface with the widest platform drift, while x/sys/unix is the
// maintained facade Task 7 already reaches for netlink.
var allowedUnixSymbols = []string{
	"Close",
	"EAGAIN",
	"Flock",
	"LOCK_EX",
	"LOCK_NB",
	"LOCK_UN",
	"NL80211_ATTR_IFINDEX",
	"NL80211_ATTR_MAC",
	"NL80211_CMD_GET_STATION",
	"NL80211_GENL_NAME",
	"O_CLOEXEC",
	"O_NONBLOCK",
	"O_RDONLY",
	"Open",
	"Read",
	"SEEK_END",
	"Seek",
}

// TestCollectorUnixUseIsNamed holds the governor packages to that list.
//
// It walks the directory rather than the build, like TestOnlyThisFileForksInTests
// and for the same reason: a rule that can be stepped around by a build tag is
// not a rule. The dependency closure is deliberately untouched, which is a
// different judgement from attribution -- see the header.
func TestCollectorUnixUseIsNamed(t *testing.T) {
	var found []string
	seen := map[string]string{}
	fset := token.NewFileSet()
	for _, g := range governed(t) {
		entries, err := os.ReadDir(g.dir)
		require.NoError(t, err)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" {
				continue
			}
			path := filepath.Join(g.dir, name)
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			require.NoErrorf(t, err, "cannot parse %s", path)
			if !importsUnix(file) {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "unix" {
					return true
				}
				if _, dup := seen[sel.Sel.Name]; !dup {
					seen[sel.Sel.Name] = g.label + "/" + name
					found = append(found, sel.Sel.Name)
				}
				return true
			})
		}
	}
	sort.Strings(found)
	assert.Equal(t, allowedUnixSymbols, found,
		"every golang.org/x/sys/unix symbol these packages use has to be named here. "+
			"x/sys/unix is allowed per R-62 because it is the maintained facade, but it is "+
			"still a raw syscall surface, so the list is what keeps it from growing unnoticed.")
	for _, s := range found {
		t.Logf("unix.%s used by %s", s, seen[s])
	}
}

func importsUnix(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path.Value == `"golang.org/x/sys/unix"` {
			return true
		}
	}
	return false
}
