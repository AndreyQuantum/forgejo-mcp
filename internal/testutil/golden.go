// Package testutil holds shared test helpers (DESIGN.md section 12.1). It may
// only be imported from _test.go files (and from the external test package
// of internal/tools, never from package tools itself, which it imports).
package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Golden compares got with testdata/<name>.golden relative to the package
// under test. With -update the file is (re)written instead. Golden files are
// pinned LF/binary by .gitattributes, so Windows keeps \r and ANSI bytes.
func Golden(t testing.TB, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update to create it)", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch (run with -update to accept)\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}
