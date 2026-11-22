// Package integration runs the tools against a real Forgejo 17.0.2 (DESIGN.md
// section 12.2). WP0 freezes the helper signatures with skipping bodies; WP17
// fills them. The whole package skips when FORGEJO_TEST_URL is unset.
package integration

import (
	"flag"
	"testing"
)

// record switches the suite into fixture-recording mode:
// go test ./test/integration -run Record -record
var record = flag.Bool("record", false, "record API fixtures from the live instance")

// skipUnlessIntegration skips the test unless FORGEJO_TEST_URL and
// FORGEJO_TEST_TOKEN are set (and bash/docker exist where the harness needs
// them). Every test starts with it.
func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	t.Skip("integration harness not implemented (WP17)")
}

// call invokes tool through the real MCP stack (in-memory transport) with
// args and returns the text and the IsError flag.
func call(t *testing.T, tool string, args map[string]any) (text string, isErr bool) {
	t.Helper()
	t.Skip("integration harness not implemented (WP17)")
	return "", false
}

// mustOK calls tool and fails the test on IsError, returning the text.
func mustOK(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	t.Skip("integration harness not implemented (WP17)")
	return ""
}

// mustErr calls tool and fails the test unless IsError is set, returning the
// error text.
func mustErr(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	t.Skip("integration harness not implemented (WP17)")
	return ""
}

// golden compares the stable parts of got with testdata/<name>.golden
// (rewritten with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	t.Skip("integration harness not implemented (WP17)")
}

// TestRecord is the entry point of fixture recording (go test -run Record -record).
func TestRecord(t *testing.T) {
	skipUnlessIntegration(t)
	if !*record {
		t.Skip("pass -record to record fixtures")
	}
}
