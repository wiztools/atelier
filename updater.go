package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// defaultUpdateManifestURL is GitHub's stable "latest release asset" redirect:
// it resolves to the update-manifest.json attached to the newest non-draft,
// non-prerelease release, so the URL never changes between releases and
// prereleases stay invisible to the updater (stable-channel semantics for
// free). release.sh attaches the manifest to every release in the same
// `gh release create` command as the artifacts, so a release cannot exist
// without its feed.
const defaultUpdateManifestURL = "https://github.com/wiztools/atelier/releases/latest/download/update-manifest.json"

// updateCheckInterval is the minimum spacing between automatic update checks.
// The last successful check is persisted in state.json so daily relaunchers
// check once a day rather than once per launch; sessions that stay open
// re-check on a ticker of the same period.
const updateCheckInterval = 24 * time.Hour

const (
	updateStateCurrent   = "current"
	updateStateAvailable = "available"
	updateStateError     = "error"
)

// updateManifest is the release feed document (update-manifest.json). It is
// shaped multi-platform from day one — platforms keyed "darwin-universal"
// today — so Windows and Linux entries later need no format migration.
type updateManifest struct {
	Version     string                    `json:"version"`
	Notes       string                    `json:"notes,omitempty"`
	PublishedAt string                    `json:"publishedAt,omitempty"`
	Platforms   map[string]updateArtifact `json:"platforms"`
}

type updateArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// artifact returns this platform's entry, validating that it is usable.
func (m *updateManifest) artifact() (updateArtifact, error) {
	key := updatePlatformKey()
	entry, ok := m.Platforms[key]
	if !ok {
		return updateArtifact{}, fmt.Errorf("update manifest has no artifact for %s", key)
	}
	if strings.TrimSpace(entry.URL) == "" || strings.TrimSpace(entry.SHA256) == "" {
		return updateArtifact{}, fmt.Errorf("update manifest entry for %s is missing url or sha256", key)
	}
	return entry, nil
}

// UpdateStatus is the CheckForUpdates result, mirroring CheckOllama's
// status-struct style: errors ride in the payload so the frontend renders one
// shape for every outcome.
type UpdateStatus struct {
	State          string `json:"state"` // current | available | error
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	Notes          string `json:"notes,omitempty"`
	Error          string `json:"error,omitempty"`
}

// UpdateAvailableEvent is the atelier:update-available payload, raised by both
// the automatic startup/ticker checks and the manual Settings/menu check so
// the banner has one source.
type UpdateAvailableEvent struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Notes   string `json:"notes,omitempty"`
}

func (a *App) emitUpdateAvailable(event UpdateAvailableEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "atelier:update-available", event)
	}
}

// semver is a parsed semantic version. Build metadata is discarded at parse
// time (it never participates in precedence).
type semver struct {
	major uint64
	minor uint64
	patch uint64
	pre   string
}

// parseSemver parses "1.2.3", "v1.2.3-rc.1+build" style versions. The v prefix
// is tolerated (release tags are v-prefixed), build metadata is dropped, and
// the prerelease segment keeps semver precedence rules.
func parseSemver(s string) (semver, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return semver{}, errors.New("empty version")
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("version %q is not semver (want major.minor.patch)", s)
	}
	var v semver
	var err error
	if v.major, err = strconv.ParseUint(parts[0], 10, 64); err != nil {
		return semver{}, fmt.Errorf("version major %q is not numeric", parts[0])
	}
	if v.minor, err = strconv.ParseUint(parts[1], 10, 64); err != nil {
		return semver{}, fmt.Errorf("version minor %q is not numeric", parts[1])
	}
	if v.patch, err = strconv.ParseUint(parts[2], 10, 64); err != nil {
		return semver{}, fmt.Errorf("version patch %q is not numeric", parts[2])
	}
	v.pre = pre
	return v, nil
}

