package testutil

import (
	"net/http"
	"testing"
)

// Fixture returns the bytes of testdata/<name> relative to the package under
// test (recorded API responses live there). WP4 implements the body.
func Fixture(t testing.TB, name string) []byte {
	t.Helper()
	panic("WP4")
}

// ServeFixtures returns a handler that answers "METHOD /path?query" keys with
// the named fixture files (status 200) and 404 for anything else. WP4
// implements the body.
func ServeFixtures(t testing.TB, routes map[string]string) http.Handler {
	t.Helper()
	panic("WP4")
}
