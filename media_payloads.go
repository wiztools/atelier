package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func decodeImagePayload(image string) ([]byte, string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, "", errors.New("image data is empty")
	}

	// A conversation loaded from history renders its images as "/atelier-artifact"
	// URLs served from disk rather than inline data URLs (hydrateHistoryContent),
	// so resolve those back to bytes on disk — mirroring SaveVideo/SaveAudio.
	if sourcePath := strings.TrimPrefix(image, artifactPrefix); sourcePath != image {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isImageBytes(data) {
			return nil, "", errors.New("artifact is not a supported image")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".png"
		}
		return data, extension, nil
	}

	extension := ".png"
	if strings.HasPrefix(image, "data:image/") {
		headerEnd := strings.Index(image, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("image data URL is missing payload")
		}
		mediaType := image[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = extensionForMediaType(mediaType)
		image = image[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return nil, "", err
	}
	if !isImageBytes(data) {
		return nil, "", errors.New("payload is not a supported image")
	}
	// Bare base64 (the frontend's attachment shape — no data: header carrying
	// the media type) falls through with the .png default, so derive the real
	// extension from the sniffed bytes. A HEIF attachment must persist as .heic,
	// and a JPEG must not be mislabeled .png: the artifact's extension becomes
	// its MIME on later turns (readArtifactAsDataURL) and the staged file name
	// the local CLI tools hand to sips/ffmpeg.
	if sniffed := imageExtensionForBytes(data); sniffed != "" {
		extension = sniffed
	}
	return data, extension, nil
}

// decodeAudioPayload mirrors decodeImagePayload for user-attached audio. It
// accepts a data:audio/<fmt>;base64,... data URL (what the frontend always
// sends) or a hydrated /atelier-artifact/ path (referencing a persisted audio
// artifact), and returns the raw bytes plus a file extension. Bytes are
// validated with isAudioBytes so a non-audio payload is rejected rather than
// written as a broken artifact. Unlike images there is no bare-base64 fallback:
// audio input is OpenRouter-only and openRouterInputAudio handles its own
// normalization at the adapter boundary.
func decodeAudioPayload(audio string) ([]byte, string, error) {
	audio = strings.TrimSpace(audio)
	if audio == "" {
		return nil, "", errors.New("audio data is empty")
	}

	// A conversation reloaded from history references audio artifacts via the
	// /atelier-artifact/ URL prefix (hydrateHistoryContent); resolve those back
	// to bytes on disk, mirroring decodeImagePayload / SaveAudio.
	if sourcePath := strings.TrimPrefix(audio, artifactPrefix); sourcePath != audio {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isAudioBytes(data) {
			return nil, "", errors.New("artifact is not a supported audio file")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".mp3"
		}
		return data, extension, nil
	}

	extension := ".mp3"
	if strings.HasPrefix(audio, "data:audio/") {
		headerEnd := strings.Index(audio, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("audio data URL is missing payload")
		}
		mediaType := audio[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = audioExtensionForMediaType(mediaType)
		audio = audio[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		return nil, "", err
	}
	if !isAudioBytes(data) {
		return nil, "", errors.New("payload is not a supported audio file")
	}
	return data, extension, nil
}

// decodeVideoPayload mirrors decodeAudioPayload for user-attached video. It
// accepts a data:video/<fmt>;base64,... data URL (what the frontend always
// sends) or a hydrated /atelier-artifact/ path (referencing a persisted video
// artifact), and returns the raw bytes plus a file extension. Bytes are
// validated with isVideoBytes so a non-video payload is rejected rather than
// written as a broken artifact. Like audio, there is no bare-base64 fallback:
// video input is tool-only and the harness resolves AttachedVideos from the data
// URL the frontend sends (or a re-encoded artifact on history fallback).
func decodeVideoPayload(video string) ([]byte, string, error) {
	video = strings.TrimSpace(video)
	if video == "" {
		return nil, "", errors.New("video data is empty")
	}

	// A conversation reloaded from history references video artifacts via the
	// /atelier-artifact/ URL prefix (hydrateHistoryContent); resolve those back
	// to bytes on disk, mirroring decodeAudioPayload.
	if sourcePath := strings.TrimPrefix(video, artifactPrefix); sourcePath != video {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isVideoBytes(data) {
			return nil, "", errors.New("artifact is not a supported video file")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".mp4"
		}
		return data, extension, nil
	}

	extension := ".mp4"
	if strings.HasPrefix(video, "data:video/") {
		headerEnd := strings.Index(video, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("video data URL is missing payload")
		}
		mediaType := video[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = videoExtensionForMediaType(mediaType)
		video = video[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(video)
	if err != nil {
		return nil, "", err
	}
	if !isVideoBytes(data) {
		return nil, "", errors.New("payload is not a supported video file")
	}
	return data, extension, nil
}

// contentArtifactPath resolves a HistoryContent entry's artifact path to an
// absolute filesystem path. Entries written by their own conversation carry a
// path relative to that conversation's directory; entries persisted as
// references to OTHER conversations' library assets carry an absolute path
// (see resolveReferencedAssets) and skip the conversation lookup entirely.
func contentArtifactPath(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	resolved := filepath.FromSlash(strings.TrimSpace(content.Path))
	if filepath.IsAbs(resolved) {
		return resolved, nil
	}
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(conversationPath), resolved), nil
}

// readArtifactAsDataURL resolves a persisted image artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedImage consumers (fal/Ollama)
// expect. Used by the harness to re-hydrate a prior turn's attached image when
// the current turn has no attachment, so image-dependent tools (upscale_image,
// image-to-image, image-to-video) work across turns without forcing the user
// to re-attach on every message. Path resolution mirrors hydrateHistoryContent;
// bytes are read directly rather than going through getConversation's hydration.
func readArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isImageBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported image", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// readAudioArtifactAsDataURL resolves a persisted audio artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedAudios consumers (fal's
// transcribe_audio) expect. Sibling of readArtifactAsDataURL for the audio
// path: used by the harness to re-hydrate a prior turn's attached audio when
// the current turn has no attachment, so transcribe_audio works across turns
// without forcing the user to re-attach on every message.
func readAudioArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isAudioBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported audio file", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "audio/") {
		// DetectContentType can return "application/octet-stream" for some
		// audio containers; fall back to mpeg so the data URL is well-formed.
		mediaType = "audio/mpeg"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// readVideoArtifactAsDataURL resolves a persisted video artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedVideos consumers (Veo extend,
// lip sync) expect. Sibling of readAudioArtifactAsDataURL: used by the harness
// to re-hydrate a prior turn's attached video when the current turn has no
// attachment, so video-dependent tools work across turns without forcing the
// user to re-attach on every message.
func readVideoArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isVideoBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported video file", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "video/") {
		// DetectContentType can return "application/octet-stream" for some
		// video containers; fall back to mp4 so the data URL is well-formed.
		mediaType = "video/mp4"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// tempAudioFileAsDataURL reads a freshly-generated audio temp file (the
// TempPath/MimeType on a ToolAudioFile produced by generate_speech/generate_sound) and
// re-encodes it as a base64 data URL — the shape AttachedAudios consumers
// (transcribe_audio, lip_sync) expect. Sibling of readAudioArtifactAsDataURL
// for the in-turn path: the file is a temp file written by writeTempAudio, not
// a persisted conversation artifact, so there is no conversation-path
// resolution. A missing/unreadable or non-audio file returns ("", nil): an
// error here must not fail the tool batch — the caller simply leaves
// AttachedAudios empty and the downstream tool surfaces its own attachment
// error, matching today's behavior.
func tempAudioFileAsDataURL(tempPath, mimeType string) (string, error) {
	path := strings.TrimSpace(tempPath)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // unreadable temp file — not fatal, see comment above
	}
	if !isAudioBytes(data) {
		return "", nil
	}
	mediaType := strings.TrimSpace(mimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "audio/") {
		mediaType = "audio/mpeg"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// tempVideoFileAsDataURL is the video sibling of tempAudioFileAsDataURL: reads
// a freshly-generated video temp file and re-encodes it as a data URL for
// AttachedVideos consumers (Veo extend, video-to-video lip sync). Same
// swallow-error contract: a bad temp file yields ("", nil), not a failure.
func tempVideoFileAsDataURL(tempPath, mimeType string) (string, error) {
	path := strings.TrimSpace(tempPath)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	if !isVideoBytes(data) {
		return "", nil
	}
	mediaType := strings.TrimSpace(mimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "video/") {
		mediaType = "video/mp4"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func normalizeImagePayloads(images []string) []string {
	normalized := make([]string, 0, len(images))
	for _, image := range images {
		if payload := normalizeImagePayload(image); payload != "" {
			normalized = append(normalized, payload)
		}
	}
	return normalized
}

func normalizeImagePayload(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "data:image/") {
		if _, _, err := decodeImagePayload(value); err == nil {
			return value
		}
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	if strings.HasPrefix(value, "![") {
		start := strings.Index(value, "](")
		end := strings.LastIndex(value, ")")
		if start >= 0 && end > start+2 {
			return normalizeImagePayload(value[start+2 : end])
		}
	}
	if strings.Contains(value, "data:image/") {
		start := strings.Index(value, "data:image/")
		end := start
		for end < len(value) && !strings.ContainsRune(" \n\r\t\"')]", rune(value[end])) {
			end++
		}
		return normalizeImagePayload(value[start:end])
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err == nil && isImageBytes(data) {
		return "data:" + mediaTypeForExtension(imageExtensionForBytes(data)) + ";base64," + value
	}
	return ""
}

// imageExtensionForBytes sniffs image bytes and returns their file extension —
// the single magic-number table behind isImageBytes, bare-base64 payload
// persistence, and attachment normalization. Beyond the web formats it covers
// every container macOS sips reads (local_images.go): the HEIF family, AVIF,
// JPEG 2000, TIFF, and BMP. An ISO BMFF container (bytes 4-8 "ftyp") counts
// only when its major or a compatible brand is an image brand, so an attached
// mp4/mov is never mistaken for a HEIF photo.
func imageExtensionForBytes(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return ".png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return ".jpg"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return ".webp"
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return ".gif"
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		// The brand table: HEIC/HEIF image and sequence brands, AVIF, and
		// JPEG 2000. The major brand sits at 8:12; compatible brands follow
		// the minor-version field at 16, four bytes apart.
		heifBrands := map[string]string{
			"heic": ".heic", "heix": ".heic", "heim": ".heic", "heis": ".heic",
			"hevc": ".heic", "hevx": ".heic", "hevm": ".heic", "hevs": ".heic",
			"mif1": ".heic", "mif2": ".heic", "msf1": ".heic",
			"avif": ".avif", "avis": ".avif",
			"jp2 ": ".jp2",
		}
		if extension, ok := heifBrands[string(data[8:12])]; ok {
			return extension
		}
		for offset := 16; offset+4 <= len(data); offset += 4 {
			if extension, ok := heifBrands[string(data[offset:offset+4])]; ok {
				return extension
			}
		}
	}
	if len(data) >= 4 && ((data[0] == 'I' && data[1] == 'I' && data[2] == 0x2a && data[3] == 0x00) ||
		(data[0] == 'M' && data[1] == 'M' && data[2] == 0x00 && data[3] == 0x2a)) {
		return ".tiff"
	}
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return ".bmp"
	}
	return ""
}

func isImageBytes(data []byte) bool {
	return imageExtensionForBytes(data) != ""
}

func extensionForMediaType(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic", "image/heif":
		return ".heic"
	case "image/avif":
		return ".avif"
	case "image/jp2":
		return ".jp2"
	case "image/tiff", "image/tif":
		return ".tiff"
	case "image/bmp":
		return ".bmp"
	default:
		return ".png"
	}
}

func mediaTypeForExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic", ".heif":
		return "image/heic"
	case ".avif":
		return "image/avif"
	case ".jp2":
		return "image/jp2"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".bmp":
		return "image/bmp"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		// Matroska: the staged-upload container allowlist keeps the .mkv
		// extension (media_upload.go), so the adopt path reads the same table —
		// an unknown mapping here would rename the artifact to .mp4 and stamp
		// the staged upload "image/png" (the table's default).
		return "video/x-matroska"
	case ".3gp":
		return "video/3gpp"
	case ".avi":
		return "video/x-msvideo"
	case ".ts":
		return "video/mp2t"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	case ".flv":
		return "video/x-flv"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".aac":
		return "audio/mp4"
	case ".md":
		return "text/markdown"
	case ".fountain":
		return "text/x-fountain"
	case ".vtt":
		return "text/vtt"
	case ".srt":
		return "application/x-subrip"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	default:
		return "image/png"
	}
}

// audioExtensionForMediaType maps an audio MIME type to a file extension,
// defaulting to .mp3 (the format most fal audio endpoints return).
func audioExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/mp4", "audio/aac", "audio/x-m4a":
		return ".m4a"
	default:
		return ".mp3"
	}
}

// isAudioBytes reports whether data looks like a container an <audio> tag can
// play: MP3 (ID3 tag or MPEG frame sync), WAV/RIFF, OGG, FLAC, or MP4/M4A
// (ISO base media "ftyp"). Returns false otherwise so a non-audio download is
// rejected rather than written as a broken artifact.
func isAudioBytes(data []byte) bool {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) {
		return true
	}
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0 {
		return true // MPEG audio frame sync (MP3/AAC-ADTS)
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC")) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true // MP4/M4A audio container
	}
	return false
}

// videoExtensionForMediaType maps a video MIME type to a file extension. Unlike
// extensionForMediaType (image-oriented, defaults to .png), an unrecognized
// video type falls back to .mp4 — the format every fal video endpoint returns.
func videoExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska", "video/mkv":
		// "video/mkv" is the MIME tag third-party Matroska UTIs declare on
		// macOS; x-matroska is the standard spelling.
		return ".mkv"
	case "video/x-msvideo":
		return ".avi"
	case "video/mp2t":
		return ".ts"
	case "video/mpeg":
		return ".mpg"
	case "video/x-flv":
		return ".flv"
	default:
		return ".mp4"
	}
}

