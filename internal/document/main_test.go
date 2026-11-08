package document

import (
	"os"
	"testing"
)

// TestMain pins the locale seam so the paper default never follows the
// developer's region; tests that exercise it re-pin SystemLocale and restore
// it with t.Cleanup.
func TestMain(m *testing.M) {
	SystemLocale = func() string { return "" }
	os.Exit(m.Run())
}
