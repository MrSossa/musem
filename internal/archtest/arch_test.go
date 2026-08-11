// Package archtest asserts the architectural rules that the package layout only
// promises.
//
// Directory names do not enforce boundaries; the direction of the imports does.
// A codebase can carry every fashionable layer directory and still have its
// persistence library imported by its core. These tests are what make the rules
// in the design document true rather than aspirational.
package archtest

import (
	"go/build"
	"os"
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

// TestNoNetworkEgress asserts musem never reaches the network.
//
// The spec promises transcripts are processed locally, and transcripts contain
// the user's source code and prompts. A promise in a document does not survive
// a future dependency being added in good faith; this does. If a package here
// ever legitimately needs the network, that is a decision to make deliberately
// by editing this test, not one to make by accident.
func TestNoNetworkEgress(t *testing.T) {
	networking := []string{
		"net", "net/http", "net/url", "net/smtp", "net/rpc",
		"crypto/tls", "database/sql/driver",
	}

	dirs := []string{filepath.Join("..", "..")}
	for _, name := range append(append([]string{}, adapters...), orchestration...) {
		dirs = append(dirs, filepath.Join("..", name))
	}
	dirs = append(dirs, filepath.Join("..", "tui"), filepath.Join("..", "app"))

	for _, dir := range dirs {
		pkg, ok := importDir(t, dir)
		if !ok {
			continue
		}
		for _, imp := range pkg.Imports {
			for _, banned := range networking {
				if imp == banned {
					t.Errorf("%s imports %q; musem processes transcripts locally and must originate no network traffic", dir, imp)
				}
			}
		}
	}
}
