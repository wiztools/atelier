//go:build darwin

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApplySelfUpdateE2E exercises the real install path — download, verify,
// stage, helper spawn — against a live manifest server. It is skipped in
// normal `go test` runs and only fires when invoked from inside an .app
// bundle with the feed URL provided:
//
//	go test -c -o /tmp/e2e/Fake.app/Contents/MacOS/Fake .
//	ATELIER_UPDATE_E2E_MANIFEST=http://127.0.0.1:8765/update-manifest.json \
//	  /tmp/e2e/Fake.app/Contents/MacOS/Fake -test.run TestApplySelfUpdateE2E -test.v
//
// The procedure is documented in AGENTS-adjacent terms: the currentAppBundlePath
// guard only passes for a packaged executable, so the test binary must be
// placed inside a (fake) bundle. When applySelfUpdate returns nil the helper
// is already waiting on this PID; the test exits, the helper swaps the bundle
// (replacing the fake .app's own path), and relaunches it.
func TestApplySelfUpdateE2E(t *testing.T) {
	manifestURL := os.Getenv("ATELIER_UPDATE_E2E_MANIFEST")
	if manifestURL == "" {
		t.Skip("set ATELIER_UPDATE_E2E_MANIFEST to run the self-update E2E")
	}
	bundlePath, err := currentAppBundlePath()
	if err != nil {
		t.Skipf("not running from a packaged app (%v) — see the test comment for the invocation", err)
	}
	t.Logf("running from bundle %s", bundlePath)

	resp, err := http.Get(manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var manifest updateManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}

	if err := applySelfUpdate(http.DefaultClient, &manifest); err != nil {
		t.Fatalf("applySelfUpdate failed: %v", err)
	}

	// Nil return means the detached helper is live and waiting on this
	// process. Prove it staged itself before exiting into its hands.
	dir, err := updateStateDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "staged-") {
			continue
		}
		staging := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(filepath.Join(staging, "install.sh")); err != nil {
			t.Errorf("install.sh missing in %s: %v", staging, err)
		} else {
			found = true
		}
	}
	if !found {
		t.Fatal("no staging dir with an install helper was created")
	}
	// Give the runner a beat to observe state before this process exits and
	// the helper performs the swap.
	time.Sleep(500 * time.Millisecond)
}
