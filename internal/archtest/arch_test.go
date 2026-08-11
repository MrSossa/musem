// Package archtest asserts the architectural rules that the package layout only
// promises.
//
// Directory names do not enforce boundaries; the direction of the imports does.
// A codebase can carry every fashionable layer directory and still have its
// persistence library imported by its core. These tests are what make the rules
// in the design document true rather than aspirational.
package archtest

import (
	"bytes"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/MrSossa/musem"

// adapters wrap something foreign: a CLI, a database, the filesystem. They are
// named after what they wrap so that "does this belong here?" answers itself.
var adapters = []string{"claude", "sqlite", "git", "inmem"}

// orchestration packages own behaviour that belongs to no single adapter: the
// discovery loop, staleness tracking, cost aggregation across sources.
var orchestration = []string{"registry", "cost"}

// TestRootPackageImportsNothingExternal is the load-bearing rule. The domain
// sits at the centre precisely because it depends on nobody; the moment it
// imports an adapter or a driver, every other boundary becomes decorative.
func TestRootPackageImportsNothingExternal(t *testing.T) {
	pkg, ok := importDir(t, filepath.Join("..", ".."))
	if !ok {
		t.Fatal("root package has no Go files")
	}

	for _, imp := range pkg.Imports {
		if isStdlib(imp) {
			continue
		}
		t.Errorf("root package imports %q; it must depend on nothing outside the standard library", imp)
	}
}

// TestOrchestrationDoesNotImportAdapters keeps the dependency arrow pointing
// inward. Orchestration declares the interfaces it needs and stays ignorant of
// who satisfies them; only main.go knows that claude is the discoverer.
func TestOrchestrationDoesNotImportAdapters(t *testing.T) {
	for _, name := range orchestration {
		pkg, ok := importDir(t, filepath.Join("..", name))
		if !ok {
			continue // not implemented yet
		}

		for _, imp := range pkg.Imports {
			if adapter, ok := internalPackage(imp); ok && contains(adapters, adapter) {
				t.Errorf("%s imports adapter %q; orchestration must declare interfaces, not depend on implementations", name, imp)
			}
		}
	}
}

// TestAdaptersDoNotImportEachOther keeps each adapter wrapping exactly one
// foreign thing. Composition is main's job, not an adapter's.
func TestAdaptersDoNotImportEachOther(t *testing.T) {
	for _, name := range adapters {
		pkg, ok := importDir(t, filepath.Join("..", name))
		if !ok {
			continue
		}

		for _, imp := range pkg.Imports {
			other, ok := internalPackage(imp)
			if ok && other != name && contains(adapters, other) {
				t.Errorf("adapter %s imports adapter %q; adapters wrap one dependency each and are composed in main", name, imp)
			}
		}
	}
}

// TestAdaptersDoNotImportOrchestration is the other half of the arrow. An
// adapter importing the package that consumes it inverts the dependency, and
// the shared type it reached for belongs in the domain instead — that is where
// both sides can see it without either depending on the other.
func TestAdaptersDoNotImportOrchestration(t *testing.T) {
	for _, name := range adapters {
		pkg, ok := importDir(t, filepath.Join("..", name))
		if !ok {
			continue
		}

		for _, imp := range pkg.Imports {
			if other, ok := internalPackage(imp); ok && contains(orchestration, other) {
				t.Errorf("adapter %s imports orchestration package %q; dependencies point inward, so the shared type belongs in the root package", name, imp)
			}
		}
	}
}

// TestTUIDoesNotImportAdapters keeps presentation off the data sources. The UI
// receives messages; it does not go looking for data.
func TestTUIDoesNotImportAdapters(t *testing.T) {
	pkg, ok := importDir(t, filepath.Join("..", "tui"))
	if !ok {
		t.Skip("tui not implemented yet")
	}

	for _, imp := range pkg.Imports {
		if adapter, ok := internalPackage(imp); ok && contains(adapters, adapter) {
			t.Errorf("tui imports adapter %q; the UI renders what it is handed and fetches nothing", imp)
		}
	}
}

// importDir loads the package in dir, reporting false when the directory holds
// no Go files yet. Packages are addressed by directory rather than by import
// path so that a package that does not exist yet is skipped rather than
// reported as a broken import.
func importDir(t *testing.T, dir string) (*build.Package, bool) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	hasGo := false
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			hasGo = true
			break
		}
	}
	if !hasGo {
		return nil, false
	}

	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("loading package in %s: %v", dir, err)
	}
	return pkg, true
}

// internalPackage returns the first path segment under internal/ for an import
// belonging to this module.
func internalPackage(imp string) (string, bool) {
	prefix := modulePath + "/internal/"
	if !strings.HasPrefix(imp, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(imp, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest, true
}

// isStdlib reports whether an import path belongs to the standard library.
// Standard library paths have no dot in their first segment.
func isStdlib(imp string) bool {
	first := imp
	if i := strings.Index(imp, "/"); i >= 0 {
		first = imp[:i]
	}
	return !strings.Contains(first, ".")
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// clients are the packages that speak a protocol over a socket. Nothing musem
// builds may reach them by any path, however many dependencies deep.
var clients = []string{
	"net/http", "net/http/httputil", "net/smtp", "net/rpc",
	"crypto/tls", "net/mail",
}

// dialers open connections. Nothing musem writes may import one directly.
//
// They are held to a weaker rule than the clients above because they arrive
// transitively whether musem wants them or not: the pure-Go SQLite driver pulls
// in net/url, and google/uuid imports net to read a MAC address. Neither is
// reachable from anything musem calls, but a test cannot assert that from the
// import graph alone — so the graph rule stops at the protocol clients and the
// first-party rule covers everything else.
var dialers = []string{"net", "net/url", "net/netip", "database/sql/driver"}

// TestNoNetworkEgress asserts musem never reaches the network.
//
// The spec promises transcripts are processed locally, and transcripts contain
// the user's source code and prompts. A promise in a document does not survive
// a future dependency being added in good faith; this does. If a package here
// ever legitimately needs the network, that is a decision to make deliberately
// by editing this test, not one to make by accident.
//
// The check is transitive because that is the only version worth having. A rule
// that inspects direct imports is satisfied by any dependency that does the
// reaching on musem's behalf, which is precisely how this gets in by accident.
func TestNoNetworkEgress(t *testing.T) {
	for _, pkg := range listPackages(t, "-deps", "./...") {
		for _, banned := range clients {
			if pkg == banned {
				t.Errorf("%q is in the dependency graph; musem processes transcripts locally and must originate no network traffic", pkg)
			}
		}
	}
}

// TestFirstPartyCodeImportsNoNetworking is the stricter half, applied to the
// code this project actually writes — including cmd/musem, which the graph rule
// above covers only through what it happens to pull in.
func TestFirstPartyCodeImportsNoNetworking(t *testing.T) {
	banned := append(append([]string{}, clients...), dialers...)

	for _, pkg := range listPackages(t, "./...") {
		for _, imp := range listPackages(t, "-f", "{{join .Imports \"\\n\"}}", pkg) {
			for _, b := range banned {
				if imp == b {
					t.Errorf("%s imports %q; musem's own code opens no sockets", pkg, imp)
				}
			}
		}
	}
}

// listPackages runs go list and returns its lines. The go tool is the only
// thing that resolves a module's full dependency graph, and getting that graph
// right is the entire point of the rule it serves.
func listPackages(t *testing.T, args ...string) []string {
	t.Helper()

	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = filepath.Join("..", "..")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, stderr.String())
	}

	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pkgs = append(pkgs, line)
		}
	}
	return pkgs
}
