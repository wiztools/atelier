package main

// Poster-frame extraction for persisted video artifacts. A <video> element in
// WKWebView paints black until playback starts (and until it is mounted at
// all), so every video artifact gets a still frame beside it at persist time:
// vid_<hex>.mp4 → vid_<hex>_poster.jpg, same directory. The name is a
// convention, not schema — the frontend derives the poster URL from the video
// URL (posterURLForVideoSrc in App.tsx) and a missing poster degrades to the
// plain video/placeholder, so old history without posters renders as before.
//
// Extraction is best-effort and never fails the turn save: ffmpeg when a local
// CLI resolves (the screenshot_video idiom — input seek, one decoded frame),
// else macOS's bundled qlmanage via the QuickLook thumbnailer, else no poster.
// The clip's own bytes decide the frame: 10% of its duration dodges both the
// black first frame AI clips often start on and the end-of-clip seek that
// yields nothing, and its tkhd display size resamples anamorphic sources to
// what a player shows (a JPEG carries no aspect metadata to correct a squeeze
// at view time). Both MP4 sniffers are the pricing/billing ones; a webm or
// unparseable container falls back to a fixed 0.5s seek and container-blind
// scaling, and a clip shorter than that seek retries once at 0.

import (
	"context"
	"fmt"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// videoPosterMaxWidth caps the poster's width. Posters ride the same asset
// handler as the clips and an assets panel can show twenty at once, so the
// decoded-image memory of a full 1080p frame × 20 is the cost that matters;
// 512px keeps a visible frame at panel-card sizes for a fraction of it.
const videoPosterMaxWidth = 512

// videoPosterTimeout bounds one poster extraction. A single-frame seek is
// fast even on long clips; this reaps a wedged process, not latency. It is
// far below the local-tool timeout because the poster sits in the turn-save
// path — a slow extractor must not hold up the save, it just loses the race
// and the card shows the plain video.
const videoPosterTimeout = 30 * time.Second

// defaultVideoPosterSeekSeconds is the seek used when the clip's duration
// can't be sniffed (webm, or a container the MP4 parser rejects). Short
// enough that virtually every clip has a frame there.
const defaultVideoPosterSeekSeconds = 0.5

// videoPosterPath returns the canonical poster path beside a video artifact:
// the video filename with its extension replaced by "_poster.jpg". IDs are
// random hex, so the derived name cannot collide with another artifact.
func videoPosterPath(videoPath string) string {
	return strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + "_poster.jpg"
}

// generateVideoPoster writes videoPath's poster (videoPosterPath) if any
// extractor resolves. Best-effort by design: it returns false on any failure
// and callers ignore that — a missing poster is a degraded thumbnail, never a
// failed save. Idempotent: an existing poster short-circuits, so a future
// re-run over the same artifact does not re-extract.
func generateVideoPoster(config AppConfig, videoPath string) bool {
	posterPath := videoPosterPath(videoPath)
	if info, err := os.Stat(posterPath); err == nil && info.Size() > 0 {
		return true
	}
	seekAt := defaultVideoPosterSeekSeconds
	displayWidth, displayHeight := 0, 0
	if data, err := os.ReadFile(videoPath); err == nil {
		if duration, ok := mp4DurationSeconds(data); ok && duration > 0 {
			seekAt = clampVideoPosterSeekSeconds(0.1 * duration)
		}
		if width, height, ok := mp4VideoDimensions(data); ok {
			displayWidth, displayHeight = int(width), int(height)
		}
	}
	if resolved, ok := resolveLocalFFmpegBinary(config); ok {
		if generateVideoPosterWithFFmpeg(resolved, videoPath, posterPath, seekAt, displayWidth, displayHeight) {
			return true
		}
	}
	return generateVideoPosterWithQuickLook(videoPath, posterPath)
}

// generateVideoPosterWithFFmpeg seeks to seekAt and decodes one frame through
// the resolved ffmpeg CLI, scaling to ≤ videoPosterMaxWidth on the way out.
// A seek past the clip's last frame produces an empty/missing output rather
// than an error, so a first empty attempt is retried once at 0.
func generateVideoPosterWithFFmpeg(resolved resolvedLocalBinary, videoPath, posterPath string, seekAt float64, displayWidth, displayHeight int) bool {
	staged, err := stageVideoPosterFile(posterPath)
	if err != nil {
		return false
	}
	defer os.Remove(staged)
	for _, attempt := range videoPosterSeekAttempts(seekAt) {
		args := ffmpegPosterArgs(videoPath, attempt, staged, displayWidth, displayHeight)
		if _, err := runLocalMediaCLI(context.Background(), resolved, "ffmpeg", videoPosterTimeout, args); err != nil {
			continue
		}
		if info, err := os.Stat(staged); err == nil && info.Size() > 0 {
			return moveFile(staged, posterPath) == nil
		}
	}
	return false
}

// generateVideoPosterWithQuickLook is the no-ffmpeg fallback: macOS's bundled
// qlmanage renders a QuickLook thumbnail PNG (≤ videoPosterMaxWidth wide),
// which is re-encoded to the canonical JPEG so the poster's extension is
// predictable from the video URL alone. Off darwin, or without a qlmanage on
// PATH (a non-stock macOS), there is no fallback — the poster is skipped.
func generateVideoPosterWithQuickLook(videoPath, posterPath string) bool {
	if runtimeGOOS != "darwin" {
		return false
	}
	binary, err := localBinaryLookPath("qlmanage")
	if err != nil {
		return false
	}
	tmpDir, err := os.MkdirTemp("", "atelier-poster-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmpDir)
	args := []string{"-t", "-s", strconv.Itoa(videoPosterMaxWidth), "-o", tmpDir, videoPath}
	// Only resolvedLocalBinary.path is read by the shared runner; the spec
	// fields would carry detection detail qlmanage doesn't have.
	resolved := resolvedLocalBinary{path: binary}
	if _, err := runLocalMediaCLI(context.Background(), resolved, "qlmanage", videoPosterTimeout, args); err != nil {
		return false
	}
	// qlmanage names its output after the input file plus .png in -o's dir.
	pngPath := filepath.Join(tmpDir, filepath.Base(videoPath)+".png")
	if info, err := os.Stat(pngPath); err != nil || info.Size() == 0 {
		matches, _ := filepath.Glob(filepath.Join(tmpDir, "*.png"))
		if len(matches) == 0 {
			return false
		}
		pngPath = matches[0]
	}
	return pngFileToJPEGFile(pngPath, posterPath) == nil
}

// pngFileToJPEGFile re-encodes a PNG as JPEG at poster quality — the bridge
// from qlmanage's PNG output to the canonical .jpg poster name. Stdlib only:
// decoding a PNG and encoding a JPEG needs no native tool.
func pngFileToJPEGFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	img, err := png.Decode(src)
	if err != nil {
		return fmt.Errorf("decode poster PNG: %w", err)
	}
	staged, err := stageVideoPosterFile(dstPath)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	out, err := os.Create(staged)
	if err != nil {
		return err
	}
	encodeErr := jpeg.Encode(out, img, &jpeg.Options{Quality: 82})
	closeErr := out.Close()
	if encodeErr != nil {
		return fmt.Errorf("encode poster JPEG: %w", encodeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := moveFile(staged, dstPath); err != nil {
		return err
	}
	return nil
}

// stageVideoPosterFile reserves a temp filename in the poster's directory
// (same volume, so the final move is a rename) and returns the path with no
// file left behind — the extractors create their own output files.
func stageVideoPosterFile(posterPath string) (string, error) {
	staged, err := os.CreateTemp(filepath.Dir(posterPath), ".poster-*.jpg")
	if err != nil {
		return "", err
	}
	name := staged.Name()
	staged.Close()
	os.Remove(name)
	return name, nil
}

// videoPosterSeekAttempts returns the seek timestamps to try, in order: the
// computed one, then 0 for a clip shorter than it.
func videoPosterSeekAttempts(seekAt float64) []string {
	if seekAt <= 0 {
		return []string{"0"}
	}
	return []string{strconv.FormatFloat(seekAt, 'f', 3, 64), "0"}
}

// clampVideoPosterSeekSeconds keeps the 10%-of-duration seek inside a sane
// band: never before the first frame has settled, never deep into a long
// join where the frame stops representing the clip.
func clampVideoPosterSeekSeconds(at float64) float64 {
	const minSeek = 0.05
	const maxSeek = 30.0
	if at < minSeek {
		return minSeek
	}
	if at > maxSeek {
		return maxSeek
	}
	return at
}

// ffmpegPosterArgs grabs one frame at a timestamp as a width-capped JPEG.
// -ss before -i is a fast input seek. With the clip's display size known
// (anamorphic included) the frame is resampled to ≤ videoPosterMaxWidth at
// the DISPLAY aspect with SAR reset — a JPEG can't carry aspect metadata, so
// the resample is what unsqueezes it. Without dimensions the scale
// expression caps width and preserves the storage aspect.
func ffmpegPosterArgs(input string, seekAt string, output string, displayWidth, displayHeight int) []string {
	args := []string{"-ss", seekAt, "-i", input}
	if displayWidth > 0 && displayHeight > 0 {
		width, height := videoPosterTargetSize(displayWidth, displayHeight)
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d,setsar=1", width, height))
	} else {
		args = append(args, "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", videoPosterMaxWidth))
	}
	return append(args, "-frames:v", "1", "-q:v", "3", output)
}

// videoPosterTargetSize scales a display size down to videoPosterMaxWidth,
// preserving aspect; sizes at or below the cap pass through untouched.
func videoPosterTargetSize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	targetWidth := width
	if targetWidth > videoPosterMaxWidth {
		targetWidth = videoPosterMaxWidth
	}
	targetHeight := int(math.Round(float64(height) * float64(targetWidth) / float64(width)))
	if targetHeight < 1 {
		targetHeight = 1
	}
	return targetWidth, targetHeight
}