// compareSemver returns >0 when a outranks b per semver precedence, including
// prerelease rules: a version without a prerelease outranks one with it, and
// prerelease identifiers compare numerically when both are numeric, lexically
// otherwise, with numeric identifiers ranking below alphanumeric ones.
func compareSemver(a, b semver) int {
	if a.major != b.major {
		return cmpUint64(a.major, b.major)
	}
	if a.minor != b.minor {
		return cmpUint64(a.minor, b.minor)
	}
	if a.patch != b.patch {
		return cmpUint64(a.patch, b.patch)
	}
	if a.pre == b.pre {
		return 0
	}
	if a.pre == "" {
		return 1
	}
	if b.pre == "" {
		return -1
	}
	aidentifiers := strings.Split(a.pre, ".")
	bidentifiers := strings.Split(b.pre, ".")
	for i := 0; i < len(aidentifiers) && i < len(bidentifiers); i++ {
		if c := comparePrereleaseIdentifier(aidentifiers[i], bidentifiers[i]); c != 0 {
			return c
		}
	}
	return cmpUint64(uint64(len(aidentifiers)), uint64(len(bidentifiers)))
}

func cmpUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func comparePrereleaseIdentifier(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return cmpUint64(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// isReleasedVersion reports whether the running build carries a real version —
// i.e. was built by release.sh's ldflags injection rather than wails dev or a
// plain wails build (both leave version at "dev").
func isReleasedVersion(v string) bool {
	_, err := parseSemver(v)
	return err == nil
}

// latestVersionIsNewer reports whether the manifest's version outranks the
// running one. A current version that doesn't parse — "dev" or a from-source
// build — loses to any parseable release, so dev builds can see (and install)
// updates on demand while the automatic path stays quiet.
func latestVersionIsNewer(current, latest string) bool {
	currentV, currentErr := parseSemver(current)
	latestV, latestErr := parseSemver(latest)
	if latestErr != nil {
		return false
	}
	if currentErr != nil {
		return true
	}
	return compareSemver(latestV, currentV) > 0
}

// updateStateDir is the updater's persistent corner, deliberately NOT under
// the configurable Storage.Root: check state must survive storage relocation
// and must never race the frontend's debounced config auto-save.
func updateStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier", "updates"), nil
}

// updateCheckState is state.json: just the last successful check time.
type updateCheckState struct {
	LastCheckAt string `json:"lastCheckAt"`
}

func readUpdateCheckState() updateCheckState {
	dir, err := updateStateDir()
	if err != nil {
		return updateCheckState{}
	}
	var state updateCheckState
	if err := readJSONFile(filepath.Join(dir, "state.json"), &state); err != nil {
		return updateCheckState{}
	}
	return state
}

func writeUpdateCheckState(state updateCheckState) error {
	dir, err := updateStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "state.json"), state)
}

// updateCheckDue reports whether an automatic check is warranted: no recorded
// check, an unparsable record, or one older than updateCheckInterval. Only
// successful checks record a timestamp, so a transient network failure at
// launch retries on the next launch.
func updateCheckDue(now time.Time) bool {
	last := strings.TrimSpace(readUpdateCheckState().LastCheckAt)
	if last == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return now.Sub(parsed) >= updateCheckInterval
}

