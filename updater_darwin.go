//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// updatePlatformKey selects this build's manifest entry. release.sh builds
// universal binaries, so darwin has a single artifact covering both arches.
func updatePlatformKey() string {
	return "darwin-universal"
}

// cleanupStaleUpdateStaging removes leftover staging dirs at launch. None can
// belong to an in-flight update by the time a new launch runs — the previous
// session's install either completed or died with it — so sweeping them all is
// safe and reclaims the ~200MB a half-finished download leaves behind.
func cleanupStaleUpdateStaging() {
	dir, err := updateStateDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "staged-") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// currentAppBundlePath resolves the running .app bundle from the executable
// path (.../Atelier.app/Contents/MacOS/Atelier). It errors for the raw-binary
// development case (wails dev) — the guard runs before any download, so a dev
// session can check for updates but never half-installs one.
func currentAppBundlePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return "", errors.New("Atelier is not running from a packaged app (development mode); install a release build to self-update")
	}
	return bundle, nil
}

// makeUpdateStagingDir creates a fresh staging directory for one update.
func makeUpdateStagingDir() (string, error) {
	dir, err := updateStateDir()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(dir, "staged-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(staging, 0755); err != nil {
		return "", err
	}
	return staging, nil
}

// readBundleShortVersion reads CFBundleShortVersionString via plutil (handles
// both XML and binary plists).
func readBundleShortVersion(appPath string) (string, error) {
	out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString", "raw", filepath.Join(appPath, "Contents", "Info.plist")).Output()
	if err != nil {
		return "", fmt.Errorf("could not read staged app's version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// parseTeamIdentifier extracts TeamIdentifier= from `codesign -dv` output.
// Ad-hoc signatures report "TeamIdentifier=not set", which maps to "" — the
// caller treats an empty id as "unpinned".
func parseTeamIdentifier(codesignOutput string) string {
	for _, line := range strings.Split(codesignOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TeamIdentifier=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "TeamIdentifier="))
		if value == "" || value == "not set" {
			return ""
		}
		return value
	}
	return ""
}

// bundleTeamIdentifier returns the bundle's signing team, "" for ad-hoc or
// unknown. codesign -dv reports to stderr.
func bundleTeamIdentifier(appPath string) string {
	cmd := exec.Command("codesign", "-dv", "--verbose=4", appPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return parseTeamIdentifier(string(out))
}

// verifyStagedAppBundle authenticates the downloaded bundle before anything is
// swapped. The checks are layered: the cryptographic ones (codesign, Team ID)
// prove the artifact is ours; the plist version check proves it is the build
// the manifest claims — a stale-but-properly-signed zip passes crypto and
// still gets rejected here.
func verifyStagedAppBundle(stagedApp string, manifestVersion string, ownBundlePath string) error {
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", stagedApp).CombinedOutput(); err != nil {
		return fmt.Errorf("staged update failed signature verification: %s", strings.TrimSpace(string(out)))
	}
	plistVersion, err := readBundleShortVersion(stagedApp)
	if err != nil {
		return err
	}
	if plistVersion != manifestVersion {
		return fmt.Errorf("staged update reports version %s but the manifest promised %s", plistVersion, manifestVersion)
	}
	ownTeam := bundleTeamIdentifier(ownBundlePath)
	if ownTeam == "" {
		// Dev/ad-hoc running build: no team to pin against (the staged
		// bundle still had to pass codesign --verify above).
		return nil
	}
	stagedTeam := bundleTeamIdentifier(stagedApp)
	if stagedTeam != ownTeam {
		return fmt.Errorf("staged update was signed by team %q, expected %q", stagedTeam, ownTeam)
	}
	return nil
}

// shellQuote single-quotes s for /bin/sh, escaping any embedded quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildInstallHelperScript renders the post-quit swap script. It waits for the
// old PID, moves the current bundle aside, moves the new one into the same
// path, relaunches, and only then deletes the backup — so at every instant a
// complete bundle exists either at the target path or as previous.app.
func buildInstallHelperScript(stagingDir, stagedApp, targetApp string, pid int) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Atelier self-update helper — generated at runtime.\n")
	b.WriteString("# Runs detached (setsid) and outlives the app; all output lands in update.log.\n")
	b.WriteString("set -u\n")
	b.WriteString("LOG=" + shellQuote(filepath.Join(stagingDir, "update.log")) + "\n")
	b.WriteString("APP_PATH=" + shellQuote(targetApp) + "\n")
	b.WriteString("STAGED_APP=" + shellQuote(stagedApp) + "\n")
	b.WriteString("STAGING=" + shellQuote(stagingDir) + "\n")
	b.WriteString("UPDATES_DIR=" + shellQuote(filepath.Dir(stagingDir)) + "\n")
	b.WriteString("PID=" + strconv.Itoa(pid) + "\n")
	b.WriteString(`
log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG" 2>&1; }

log "waiting for pid $PID to exit"
while kill -0 "$PID" 2>/dev/null; do sleep 1; done

log "moving current app aside"
if ! mv "$APP_PATH" "$STAGING/previous.app"; then
  log "FATAL: could not move current app aside; aborting (nothing was changed)"
  exit 1
fi

log "moving new app into place"
if ! mv "$STAGED_APP" "$APP_PATH"; then
  log "FATAL: install failed — restoring previous app"
  mv "$STAGING/previous.app" "$APP_PATH" || log "could not restore; previous app remains at $STAGING/previous.app"
  exit 1
fi

# Tidy and preserve the log BEFORE relaunching: the new instance's startup
# sweeps stale staging dirs, so relaunching first would race these lines and
# the evidence would vanish with the sweep.
rm -rf "$STAGING/previous.app"
log "update complete"
log "launching updated app"
cp -f "$LOG" "$UPDATES_DIR/last-update.log" || true
if open "$APP_PATH"; then
  exit 0
fi
echo "$(date '+%Y-%m-%d %H:%M:%S') could not relaunch; start Atelier manually" >> "$UPDATES_DIR/last-update.log"
`)
	return b.String()
}