// isVideoBytes reports whether data looks like one of the video containers the
// attachment pipeline admits (the staged-upload allowlist in media_upload.go
// and the frontend's videoExtensions mirror each other): MP4/MOV and 3GP (ISO
// base media, "ftyp" box at offset 4), WebM/Matroska (EBML magic), FLV ("FLV"
// magic), AVI (RIFF with "AVI " form type), MPEG program stream (.mpg/.mpeg,
// pack header), and MPEG transport stream (.ts, 0x47 packet sync — the M2TS
// variant shifts it four bytes in). Returns false otherwise so a stray
// non-video download is rejected rather than written as a broken artifact.
func isVideoBytes(data []byte) bool {
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return true
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("FLV")) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xBA}) {
		return true // MPEG program stream pack header
	}
	// Transport stream: 188-byte packets carry 0x47 at every header, so two
	// consecutive syncs identify it where a lone 0x47 could be anything.
	if len(data) >= 189 && data[0] == 0x47 && data[188] == 0x47 {
		return true
	}
	if len(data) >= 197 && data[4] == 0x47 && data[196] == 0x47 {
		return true // M2TS: 4-byte TP_extra_header before each packet
	}
	return false
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, invalid := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"} {
		value = strings.ReplaceAll(value, invalid, "-")
	}
	value = strings.Trim(value, ". ")
	return compactString(value, 80)
}
