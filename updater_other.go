//go:build !darwin

package main

import (
	"errors"
	"net/http"
	"runtime"
)

// updatePlatformKey selects this build's manifest entry. Keyed GOOS-GOARCH so
// future per-arch Windows/Linux artifacts slot into the existing feed shape.
func updatePlatformKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func cleanupStaleUpdateStaging() {}

// applySelfUpdate is the platform seam, not yet implemented off darwin. The
// recipes are known — Windows renames the running exe aside and replaces it
// (a locked file can still be renamed), Linux replaces the executable path
// outright for tarball/AppImage installs and defers to the package manager
// otherwise — but no non-darwin build ships yet, so the seam fails loudly.
func applySelfUpdate(client *http.Client, manifest *updateManifest) error {
	return errors.New("self-update is not supported on this platform yet")
}