// launchInstallHelper writes and spawns the swap script, detached from the
// app's session so it survives the quit, with its output captured to
// update.log. Callers must treat a nil return as "the swap is happening: quit
// now".
func launchInstallHelper(stagingDir, stagedApp, targetApp string, pid int) error {
	scriptPath := filepath.Join(stagingDir, "install.sh")
	script := buildInstallHelperScript(stagingDir, stagedApp, targetApp, pid)
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stagingDir, "update.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Dir = stagingDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Deliberately never Wait: the helper must outlive this process.
	return nil
}

// applySelfUpdate is the darwin install: guard, download, verify, stage the
// swap, and hand off to the detached helper. A nil return means the helper is
// running and the caller must quit immediately — the bundle at targetApp is
// about to be replaced.
func applySelfUpdate(client *http.Client, manifest *updateManifest) error {
	bundlePath, err := currentAppBundlePath()
	if err != nil {
		return err
	}
	// Gatekeeper translocation: a quarantined app launched from Downloads
	// runs from a randomized read-only path; swapping there either fails or
	// updates a path the user can't find again.
	if strings.Contains(bundlePath, "AppTranslocation") {
		return errors.New("Atelier is running from a temporary translocated location; move it to /Applications and try again")
	}
	// Same-class guard: the swap needs write access to the bundle's parent.
	probe, err := os.CreateTemp(filepath.Dir(bundlePath), ".atelier-update-probe")
	if err != nil {
		return errors.New("Atelier's folder is not writable; move it to /Applications and try again")
	}
	probe.Close()
	os.Remove(probe.Name())

	artifact, err := manifest.artifact()
	if err != nil {
		return err
	}
	stagingDir, err := makeUpdateStagingDir()
	if err != nil {
		return err
	}
	zipPath := filepath.Join(stagingDir, "update.zip")
	if err := downloadUpdateArtifact(context.Background(), client, artifact, zipPath); err != nil {
		os.RemoveAll(stagingDir)
		return err
	}
	// ditto preserves symlinks, permissions, and extended attributes —
	// archive/zip does not.
	if out, err := exec.Command("ditto", "-x", "-k", zipPath, stagingDir).CombinedOutput(); err != nil {
		os.RemoveAll(stagingDir)
		return fmt.Errorf("could not extract update archive: %s", strings.TrimSpace(string(out)))
	}
	stagedApp, err := findStagedAppBundle(stagingDir)
	if err != nil {
		os.RemoveAll(stagingDir)
		return err
	}
	if err := verifyStagedAppBundle(stagedApp, manifest.Version, bundlePath); err != nil {
		os.RemoveAll(stagingDir)
		return err
	}
	if err := launchInstallHelper(stagingDir, stagedApp, bundlePath, os.Getpid()); err != nil {
		os.RemoveAll(stagingDir)
		return err
	}
	return nil
}

// findStagedAppBundle locates the single top-level .app in the extracted
// archive (the release zip is built with ditto --keepParent so the bundle is
// the archive root).
func findStagedAppBundle(stagingDir string) (string, error) {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return "", err
	}
	var candidates []fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".app") {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("expected exactly one .app bundle in the update archive, found %d", len(candidates))
	}
	return filepath.Join(stagingDir, candidates[0].Name()), nil
}
