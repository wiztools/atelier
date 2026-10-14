package main

// media_upload.go stages large user-attached audio/video files on disk BEFORE a
// turn exists. The attachment path (drop or file picker) can only hand the
// frontend an opaque File blob — no filesystem path — so a 700MB clip would
// otherwise have to cross the Wails IPC bridge as one ~933MB base64 data URL:
// the WebView JSON-stringifies it, the bridge copies it, Go decodes it, and
// every step chokes long before a conversation is even created. Instead the
// frontend streams the file in base64 chunks through the bound methods below
// (each call a few MB), and the completed upload is served back as a
// /atelier-artifact/ URL — the same URL shape history hydration already uses —
// so it renders, plays, and rides the request as a short string. At turn
// persistence the staged file is linked into the conversation's artifacts
// directory (historyContentForMessage) while the staged entry stays put — the
// request's own payload references it for the rest of the turn — and every
// consumer of media payloads (local CLI tools, fal, Replicate) resolves the
// artifact URL to the bytes on disk. Files staged but never sent (a dropped
// attachment removed from the composer, a crashed session) are reclaimed by
// the age-based sweep.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// stagedUploadIDPrefix marks files inside the uploads directory as staged
// uploads (randomID appends its own separator, so "up" lands as "up_<hex>").
// The frontend's artifact-URL helpers use the vid_/aud_/img_ prefixes
// to decide whether a URL points at a persisted conversation artifact, so a
// distinct prefix keeps a not-yet-adopted upload from offering editor or
// download actions against ids no conversation owns.
const stagedUploadIDPrefix = "up"

// stagedUploadMaxAge bounds how long a staged-but-never-sent upload survives.
// Generous on purpose: an app left open for days with an unsent attachment
// still references its staged file, and the sweep must not delete it.
const stagedUploadMaxAge = 7 * 24 * time.Hour

// MediaUploadSession is the in-progress state for one staged file. Chunk
// appends are open/append/close per call — stateless against bridge hiccups
// and simple to reason about; the per-chunk open cost is nothing next to the
// transfer itself.
type MediaUploadSession struct {
	path    string
	kind    string
	created time.Time
}

// StagedMediaUpload is the completed upload the frontend attaches to the
// composer. URL is the /atelier-artifact/ form the transcript renders and the
// request carries; Path is the on-disk location (the frontend only ever echoes
// it back to DiscardMediaUpload).
type StagedMediaUpload struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

// mediaUploadsDir is where staged uploads live until a turn adopts them. It
// sits under the history root so it is inside the app's own data directory,
// next to the conversations it feeds.
func mediaUploadsDir(storage ConfigStorage) string {
	return filepath.Join(storage.History, "uploads")
}

// mediaUploadExtension picks the staged file's extension from the original
// filename when it plausibly belongs to the kind, falling back to the kind's
// house extension — a misleading or missing extension must not make a video
// land as .mp3. The MIME mapping misses real containers (.mkv, .wma), so a
// per-kind container allowlist rides beside it; extensions that map to another
// kind (.png on a video, .mp3 on an audio) are never kept.
func mediaUploadExtension(name, kind string) string {
	houseExtension, kindPrefix := ".mp4", "video"
	if kind == "audio" {
		houseExtension, kindPrefix = ".mp3", "audio"
	}
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if !regexpMediaUploadExtension.MatchString(extension) {
		return houseExtension
	}
	if strings.HasPrefix(mediaTypeForExtension(extension), kindPrefix+"/") {
		return extension
	}
	containers := mediaUploadVideoContainers
	if kind == "audio" {
		containers = mediaUploadAudioContainers
	}
	if containers[extension] {
		return extension
	}
	return houseExtension
}

// regexpMediaUploadExtension accepts plain file extensions (".mkv") and
// rejects anything a path could hide behind — traversal, separators, dots.
var regexpMediaUploadExtension = regexp.MustCompile(`^\.[a-z0-9]{1,5}$`)

var mediaUploadVideoContainers = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".mkv": true,
	".avi": true, ".ts": true, ".3gp": true, ".mpg": true, ".mpeg": true,
}

var mediaUploadAudioContainers = map[string]bool{
	".mp3": true, ".wav": true, ".ogg": true, ".opus": true, ".flac": true,
	".m4a": true, ".aac": true, ".wma": true, ".aif": true, ".aiff": true,
}

