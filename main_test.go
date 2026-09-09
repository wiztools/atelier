package main

import (
	"testing"
)

// TestPrependMissingPathDirs pins the GUI PATH augmentation merge: existing
// Homebrew directories are prepended in order, anything already on the PATH
// (or nonexistent) is left alone, and an empty PATH degrades to just the
// prepended entries.
func TestPrependMissingPathDirs(t *testing.T) {
	always := func(string) bool { return true }
	never := func(string) bool { return false }

	if got := prependMissingPathDirs("/usr/bin:/bin", []string{"/opt/homebrew/bin", "/usr/local/bin"}, always); got != "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("both missing: got %q", got)
	}
	if got := prependMissingPathDirs("/opt/homebrew/bin:/usr/bin:/bin", []string{"/opt/homebrew/bin", "/usr/local/bin"}, always); got != "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin" {
		t.Errorf("one already present: got %q", got)
	}
	if got := prependMissingPathDirs("/opt/homebrew/bin:/usr/local/bin:/usr/bin", []string{"/opt/homebrew/bin", "/usr/local/bin"}, always); got != "/opt/homebrew/bin:/usr/local/bin:/usr/bin" {
		t.Errorf("all present must be a no-op: got %q", got)
	}
	if got := prependMissingPathDirs("/usr/bin:/bin", []string{"/opt/homebrew/bin", "/usr/local/bin"}, never); got != "/usr/bin:/bin" {
		t.Errorf("nonexistent dirs must not be added: got %q", got)
	}
	if got := prependMissingPathDirs("", []string{"/opt/homebrew/bin", "/usr/local/bin"}, always); got != "/opt/homebrew/bin:/usr/local/bin" {
		t.Errorf("empty path: got %q", got)
	}
	if got := prependMissingPathDirs("/usr/bin", []string{"/opt/homebrew/bin", "/opt/homebrew/bin"}, always); got != "/opt/homebrew/bin:/usr/bin" {
		t.Errorf("duplicate dir entries must not repeat: got %q", got)
	}
	if got := prependMissingPathDirs("/usr/bin", nil, always); got != "/usr/bin" {
		t.Errorf("no dirs must be a no-op: got %q", got)
	}
	if got := prependMissingPathDirs("/opt/homebrew/bin/:/usr/bin", []string{"/opt/homebrew/bin"}, always); got != "/opt/homebrew/bin/:/usr/bin" {
		t.Errorf("a trailing-slash variant of an entry counts as present: got %q", got)
	}
}
