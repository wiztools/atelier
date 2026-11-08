package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) SaveImage(req SaveImageRequest) (string, error) {
	data, extension, err := decodeImagePayload(req.Image)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-image", extension, "Save generated image", []runtime.FileFilter{
		{DisplayName: "Image Files", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif;*.heic;*.avif;*.tiff;*.bmp;*.jp2"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveVideo copies a generated video artifact to a user-chosen location. The
// frontend passes the path the asset handler served the video from (either a
// bare filesystem path or the "/atelier-artifact"-prefixed URL); this strips the
// prefix, confirms the bytes are a playable video, and streams a Save dialog
// copy. Returns the chosen path, or "" if the dialog was cancelled.
func (a *App) SaveVideo(req SaveVideoRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("video path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isVideoBytes(data) {
		return "", errors.New("artifact is not a supported video")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp4"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-video", extension, "Save generated video", []runtime.FileFilter{
		{DisplayName: "Video Files", Pattern: "*.mp4;*.webm;*.mov;*.m4v"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveAudio copies a generated audio artifact to a user-chosen location,
// mirroring SaveVideo.
func (a *App) SaveAudio(req SaveAudioRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("audio path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isAudioBytes(data) {
		return "", errors.New("artifact is not a supported audio clip")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp3"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-audio", extension, "Save generated audio", []runtime.FileFilter{
		{DisplayName: "Audio Files", Pattern: "*.mp3;*.wav;*.ogg;*.flac;*.m4a;*.aac;*.opus"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveTranscript copies a transcript artifact (.vtt timestamped or .txt plain)
// to a user-chosen location, mirroring SaveVideo/SaveAudio. The guard is a
// non-empty-text check rather than the media savers' byte sniffing — there is
// no container magic to verify.
func (a *App) SaveTranscript(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("transcript path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", errors.New("transcript artifact is empty")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension != ".vtt" && extension != ".txt" && extension != ".srt" {
		extension = ".txt"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-transcript", extension, "Save transcript", []runtime.FileFilter{
		{DisplayName: "Transcript & Subtitle Files", Pattern: "*.vtt;*.txt;*.srt"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// saveArtifactDialog is the shared body for SaveImage/SaveVideo/SaveAudio: it
// builds a filename (defaultName + extension when the user gave none, ensuring
// the extension is present), opens a Save dialog with the given title/filters,
// and writes data to the chosen path. Each caller keeps its own source-loading
// and byte-sniffing; this owns the filename-guard → dialog → write ritual they
// all shared. Returns the chosen path, or ("", nil) if the dialog was cancelled.
func (a *App) saveArtifactDialog(data []byte, suggestedName, defaultName, extension, title string, filters []runtime.FileFilter) (string, error) {
	filename := sanitizeFilename(suggestedName)
	if filename == "" {
		filename = defaultName + extension
	}
	if !strings.HasSuffix(strings.ToLower(filename), extension) {
		filename += extension
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: filename,
		Filters:         filters,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// RevealFile shows a saved file in the platform's file manager — the save
// toasts' "Show in Finder" action. macOS selects the file in a Finder window
// (open -R); elsewhere the containing folder opens (xdg-open cannot select a
// file, and Explorer's /select needs raw command-line quoting Go's argument
// escaping can't express). The path comes from a save dialog's result and
// must still exist: a file moved or deleted since is reported, not answered
// with a stale folder.
func (a *App) RevealFile(path string) error {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return fmt.Errorf("can't show %q: not an absolute path", path)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is no longer there — it was moved or deleted", filepath.Base(path))
		}
		return err
	}
	name, args := revealFileCommand(runtimeGOOS, path)
	return revealFileStart(name, args)
}

// revealFileCommand is the platform's file-manager invocation for path.
func revealFileCommand(goos, path string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{"-R", path}
	case "windows":
		return "explorer", []string{filepath.Dir(path)}
	default:
		return "xdg-open", []string{filepath.Dir(path)}
	}
}

// revealFileStart launches the file manager without waiting on it (Explorer
// exits nonzero even on success) and reaps it in the background so no zombie
// outlives it. The test seam: tests swap it to record the invocation.
var revealFileStart = func(name string, args []string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