// beginMediaUpload is the shared body of the bound method so tests can drive
// it against a temp storage root without touching App state.
func (a *App) beginMediaUpload(storage ConfigStorage, name, kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind != "video" && kind != "audio" {
		return "", fmt.Errorf("unsupported media upload kind %q (want \"video\" or \"audio\")", kind)
	}
	if a.mediaUploads == nil {
		a.mediaUploads = map[string]*MediaUploadSession{}
	}
	uploadsDir := mediaUploadsDir(storage)
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		return "", err
	}
	sweepStaleMediaUploads(uploadsDir)
	uploadID := randomID(stagedUploadIDPrefix)
	path := filepath.Join(uploadsDir, uploadID+mediaUploadExtension(name, kind))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	file.Close()
	a.mediaUploadsMu.Lock()
	a.mediaUploads[uploadID] = &MediaUploadSession{path: path, kind: kind, created: time.Now()}
	a.mediaUploadsMu.Unlock()
	return uploadID, nil
}

// BeginMediaUpload opens a staged upload for a large media attachment and
// returns the upload ID the chunk calls address. name only shapes the file
// extension; kind decides validation and the fallback extension.
func (a *App) BeginMediaUpload(name string, kind string) (string, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return "", err
	}
	return a.beginMediaUpload(config.Storage, name, kind)
}

