package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    semver
		wantErr bool
	}{
		{name: "plain", input: "1.2.3", want: semver{major: 1, minor: 2, patch: 3}},
		{name: "v prefix", input: "v0.0.7", want: semver{major: 0, minor: 0, patch: 7}},
		{name: "prerelease", input: "1.0.0-rc.1", want: semver{major: 1, minor: 0, patch: 0, pre: "rc.1"}},
		{name: "build metadata dropped", input: "1.0.0+build.42", want: semver{major: 1, minor: 0, patch: 0}},
		{name: "full", input: "v2.10.3-beta.2+meta", want: semver{major: 2, minor: 10, patch: 3, pre: "beta.2"}},
		{name: "empty", input: "", wantErr: true},
		{name: "dev", input: "dev", wantErr: true},
		{name: "two parts", input: "1.2", wantErr: true},
		{name: "non numeric", input: "1.x.3", wantErr: true},
		{name: "leading zero tolerated", input: "01.02.03", want: semver{major: 1, minor: 2, patch: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSemver(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSemver(%q) succeeded, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSemver(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseSemver(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

// TestCompareSemver walks the semver.org precedence chain: build metadata is
// ignored, prerelease < release, numeric identifiers compare numerically,
// alphanumeric rank above numeric, and longer prerelease lists win ties.
func TestCompareSemver(t *testing.T) {
	chain := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.0.1",
		"1.1.0",
		"2.0.0",
	}
	for i, lower := range chain {
		lowerV, err := parseSemver(lower)
		if err != nil {
			t.Fatalf("parseSemver(%q): %v", lower, err)
		}
		for _, higher := range chain[i+1:] {
			higherV, err := parseSemver(higher)
			if err != nil {
				t.Fatalf("parseSemver(%q): %v", higher, err)
			}
			if compareSemver(lowerV, higherV) >= 0 {
				t.Fatalf("compareSemver(%q, %q) >= 0, want lower first", lower, higher)
			}
			if compareSemver(higherV, lowerV) <= 0 {
				t.Fatalf("compareSemver(%q, %q) <= 0, want higher first", higher, lower)
			}
		}
	}
	if compareSemver(parseSemverOrFatal(t, "1.0.0+b1"), parseSemverOrFatal(t, "1.0.0+b2")) != 0 {
		t.Fatal("build metadata must not affect precedence")
	}
}

func parseSemverOrFatal(t *testing.T, v string) semver {
	t.Helper()
	parsed, err := parseSemver(v)
	if err != nil {
		t.Fatalf("parseSemver(%q): %v", v, err)
	}
	return parsed
}

func TestLatestVersionIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "newer patch", current: "0.0.7", latest: "0.0.8", want: true},
		{name: "newer major", current: "0.9.9", latest: "1.0.0", want: true},
		{name: "same", current: "0.0.8", latest: "0.0.8", want: false},
		{name: "older", current: "0.1.0", latest: "0.0.9", want: false},
		{name: "same with prerelease lower", current: "1.0.0-rc.1", latest: "1.0.0", want: true},
		{name: "dev current loses to any release", current: "dev", latest: "0.0.1", want: true},
		{name: "invalid latest never wins", current: "0.0.7", latest: "oops", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latestVersionIsNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("latestVersionIsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestUpdateCheckDue(t *testing.T) {
	t.Run("missing state file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if !updateCheckDue(time.Now()) {
			t.Fatal("want due when no state file exists")
		}
	})
	t.Run("stale check is due", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		writeUpdateCheckStateForTest(t, time.Now().Add(-25*time.Hour))
		if !updateCheckDue(time.Now()) {
			t.Fatal("want due after 25h")
		}
	})
	t.Run("fresh check is not due", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		writeUpdateCheckStateForTest(t, time.Now().Add(-time.Hour))
		if updateCheckDue(time.Now()) {
			t.Fatal("want not due after 1h")
		}
	})
	t.Run("unparsable record is due", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir, err := updateStateDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"lastCheckAt":"not-a-time"}`), 0644); err != nil {
			t.Fatal(err)
		}
		if !updateCheckDue(time.Now()) {
			t.Fatal("want due on unparsable lastCheckAt")
		}
	})
}

func writeUpdateCheckStateForTest(t *testing.T, at time.Time) {
	t.Helper()
	if err := writeUpdateCheckState(updateCheckState{LastCheckAt: at.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
}

func TestMergeAppConfigBackfillsUpdates(t *testing.T) {
	t.Run("absent section becomes defaults", func(t *testing.T) {
		merged := mergeAppConfig(AppConfig{})
		if merged.Updates.ManifestURL != defaultUpdateManifestURL {
			t.Fatalf("manifestUrl = %q, want default", merged.Updates.ManifestURL)
		}
		if merged.Updates.AutoCheck == nil || !*merged.Updates.AutoCheck {
			t.Fatal("autoCheck should default to true when absent")
		}
	})
	t.Run("explicit opt-out is preserved", func(t *testing.T) {
		disabled := false
		merged := mergeAppConfig(AppConfig{Updates: ConfigUpdates{AutoCheck: &disabled}})
		if merged.Updates.AutoCheck == nil || *merged.Updates.AutoCheck {
			t.Fatal("explicit autoCheck=false must survive the merge")
		}
	})
}

// newUpdateTestApp builds an App whose update client serves canned responses
// keyed by URL, mirroring the canonical app-under-test setup in app_test.go.
func newUpdateTestApp(t *testing.T, manifestURL string, handler roundTripFunc) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	config := defaultAppConfig()
	config.Updates.ManifestURL = manifestURL
	if err := writeAppConfig(config); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.updateClient.Transport = handler
	return app
}

func httpResponse(status int, contentType string, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const testManifestV9 = `{
  "version": "9.9.9",
  "notes": "test release",
  "platforms": {
    "darwin-universal": {"url": "http://updates.test/zip", "sha256": "1111111111111111111111111111111111111111111111111111111111111111"}
  }
}`

func TestCheckForUpdatesStates(t *testing.T) {
	saveVersion := version
	defer func() { version = saveVersion }()

	t.Run("newer manifest is available and cached", func(t *testing.T) {
		version = "0.0.7"
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/manifest.json" {
				return httpResponse(404, "text/plain", "not found"), nil
			}
			return httpResponse(200, "application/json", testManifestV9), nil
		})
		status := app.checkForUpdates(context.Background(), false)
		if status.State != updateStateAvailable {
			t.Fatalf("state = %q (%s), want available", status.State, status.Error)
		}
		if status.LatestVersion != "9.9.9" || status.Notes != "test release" {
			t.Fatalf("status = %+v", status)
		}
		if app.cachedUpdateManifest() == nil {
			t.Fatal("available update should be cached for InstallUpdate")
		}
		// A successful check records its time, so the next automatic check is
		// not due.
		if updateCheckDue(time.Now()) {
			t.Fatal("successful check should mark the throttle window fresh")
		}
	})
	t.Run("same version is current and clears the cache", func(t *testing.T) {
		version = "9.9.9"
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			return httpResponse(200, "application/json", testManifestV9), nil
		})
		// Seed the cache with an older find to prove the current-state path
		// clears it.
		app.updatesMu.Lock()
		app.lastUpdate = &updateManifest{Version: "9.9.9"}
		app.updatesMu.Unlock()
		status := app.checkForUpdates(context.Background(), false)
		if status.State != updateStateCurrent {
			t.Fatalf("state = %q (%s), want current", status.State, status.Error)
		}
		if app.cachedUpdateManifest() != nil {
			t.Fatal("current-state check should clear the cached manifest")
		}
	})
	t.Run("http failure is an error state", func(t *testing.T) {
		version = "0.0.7"
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			return httpResponse(500, "text/plain", "boom"), nil
		})
		status := app.checkForUpdates(context.Background(), false)
		if status.State != updateStateError {
			t.Fatalf("state = %q, want error", status.State)
		}
		if status.Error == "" || !strings.Contains(status.Error, "500") {
			t.Fatalf("error = %q, want the status code surfaced", status.Error)
		}
	})
	t.Run("manifest without this platform is an error state", func(t *testing.T) {
		version = "0.0.7"
		manifest := `{"version":"9.9.9","platforms":{"windows-x64":{"url":"x","sha256":"y"}}}`
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			return httpResponse(200, "application/json", manifest), nil
		})
		status := app.checkForUpdates(context.Background(), false)
		if status.State != updateStateError {
			t.Fatalf("state = %q, want error", status.State)
		}
		if !strings.Contains(status.Error, updatePlatformKey()) {
			t.Fatalf("error = %q, want it to name the missing platform", status.Error)
		}
	})
}

func TestInstallUpdateGates(t *testing.T) {
	t.Run("busy stream refuses", func(t *testing.T) {
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			return httpResponse(200, "application/json", testManifestV9), nil
		})
		app.streamsMu.Lock()
		app.streams["req-busy"] = func() {}
		app.streamsMu.Unlock()
		err := app.InstallUpdate()
		if err == nil || !strings.Contains(err.Error(), "running") {
			t.Fatalf("err = %v, want busy refusal", err)
		}
	})
	t.Run("no cached check refuses before any download", func(t *testing.T) {
		app := newUpdateTestApp(t, "http://updates.test/manifest.json", func(req *http.Request) (*http.Response, error) {
			t.Fatal("no HTTP request should happen without a cached check")
			return nil, nil
		})
		err := app.InstallUpdate()
		if err == nil || !strings.Contains(err.Error(), "check for updates first") {
			t.Fatalf("err = %v, want the no-update refusal", err)
		}
	})
}

func TestBeforeCloseUpdaterBypass(t *testing.T) {
	app := NewApp()
	app.updaterQuitting.Store(true)
	if prevent := app.beforeClose(context.Background()); prevent {
		t.Fatal("updater quit must bypass the confirmation dialog")
	}
}

func TestDownloadUpdateArtifact(t *testing.T) {
	payload := "fake-zip-bytes"
	hasher := sha256.New()
	hasher.Write([]byte(payload))
	sum := hex.EncodeToString(hasher.Sum(nil))

	serve := func(status int, body string, lengthCap int64) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/zip" {
				return httpResponse(404, "text/plain", "not found"), nil
			}
			var reader io.Reader = strings.NewReader(body)
			if lengthCap > 0 {
				reader = io.LimitReader(strings.NewReader(body), lengthCap)
			}
			return &http.Response{
				StatusCode: status,
				Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
				Header:     http.Header{"Content-Type": []string{"application/zip"}},
				Body:       io.NopCloser(reader),
			}, nil
		}
	}

	t.Run("happy path writes the verified file", func(t *testing.T) {
		client := &http.Client{Transport: serve(200, payload, 0)}
		dest := filepath.Join(t.TempDir(), "staged", "update.zip")
		if err := downloadUpdateArtifact(context.Background(), client, updateArtifact{URL: "http://updates.test/zip", SHA256: sum, Size: int64(len(payload))}, dest); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != payload {
			t.Fatalf("downloaded %q, want %q", data, payload)
		}
		if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
			t.Fatal("partial file should be renamed away on success")
		}
	})
	t.Run("checksum mismatch aborts and leaves no file", func(t *testing.T) {
		client := &http.Client{Transport: serve(200, payload, 0)}
		dest := filepath.Join(t.TempDir(), "staged", "update.zip")
		err := downloadUpdateArtifact(context.Background(), client, updateArtifact{URL: "http://updates.test/zip", SHA256: "deadbeef", Size: int64(len(payload))}, dest)
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("err = %v, want checksum mismatch", err)
		}
		for _, path := range []string{dest, dest + ".part"} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("%s should not exist after a failed download", path)
			}
		}
	})
	t.Run("truncated download fails the size check", func(t *testing.T) {
		client := &http.Client{Transport: serve(200, payload, 3)}
		dest := filepath.Join(t.TempDir(), "staged", "update.zip")
		err := downloadUpdateArtifact(context.Background(), client, updateArtifact{URL: "http://updates.test/zip", SHA256: sum, Size: int64(len(payload))}, dest)
		if err == nil || !strings.Contains(err.Error(), "size mismatch") {
			t.Fatalf("err = %v, want size mismatch", err)
		}
	})
	t.Run("http failure surfaces", func(t *testing.T) {
		client := &http.Client{Transport: serve(403, "denied", 0)}
		dest := filepath.Join(t.TempDir(), "staged", "update.zip")
		err := downloadUpdateArtifact(context.Background(), client, updateArtifact{URL: "http://updates.test/zip", SHA256: sum}, dest)
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("err = %v, want the status surfaced", err)
		}
	})
}