// fetchUpdateManifest loads the feed from the configured (default: GitHub
// Releases) URL and validates that it is usable for this platform.
func (a *App) fetchUpdateManifest(ctx context.Context) (*updateManifest, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	url := strings.TrimSpace(config.Updates.ManifestURL)
	if url == "" {
		url = defaultUpdateManifestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// GitHub's releases/latest/download URL answers with a redirect to the
	// asset host; the default client follows redirects.
	resp, err := a.updateClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("update manifest returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var manifest updateManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("update manifest is not valid JSON: %w", err)
	}
	if _, err := parseSemver(manifest.Version); err != nil {
		return nil, fmt.Errorf("update manifest version %q is not semver", manifest.Version)
	}
	if _, err := manifest.artifact(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// checkForUpdates is the one check path shared by the manual button/menu entry
// and the automatic scheduler: fetch, record the check, compare, cache for
// InstallUpdate, and (when newer and notify is set) raise the banner event.
// Errors come back in the status, never as a second return.
func (a *App) checkForUpdates(ctx context.Context, notify bool) UpdateStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	current := strings.TrimSpace(version)
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	manifest, err := a.fetchUpdateManifest(fetchCtx)
	cancel()
	if err != nil {
		return UpdateStatus{State: updateStateError, CurrentVersion: current, Error: err.Error()}
	}
	// Record only successful checks (see updateCheckDue).
	_ = writeUpdateCheckState(updateCheckState{LastCheckAt: time.Now().UTC().Format(time.RFC3339)})

	status := UpdateStatus{
		State:          updateStateCurrent,
		CurrentVersion: current,
		LatestVersion:  manifest.Version,
		Notes:          manifest.Notes,
	}
	if latestVersionIsNewer(current, manifest.Version) {
		status.State = updateStateAvailable
		a.updatesMu.Lock()
		a.lastUpdate = manifest
		a.updatesMu.Unlock()
		if notify {
			a.emitUpdateAvailable(UpdateAvailableEvent{
				Current: current,
				Latest:  manifest.Version,
				Notes:   manifest.Notes,
			})
		}
	} else {
		a.updatesMu.Lock()
		a.lastUpdate = nil
		a.updatesMu.Unlock()
	}
	return status
}

func (a *App) cachedUpdateManifest() *updateManifest {
	a.updatesMu.Lock()
	defer a.updatesMu.Unlock()
	return a.lastUpdate
}

func updateAutoCheckEnabled() bool {
	config, err := loadAppConfig()
	if err != nil {
		return true
	}
	if config.Updates.AutoCheck == nil {
		return true
	}
	return *config.Updates.AutoCheck
}

// startUpdateScheduler drives the automatic checks: a settled startup check
// (gated on the 24h window) plus a re-check ticker for sessions that stay
// open. Dev builds skip the automatic path entirely so wails dev never nags —
// the manual Settings/menu check remains available. Failures are silent here;
// only the manual path surfaces them.
func (a *App) startUpdateScheduler(ctx context.Context) {
	cleanupStaleUpdateStaging()
	if !isReleasedVersion(version) {
		return
	}
	go func() {
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
		if updateCheckDue(time.Now()) && updateAutoCheckEnabled() {
			_ = a.checkForUpdates(ctx, true)
		}
		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if updateAutoCheckEnabled() {
					_ = a.checkForUpdates(ctx, true)
				}
			}
		}
	}()
}

// downloadUpdateArtifact streams the artifact to destPath, enforcing the
// manifest's size cap while copying and verifying the SHA256 before
// returning. The hash is computed mid-copy (MultiWriter), so a tampered or
// truncated download is rejected without a second pass, and the partial file
// never appears at its final name.
func downloadUpdateArtifact(ctx context.Context, client *http.Client, artifact updateArtifact, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("update download returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	var body io.Reader = resp.Body
	if artifact.Size > 0 {
		// Cap at size+1 so a lying server can't stream forever; the exact
		// length is checked after the copy.
		body = io.LimitReader(resp.Body, artifact.Size+1)
	}
	tempPath := destPath + ".part"
	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), body)
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tempPath)
		return copyErr
	}
	if artifact.Size > 0 && written != artifact.Size {
		os.Remove(tempPath)
		return fmt.Errorf("update download size mismatch: expected %d bytes, got %d", artifact.Size, written)
	}
	expected := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	actual := hex.EncodeToString(hasher.Sum(nil))
	if expected == "" {
		os.Remove(tempPath)
		return errors.New("update artifact has no sha256")
	}
	if actual != expected {
		os.Remove(tempPath)
		return fmt.Errorf("update checksum mismatch: expected %s, got %s", expected, actual)
	}
	return os.Rename(tempPath, destPath)
}