// AppendMediaUploadChunk decodes one base64 chunk and appends it to the staged
// file. The chunk carries raw-file bytes in attachment order.
func (a *App) AppendMediaUploadChunk(uploadID string, chunk string) error {
	session, err := a.mediaUploadSession(uploadID)
	if err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(chunk))
	if err != nil {
		return fmt.Errorf("media upload chunk is not valid base64: %w", err)
	}
	file, err := os.OpenFile(session.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// CompleteMediaUpload closes the upload: the bytes are sniffed against the
// declared kind (a mislabeled drop must fail here, not at generation time),
// a video gets its poster frame so the composer-side preview has one, and the
// /atelier-artifact/ URL is returned. The session is consumed either way.
func (a *App) CompleteMediaUpload(uploadID string) (*StagedMediaUpload, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return a.completeMediaUpload(config, uploadID)
}

func (a *App) completeMediaUpload(config AppConfig, uploadID string) (*StagedMediaUpload, error) {
	session, err := a.mediaUploadSession(uploadID)
	if err != nil {
		return nil, err
	}
	a.mediaUploadsMu.Lock()
	delete(a.mediaUploads, uploadID)
	a.mediaUploadsMu.Unlock()

	data, err := os.ReadFile(session.path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("media upload is empty")
	}
	if session.kind == "video" && !isVideoBytes(data) {
		return nil, errors.New("media upload is not a supported video file")
	}
	if session.kind == "audio" && !isAudioBytes(data) {
		return nil, errors.New("media upload is not a supported audio file")
	}
	mediaType := mediaTypeForExtension(strings.ToLower(filepath.Ext(session.path)))
	if session.kind == "video" {
		generateVideoPoster(config, session.path)
	}
	return &StagedMediaUpload{
		Path:      session.path,
		URL:       artifactPrefix + filepath.ToSlash(session.path),
		Size:      int64(len(data)),
		MediaType: mediaType,
	}, nil
}

// DiscardMediaUpload removes a staged upload the composer no longer
// references. It takes either an in-flight upload ID (staging failed
// mid-stream and the frontend is cleaning up) or the staged file's path (the
// attachment chip was deleted after a completed stage). Only files inside the
// uploads directory bearing the staged prefix are eligible — the frontend
// echoes back values it was handed, but the backend keeps the boundary. A
// missing file is success: the common caller is cleanup after the turn already
// adopted (moved) the file.
func (a *App) DiscardMediaUpload(pathOrUploadID string) error {
	return a.discardMediaUpload(mediaUploadsDir(loadStorageOrEmpty()), pathOrUploadID)
}

func (a *App) discardMediaUpload(uploadsDir string, pathOrUploadID string) error {
	key := strings.TrimSpace(pathOrUploadID)
	a.mediaUploadsMu.Lock()
	session, isSession := a.mediaUploads[key]
	if isSession {
		delete(a.mediaUploads, key)
	}
	a.mediaUploadsMu.Unlock()
	stagedPath := ""
	if isSession {
		stagedPath = session.path
	} else if resolved, ok := stagedUploadPath(key, uploadsDir); ok {
		stagedPath = resolved
	}
	if stagedPath == "" {
		return fmt.Errorf("refusing to discard %q: not a staged media upload", pathOrUploadID)
	}
	if err := os.Remove(stagedPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(stagedUploadPosterPath(stagedPath)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (a *App) mediaUploadSession(uploadID string) (*MediaUploadSession, error) {
	uploadID = strings.TrimSpace(uploadID)
	a.mediaUploadsMu.Lock()
	defer a.mediaUploadsMu.Unlock()
	session, ok := a.mediaUploads[uploadID]
	if !ok {
		return nil, fmt.Errorf("unknown media upload %q", uploadID)
	}
	return session, nil
}

// loadStorageOrEmpty is the storage root for path-confinement checks that must
// work even when the config cannot load: a default root still matches the
// default installation layout, and a mismatched root only ever fails closed
// (the path is refused, nothing is deleted).
func loadStorageOrEmpty() ConfigStorage {
	if config, err := loadReadyConfig(); err == nil {
		return config.Storage
	}
	return ConfigStorage{History: filepath.Join(defaultStorageRoot(), "history")}
}

// sweepStaleMediaUploads deletes staged uploads (and their poster siblings)
// older than stagedUploadMaxAge. Best-effort: a file that cannot be removed is
// skipped and retried on a later sweep.
func sweepStaleMediaUploads(uploadsDir string) {
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stagedUploadMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(uploadsDir, entry.Name())
		os.Remove(path)
	}
}

// stagedUploadPath resolves a payload string (an /atelier-artifact/ URL or a
// bare path) to a staged upload inside uploadsDir. The second return names
// whether the payload actually points at one — every caller treats false as
// "not a staged upload", never as an error.
func stagedUploadPath(payload string, uploadsDir string) (string, bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", false
	}
	if prefix := artifactPrefix + "/"; strings.HasPrefix(trimmed, prefix) {
		trimmed = filepath.FromSlash(strings.TrimPrefix(trimmed, artifactPrefix))
	}
	if filepath.Dir(trimmed) != uploadsDir {
		return "", false
	}
	base := filepath.Base(trimmed)
	if !strings.HasPrefix(base, stagedUploadIDPrefix) || strings.Contains(base, "..") {
		return "", false
	}
	return trimmed, true
}

// stagedUploadPosterPath is the poster sibling convention (video_poster.go)
// applied to a staged file: up_<hex>.mp4 → up_<hex>_poster.jpg.
func stagedUploadPosterPath(stagedPath string) string {
	ext := filepath.Ext(stagedPath)
	return strings.TrimSuffix(stagedPath, ext) + "_poster.jpg"
}

// adoptStagedUploadFile links a staged upload into the conversation's
// artifacts directory under its final artifact filename. Link, not move: the
// turn's REQUEST still carries the staged /atelier-artifact/ URL, and the
// tools read their sources from it after persistence — a move would strand
// every tool call on a path that no longer exists (conv_15ecb089: the planner
// called extract_audio and the staged open failed). A second directory entry
// costs no bytes — the staged name is reclaimed by the age sweep — and
// DiscardMediaUpload stays able to remove it. The copy fallback covers the
// impossible cross-device case and likewise keeps the source.
func adoptStagedUploadFile(stagedPath, artifactPath string) error {
	if err := os.Link(stagedPath, artifactPath); err == nil {
		return nil
	}
	source, err := os.Open(stagedPath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.Create(artifactPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

// adoptStagedUpload links a staged upload into the conversation's artifacts
// directory and returns its HistoryContent entry plus the adopted file's
// absolute path (the caller turns it into the hydrated artifact URL).
// kindTag is the artifact id prefix ("vid" or "aud"); content Type is the
// full word. The staged extension is kept when it maps to the kind's media
// type, else the house extension lands (the decode path's behavior). A video's
// staged poster sibling links with it; when there is none (the Complete-time
// generation failed) one is generated here, best-effort per the
// video_poster.go contract.
func adoptStagedUpload(config AppConfig, stagedPath, artifactsDir, kindTag string) (HistoryContent, string, error) {
	kindPrefix, houseExtension := "video", ".mp4"
	if kindTag == "aud" {
		kindPrefix, houseExtension = "audio", ".mp3"
	}
	extension := strings.ToLower(filepath.Ext(stagedPath))
	if !strings.HasPrefix(mediaTypeForExtension(extension), kindPrefix+"/") {
		extension = houseExtension
	}
	artifactID := randomID(kindTag)
	filename := artifactID + extension
	artifactPath := filepath.Join(artifactsDir, filename)
	if err := adoptStagedUploadFile(stagedPath, artifactPath); err != nil {
		return HistoryContent{}, "", err
	}
	if kindPrefix == "video" {
		posterName := strings.TrimSuffix(filename, extension) + "_poster.jpg"
		if !adoptStagedUploadPoster(stagedPath, filepath.Join(artifactsDir, posterName)) {
			generateVideoPoster(config, artifactPath)
		}
	}
	return HistoryContent{
		Type:       kindPrefix,
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   mediaTypeForExtension(extension),
	}, artifactPath, nil
}

// adoptStagedUploadPoster links the staged file's poster sibling beside the
// adopted artifact; reports whether one linked. historyContentForMessage
// generates a poster when this returns false.
func adoptStagedUploadPoster(stagedPath, posterPath string) bool {
	stagedPoster := stagedUploadPosterPath(stagedPath)
	if _, err := os.Stat(stagedPoster); err != nil {
		return false
	}
	return adoptStagedUploadFile(stagedPoster, posterPath) == nil
}
