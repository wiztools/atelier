package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type VideoEditSubmitRequest struct {
	ParentConversationID  string              `json:"parentConversationId"`
	SourceArtifactID      string              `json:"sourceArtifactId"`
	SessionConversationID string              `json:"sessionConversationId,omitempty"`
	InputArtifactID       string              `json:"inputArtifactId,omitempty"`
	SourceDigest          string              `json:"sourceDigest,omitempty"`
	Reframe               *VideoReframeParams `json:"reframe,omitempty"`
}

type videoPreviewJob struct {
	cancel context.CancelFunc
}

func (a *App) ResolveVideoEditSource(conversationID, artifactID string) (EditSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	artifactID = strings.TrimSpace(artifactID)
	if conversationID == "" || artifactID == "" {
		return EditSourceInfo{}, errors.New("a conversation id and artifact id are required to open the video editor")
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return EditSourceInfo{}, err
	}
	content, originTurnID, ok := findVideoContent(detail, artifactID)
	if !ok {
		return EditSourceInfo{}, fmt.Errorf("video %q not found in conversation %s", artifactID, conversationID)
	}
	key := videoEditPreviewKey(conversationID, artifactID)
	ctx, cancel := context.WithCancel(context.Background())
	job := &videoPreviewJob{cancel: cancel}
	a.videoPreviewMu.Lock()
	if prev := a.videoPreviewJobs[key]; prev != nil {
		prev.cancel()
	}
	a.videoPreviewJobs[key] = job
	a.videoPreviewMu.Unlock()
	defer func() {
		a.videoPreviewMu.Lock()
		if a.videoPreviewJobs[key] == job {
			delete(a.videoPreviewJobs, key)
		}
		a.videoPreviewMu.Unlock()
		cancel()
	}()
	cleanupEditSourcePreviews()
	info, err := videoEditSourceInfoForPreview(ctx, config, conversationID, detail.Conversation.Title, originTurnID, content)
	if err != nil {
		return EditSourceInfo{}, err
	}
	a.videoPreviewMu.Lock()
	current := a.videoPreviewJobs[key] == job && ctx.Err() == nil
	if current {
		if a.videoSourcePreviews == nil {
			a.videoSourcePreviews = make(map[string]EditSourceInfo)
		}
		a.videoSourcePreviews[key] = info
	}
	a.videoPreviewMu.Unlock()
	if !current {
		removeVideoPreviewFiles(info)
		return EditSourceInfo{}, context.Canceled
	}
	return info, nil
}

func (a *App) CancelVideoEditSource(conversationID, artifactID string) error {
	key := videoEditPreviewKey(conversationID, artifactID)
	a.videoPreviewMu.Lock()
	job := a.videoPreviewJobs[key]
	if job != nil {
		delete(a.videoPreviewJobs, key)
	}
	a.videoPreviewMu.Unlock()
	if job != nil {
		job.cancel()
	}
	return nil
}

func videoEditPreviewKey(conversationID, artifactID string) string {
	return strings.TrimSpace(conversationID) + "\x00" + strings.TrimSpace(artifactID)
}

func (a *App) ResolveVideoEditInput(sessionID, artifactID string) (EditSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditSourceInfo{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	artifactID = strings.TrimSpace(artifactID)
	if sessionID == "" || artifactID == "" {
		return EditSourceInfo{}, errors.New("a session id and artifact id are required")
	}
	detail, err := getConversation(config.Storage, sessionID)
	if err != nil {
		return EditSourceInfo{}, err
	}
	if detail.Conversation.Kind != editConversationKind || editSessionMediaKind(detail.Conversation.EditSession) != "video" {
		return EditSourceInfo{}, fmt.Errorf("conversation %s is not a video edit session", sessionID)
	}
	content, turnID, ok := findVideoContent(detail, artifactID)
	if !ok {
		return EditSourceInfo{}, fmt.Errorf("video %q not found in session %s", artifactID, sessionID)
	}
	info, err := videoEditSourceInfoFor(config.Storage, sessionID, detail.Conversation.Title, turnID, content, nil)
	if err == nil {
		path, pathErr := contentArtifactPath(config.Storage, sessionID, content)
		if pathErr == nil {
			info.Thumbnails = videoEditThumbnailURLs(context.Background(), config, path, info.DurationSeconds, 12)
		}
	}
	return info, err
}

func videoEditSourceInfoForPreview(ctx context.Context, config AppConfig, conversationID, title, originTurnID string, content HistoryContent) (EditSourceInfo, error) {
	absPath, err := contentArtifactPath(config.Storage, conversationID, content)
	if err != nil {
		return EditSourceInfo{}, err
	}
	digest, err := fileDigest(absPath)
	if err != nil {
		return EditSourceInfo{}, err
	}
	preview, probe, notices, err := normalizeVideoEditSource(ctx, config, absPath)
	if err != nil {
		return EditSourceInfo{}, err
	}
	info, err := videoEditSourceInfoFor(config.Storage, conversationID, title, originTurnID, HistoryContent{
		Type: "video", ArtifactID: content.ArtifactID, Path: preview, MimeType: "video/mp4",
	}, &probe)
	if err != nil {
		_ = os.Remove(preview)
		return EditSourceInfo{}, err
	}
	info.SourceDigest = digest
	info.Notices = append(info.Notices, notices...)
	info.URL = artifactPrefix + preview
	info.Thumbnails = videoEditThumbnailURLs(ctx, config, preview, probe.Duration, 12)
	if ctx.Err() != nil {
		removeVideoPreviewFiles(info)
		return EditSourceInfo{}, ctx.Err()
	}
	return info, nil
}

func removeVideoPreviewFiles(info EditSourceInfo) {
	for _, url := range append([]string{info.URL}, info.Thumbnails...) {
		path := strings.TrimPrefix(url, artifactPrefix)
		if filepath.Dir(path) == editSourcePreviewDir() && strings.HasPrefix(filepath.Base(path), "editpreview") {
			_ = os.Remove(path)
		}
	}
}

func videoEditSourceInfoFor(storage ConfigStorage, conversationID, title, originTurnID string, content HistoryContent, knownProbe *ToolProbeResult) (EditSourceInfo, error) {
	absPath := ""
	var err error
	if filepath.IsAbs(strings.TrimSpace(content.Path)) {
		absPath = filepath.Clean(content.Path)
	} else {
		absPath, err = contentArtifactPath(storage, conversationID, content)
		if err != nil {
			return EditSourceInfo{}, err
		}
	}
	probe := ToolProbeResult{}
	if knownProbe != nil {
		probe = *knownProbe
	} else {
		config, cfgErr := loadReadyConfig()
		if cfgErr == nil {
			probe, _ = probeStagedMedia(context.Background(), config, absPath, "video")
		}
	}
	if probe.Width <= 0 || probe.Height <= 0 || probe.Duration <= 0 {
		if data, readErr := os.ReadFile(absPath); readErr == nil {
			if w, h, ok := mp4VideoDimensions(data); ok {
				probe.Width, probe.Height = int(w+0.5), int(h+0.5)
			}
			if duration, ok := mp4DurationSeconds(data); ok {
				probe.Duration = duration
			}
		}
	}
	if probe.Width <= 0 || probe.Height <= 0 || probe.Duration <= 0 {
		return EditSourceInfo{}, errors.New("could not read the video dimensions and duration")
	}
	return EditSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		OriginTurnID:      originTurnID,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          "video/mp4",
		Width:             probe.Width,
		Height:            probe.Height,
		MediaKind:         "video",
		DurationSeconds:   probe.Duration,
	}, nil
}

func normalizeVideoEditSource(ctx context.Context, config AppConfig, input string) (string, ToolProbeResult, []string, error) {
	if _, ok := resolveLocalFFmpegBinary(config); !ok {
		return "", ToolProbeResult{}, nil, errors.New("no local ffmpeg CLI found — install ffmpeg to use the Video Editor")
	}
	if _, ok := resolveLocalFFprobeBinary(config); !ok {
		return "", ToolProbeResult{}, nil, errors.New("no local ffprobe CLI found — it ships with ffmpeg and is required by the Video Editor")
	}
	dir := editSourcePreviewDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", ToolProbeResult{}, nil, err
	}
	output := filepath.Join(dir, randomID("editpreview")+".mp4")
	inputProbe, err := probeStagedMedia(ctx, config, input, "video")
	if err != nil {
		return "", ToolProbeResult{}, nil, err
	}
	if !videoReframeDimension(inputProbe.Width) || !videoReframeDimension(inputProbe.Height) || !videoReframeFinite(inputProbe.Duration) || inputProbe.Duration <= 0 {
		return "", ToolProbeResult{}, nil, errors.New("the video has unsupported dimensions or timing")
	}
	data, probeErr := runLocalFFprobe(ctx, config, []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=start_time,pix_fmt", "-of", "json", input})
	var timing struct {
		Streams []struct {
			Start       string `json:"start_time"`
			PixelFormat string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if probeErr != nil || json.Unmarshal(data, &timing) != nil || len(timing.Streams) == 0 {
		return "", ToolProbeResult{}, nil, errors.New("could not determine the source video's presentation origin")
	}
	start, parseErr := strconv.ParseFloat(timing.Streams[0].Start, 64)
	if parseErr != nil || !videoReframeFinite(start) {
		return "", ToolProbeResult{}, nil, errors.New("the source video has unsupported presentation timestamps")
	}
	// An ordinary browser-playable clip needs a private copy, not another
	// lossy encode. The timestamp check excludes files whose timeline needs
	// rebasing before preview and rendering can share the same zero origin.
	if inputProbe.VideoCodec == "h264" && (inputProbe.AudioCodec == "" || inputProbe.AudioCodec == "aac") && inputProbe.Rotation == 0 &&
		(inputProbe.SampleAspectRatio == "" || inputProbe.SampleAspectRatio == "1:1") && inputProbe.Width%2 == 0 && inputProbe.Height%2 == 0 && strings.Contains(inputProbe.Format, "mp4") {
		if start == 0 && (timing.Streams[0].PixelFormat == "yuv420p" || timing.Streams[0].PixelFormat == "") {
			if err := copyVideoPreview(ctx, input, output); err != nil {
				return "", ToolProbeResult{}, nil, err
			}
			return output, inputProbe, nil, nil
		}
	}
	origin := videoReframeNumber(start)
	filter := "setpts=PTS-(" + origin + ")/TB,scale=trunc(iw*sar/2)*2:trunc(ih/2)*2,setsar=1"
	args := []string{"-nostdin", "-y", "-copyts", "-i", input, "-map", "0:v:0", "-vf", filter, "-fps_mode", "passthrough", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	if inputProbe.AudioCodec != "" {
		args = append(args, "-map", "0:a:0", "-af", "asetpts=PTS-("+origin+")/TB", "-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, "-avoid_negative_ts", "disabled", "-movflags", "+faststart", output)
	if err := runLocalFFmpeg(ctx, config, args); err != nil {
		_ = os.Remove(output)
		return "", ToolProbeResult{}, nil, err
	}
	probe, err := probeStagedMedia(ctx, config, output, "video")
	if err != nil {
		_ = os.Remove(output)
		return "", ToolProbeResult{}, nil, err
	}
	var notices []string
	notices = append(notices, "The editor prepared an upright square-pixel preview copy so framing and export use the same coordinates.")
	return output, probe, notices, nil
}

// videoEditThumbnailBudget bounds one thumbnail-strip generation pass. With
// the deterministic per-artifact naming below, an expired budget leaves the
// missing tiles to a later resolve instead of losing them.
const videoEditThumbnailBudget = 30 * time.Second

// videoEditThumbnailURLs returns filmstrip thumbnail URLs for a video file.
// Poster-style (the video_poster.go convention), the tiles are
// <stem>_thumb_NN.jpg siblings of the source itself: thumbnails for a session
// artifact live as long as the artifact, so reopening an editor or
// re-resolving a source (restore, use-as-source, edit-framing) is a stat
// check instead of twelve ffmpeg spawns, and an interrupted strip resumes
// where it stopped. ReleaseVideoEditSourcePreview only deletes files in the
// editpreview scratch dir, so artifact-side tiles survive an editor close by
// design; scratch-dir tiles still ride the info's release.
func videoEditThumbnailURLs(ctx context.Context, config AppConfig, input string, duration float64, maxCount int) []string {
	if duration <= 0 || maxCount <= 0 {
		return nil
	}
	if maxCount > 30 {
		maxCount = 30
	}
	count := maxCount
	if duration < float64(count) {
		count = max(1, int(duration))
	}
	stem := strings.TrimSuffix(input, filepath.Ext(input))
	paths := make([]string, count)
	for i := range paths {
		paths[i] = fmt.Sprintf("%s_thumb_%02d.jpg", stem, i+1)
	}
	pruneVideoEditThumbnails(stem, paths)
	ctx, cancel := context.WithTimeout(ctx, videoEditThumbnailBudget)
	defer cancel()
	for i, path := range paths {
		if ctx.Err() != nil {
			break
		}
		if videoEditThumbnailPresent(path) {
			continue
		}
		at := duration * (float64(i) + 0.5) / float64(count)
		err := runLocalFFmpeg(ctx, config, []string{"-nostdin", "-y", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", input, "-frames:v", "1", "-vf", "scale='min(240,iw)':-2", "-q:v", "4", path})
		if err != nil {
			_ = os.Remove(path)
		}
	}
	urls := make([]string, 0, count)
	for _, path := range paths {
		if videoEditThumbnailPresent(path) {
			urls = append(urls, artifactPrefix+path)
		}
	}
	return urls
}

// videoEditThumbnailPresent treats a zero-byte file as absent: an ffmpeg run
// killed mid-encode leaves exactly that, and the tile must regenerate.
func videoEditThumbnailPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// pruneVideoEditThumbnails deletes strip tiles left by an earlier generation
// whose tile count differed (a re-probed duration), so the deterministic
// naming never strands orphans beside the clip.
func pruneVideoEditThumbnails(stem string, expected []string) {
	dir := filepath.Dir(stem)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Base(stem) + "_thumb_"
	wanted := make(map[string]bool, len(expected))
	for _, path := range expected {
		wanted[path] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jpg") {
			continue
		}
		path := filepath.Join(dir, name)
		if !wanted[path] {
			_ = os.Remove(path)
		}
	}
}

func (a *App) SubmitVideoEdit(req VideoEditSubmitRequest) (EditOperationState, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditOperationState{}, err
	}
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if req.Reframe == nil {
		return EditOperationState{}, errors.New("reframe parameters are required")
	}
	reframe, err := validateVideoReframe(*req.Reframe)
	if err != nil {
		return EditOperationState{}, err
	}
	parentID := strings.TrimSpace(req.ParentConversationID)
	sessionID := strings.TrimSpace(req.SessionConversationID)
	store := newHistoryStore(config.Storage)
	nowText := time.Now().Format(time.RFC3339)
	createdSession := false
	sourceURL := ""

	if sessionID == "" {
		if strings.TrimSpace(req.InputArtifactID) != "" {
			return EditOperationState{}, errors.New("an input artifact can only be selected within an existing video edit session")
		}
		sourceArtifactID := strings.TrimSpace(req.SourceArtifactID)
		if parentID == "" || sourceArtifactID == "" {
			return EditOperationState{}, errors.New("the source conversation and video artifact must be named")
		}
		parentDetail, err := getConversation(config.Storage, parentID)
		if err != nil {
			return EditOperationState{}, fmt.Errorf("the conversation this video belongs to is unavailable: %w", err)
		}
		if parentDetail.Conversation.Kind != "chat" {
			return EditOperationState{}, errors.New("this video already belongs to an edit session — reopen that editor instead")
		}
		sourceContent, sourceTurnID, ok := findVideoContent(parentDetail, sourceArtifactID)
		if !ok {
			return EditOperationState{}, fmt.Errorf("video %q not found in conversation %s", sourceArtifactID, parentID)
		}
		sourcePath, err := contentArtifactPath(config.Storage, parentID, sourceContent)
		if err != nil {
			return EditOperationState{}, err
		}
		if digest := strings.TrimSpace(req.SourceDigest); digest != "" {
			actual, err := fileDigest(sourcePath)
			if err != nil {
				return EditOperationState{}, err
			}
			if digest != actual {
				return EditOperationState{}, errors.New("the source video changed after the editor opened — reopen it and try again")
			}
		}
		// Reuse the exact preview pixels while holding editSubmitMu. Releasing
		// a draft takes the same lock, so its file cannot disappear mid-copy.
		a.videoPreviewMu.Lock()
		prepared, cached := a.videoSourcePreviews[videoEditPreviewKey(parentID, sourceArtifactID)]
		a.videoPreviewMu.Unlock()
		normalized := ""
		probe := ToolProbeResult{}
		var notices []string
		if cached && req.SourceDigest != "" && prepared.SourceDigest == req.SourceDigest {
			normalized = strings.TrimPrefix(prepared.URL, artifactPrefix)
			probe, err = probeStagedMedia(context.Background(), config, normalized, "video")
			notices = prepared.Notices
		} else {
			normalized, probe, notices, err = normalizeVideoEditSource(context.Background(), config, sourcePath)
			if err == nil {
				defer os.Remove(normalized)
			}
		}
		if err != nil {
			return EditOperationState{}, err
		}
		if reframe.Source.Width != probe.Width || reframe.Source.Height != probe.Height {
			return EditOperationState{}, fmt.Errorf("the submitted framing targets %dx%d, but the normalized source is %dx%d — reopen the editor", reframe.Source.Width, reframe.Source.Height, probe.Width, probe.Height)
		}
		if absDurationDelta(reframe.Source.DurationSeconds, probe.Duration) > 0.25 {
			return EditOperationState{}, errors.New("the submitted framing duration no longer matches the normalized source — reopen the editor")
		}
		sessionID, sourceURL, err = createVideoEditSession(store, config, parentDetail, sourceContent, sourceTurnID, normalized, probe, notices, nowText)
		if err != nil {
			return EditOperationState{}, err
		}
		createdSession = true
	}

	loaded, err := loadForEditAppend(config.Storage, sessionID)
	if err != nil {
		return EditOperationState{}, err
	}
	meta := loaded.Conversation.EditSession
	if meta == nil || editSessionMediaKind(meta) != "video" {
		return EditOperationState{}, fmt.Errorf("edit session %s is not a video session", sessionID)
	}
	if parentID == "" {
		parentID = meta.ParentConversationID
	} else if parentID != meta.ParentConversationID {
		return EditOperationState{}, fmt.Errorf("session %s does not belong to conversation %s", sessionID, parentID)
	}
	if a.editOpRunning(sessionID) {
		return EditOperationState{}, errors.New("an edit operation is already running in this session — wait for it to finish or cancel it first")
	}
	sessionDetail, err := getConversation(config.Storage, sessionID)
	if err != nil {
		return EditOperationState{}, err
	}
	inputArtifactID := strings.TrimSpace(req.InputArtifactID)
	if inputArtifactID == "" {
		inputArtifactID = latestCanvasArtifactID(sessionDetail)
	}
	inputContent, _, ok := findVideoContent(sessionDetail, inputArtifactID)
	if !ok {
		return EditOperationState{}, fmt.Errorf("the video this edit builds on (%s) is not part of the session", inputArtifactID)
	}
	inputPath, err := contentArtifactPath(config.Storage, sessionID, inputContent)
	if err != nil {
		return EditOperationState{}, err
	}
	probe, err := probeStagedMedia(context.Background(), config, inputPath, "video")
	if err != nil {
		return EditOperationState{}, err
	}
	if reframe.Source.Width != probe.Width || reframe.Source.Height != probe.Height || absDurationDelta(reframe.Source.DurationSeconds, probe.Duration) > 0.25 {
		return EditOperationState{}, errors.New("the submitted framing no longer matches the selected video source — use the result as source again and retry")
	}
	op := EditOperation{
		ID:              randomID("editop"),
		Kind:            editOperationKindReframe,
		Status:          editOperationStatusQueued,
		CreatedAt:       nowText,
		InputArtifactID: inputArtifactID,
		Backend:         "ffmpeg",
		Reframe:         &reframe,
	}
	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: sessionID,
		CreatedAt:      nowText,
		Kind:           editConversationKind,
		Role:           "assistant",
		Request:        map[string]any{"operation": op},
	}
	opCtx, opCancel := a.registerImageEditOperation(sessionID, op.ID)
	launched := false
	defer func() {
		if !launched {
			a.editOpsMu.Lock()
			delete(a.editOps, op.ID)
			a.editOpsMu.Unlock()
			opCancel()
		}
	}()
	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return EditOperationState{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, turn); err != nil {
		return EditOperationState{}, err
	}
	launched = true
	go a.executeVideoEditOperation(opCtx, opCancel, config, sessionID, turn.ID, op, inputPath, probe.AudioCodec != "", probe.Duration)
	return EditOperationState{SessionConversationID: sessionID, CreatedSession: createdSession, Operation: op, SourceURL: sourceURL}, nil
}

func (a *App) CancelVideoEdit(sessionConversationID, operationID string) error {
	return a.CancelImageEdit(sessionConversationID, operationID)
}

func createVideoEditSession(store HistoryStore, config AppConfig, parentDetail ConversationDetail, sourceContent HistoryContent, sourceTurnID, sourcePath string, probe ToolProbeResult, notices []string, nowText string) (string, string, error) {
	parent := parentDetail.Conversation
	workspace, err := store.newWorkspace(time.Now())
	if err != nil {
		return "", "", err
	}
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(workspace.Dir)
		}
	}()
	if err := os.MkdirAll(workspace.ArtifactsDir, 0o755); err != nil {
		return "", "", err
	}
	artifactID := randomID("vid")
	filename := artifactID + ".mp4"
	dest := filepath.Join(workspace.ArtifactsDir, filename)
	if err := copyFile(sourcePath, dest); err != nil {
		return "", "", err
	}
	generateVideoPoster(config, dest)
	sourceEntry := HistoryContent{Type: "video", ArtifactID: artifactID, Path: filepath.ToSlash(filepath.Join("artifacts", filename)), MimeType: "video/mp4", Width: probe.Width, Height: probe.Height}
	conversation := HistoryConversation{
		SchemaVersion:  currentConversationSchemaVersion,
		ID:             workspace.ID,
		Kind:           editConversationKind,
		Title:          fmt.Sprintf("Edit · %s", parent.Title),
		CreatedAt:      nowText,
		UpdatedAt:      nowText,
		Provider:       parent.Provider,
		Defaults:       parent.Defaults,
		Stats:          HistoryConversationStats{TurnCount: 1, ArtifactCount: 1},
		Workspace:      parent.Workspace,
		ProjectID:      parent.ProjectID,
		ModelOverrides: parent.ModelOverrides,
		EditSession: &EditSessionMeta{
			ParentConversationID: parent.ID,
			SourceTurnID:         sourceTurnID,
			SourceArtifactID:     sourceContent.ArtifactID,
			SourceTitleSnapshot:  parent.Title,
			MediaKind:            "video",
		},
	}
	sourceTurn := HistoryTurn{SchemaVersion: 1, ID: "turn_000001", ConversationID: workspace.ID, CreatedAt: nowText, Kind: editConversationKind, Role: "user", Content: []HistoryContent{sourceEntry}}
	if len(notices) > 0 {
		sourceTurn.Request = map[string]any{"notices": notices}
	}
	if err := store.writeSnapshot(workspace, conversation, sourceTurn); err != nil {
		return "", "", err
	}
	created = true
	return workspace.ID, artifactPrefix + dest, nil
}

func (a *App) executeVideoEditOperation(ctx context.Context, cancel context.CancelFunc, config AppConfig, sessionID, turnID string, op EditOperation, inputPath string, hasAudio bool, duration float64) {
	defer func() {
		a.editOpsMu.Lock()
		delete(a.editOps, op.ID)
		a.editOpsMu.Unlock()
		cancel()
	}()
	store := newHistoryStore(config.Storage)
	persistAndEmit := func(next EditOperation) error {
		a.editSubmitMu.Lock()
		defer a.editSubmitMu.Unlock()
		loaded, err := loadForEditAppend(config.Storage, sessionID)
		if err != nil {
			next.Status = editOperationStatusFailed
			next.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, next)
			return err
		}
		cancelledResult := next.Status == editOperationStatusCompleted && ctx.Err() != nil
		if cancelledResult {
			next.Status = editOperationStatusCancelled
			next.Error = ctx.Err().Error()
			next.ResultArtifactID, next.ResultPath, next.ResultURL, next.ResultMimeType = "", "", "", ""
			next.ResultWidth, next.ResultHeight, next.ResultDurationSeconds = 0, 0, 0
		}
		before := loaded.Conversation
		loaded.Conversation.UpdatedAt = time.Now().Format(time.RFC3339)
		if next.Status == editOperationStatusCompleted {
			loaded.Conversation.Stats.ArtifactCount++
		}
		if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
			next.Status = editOperationStatusFailed
			next.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, next)
			return err
		}
		if err := store.writeTurn(loaded.TurnsDir, HistoryTurn{SchemaVersion: 1, ID: turnID, ConversationID: sessionID, CreatedAt: next.CreatedAt, Kind: editConversationKind, Role: "assistant", Request: map[string]any{"operation": next}, Content: editOperationContents(next)}); err != nil {
			_ = store.writeConversation(loaded.Path, before)
			next.Status = editOperationStatusFailed
			next.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, next)
			return err
		}
		if next.ResultPath != "" {
			next.ResultURL = artifactPrefix + filepath.Join(filepath.Dir(loaded.Path), filepath.FromSlash(next.ResultPath))
		}
		a.emitEditOperation(sessionID, next)
		if cancelledResult {
			return ctx.Err()
		}
		return nil
	}
	op.Status = editOperationStatusRunning
	op.Progress = 0
	if err := persistAndEmit(op); err != nil {
		return
	}
	staging, err := os.MkdirTemp("", "atelier-video-edit-*")
	if err != nil {
		op.Status = editOperationStatusFailed
		op.Error = err.Error()
		op.CompletedAt = time.Now().Format(time.RFC3339)
		_ = persistAndEmit(op)
		return
	}
	defer os.RemoveAll(staging)
	staged := filepath.Join(staging, "reframed.mp4")
	args, err := videoReframeFFmpegArgs(*op.Reframe, inputPath, staged, 0, hasAudio)
	if err == nil {
		err = runLocalFFmpegProgress(ctx, config, duration, args, func(progress float64) {
			op.Progress = progress
			a.emitEditOperation(sessionID, op)
		})
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		resultProbe, probeErr := probeStagedMedia(ctx, config, staged, "video")
		if probeErr != nil {
			err = fmt.Errorf("could not verify the rendered video: %w", probeErr)
		} else if resultProbe.Width != op.Reframe.Output.Width || resultProbe.Height != op.Reframe.Output.Height ||
			absDurationDelta(resultProbe.Duration, duration) > 0.1 || (hasAudio && resultProbe.AudioCodec == "") {
			err = errors.New("the rendered video did not preserve the requested dimensions, duration, or audio")
		}
	}
	op.CompletedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		op.Status = editOperationStatusFailed
		if ctx.Err() != nil {
			op.Status = editOperationStatusCancelled
		}
		op.Error = err.Error()
		_ = persistAndEmit(op)
		return
	}
	loaded, loadErr := loadForEditAppend(config.Storage, sessionID)
	if loadErr != nil {
		op.Status = editOperationStatusFailed
		op.Error = fmt.Sprintf("the edit ran but its result could not be saved: %v", loadErr)
		_ = persistAndEmit(op)
		return
	}
	tempResult := filepath.Join(staging, "result.mp4")
	if err := copyFile(staged, tempResult); err != nil {
		op.Status = editOperationStatusFailed
		op.Error = fmt.Sprintf("the edit ran but its result could not be staged: %v", err)
		_ = persistAndEmit(op)
		return
	}
	contents, _, writeErr := writeChatVideoArtifacts(config, loaded.ArtifactsDir, []ToolVideoFile{{TempPath: tempResult, MimeType: "video/mp4"}})
	if writeErr != nil || len(contents) == 0 {
		op.Status = editOperationStatusFailed
		if writeErr == nil {
			writeErr = errors.New("the edit produced no video")
		}
		op.Error = fmt.Sprintf("the edit ran but its result could not be saved: %v", writeErr)
		_ = persistAndEmit(op)
		return
	}
	result := contents[0]
	op.Status = editOperationStatusCompleted
	op.Progress = 1
	op.ResultArtifactID = result.ArtifactID
	op.ResultPath = result.Path
	op.ResultMimeType = result.MimeType
	op.ResultWidth = op.Reframe.Output.Width
	op.ResultHeight = op.Reframe.Output.Height
	op.ResultDurationSeconds = duration
	if err := persistAndEmit(op); err != nil {
		resultPath := filepath.Join(loaded.ArtifactsDir, filepath.Base(result.Path))
		_ = os.Remove(resultPath)
		_ = os.Remove(strings.TrimSuffix(resultPath, filepath.Ext(resultPath)) + "_poster.jpg")
	}
}

func runLocalFFmpegProgress(ctx context.Context, config AppConfig, duration float64, args []string, onProgress func(float64)) error {
	resolved, ok := resolveLocalFFmpegBinary(config)
	if !ok {
		return errors.New("no local ffmpeg CLI found — install ffmpeg to use the Video Editor")
	}
	deadline := max(localFFmpegTimeout, time.Duration(min(duration*2, 1800))*time.Second)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	progressArgs := append([]string{"-progress", "pipe:1", "-nostats"}, args...)
	cmd := exec.CommandContext(ctx, resolved.path, progressArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var stderrBuf strings.Builder
	stderrDone := make(chan struct{})
	doneErr := make(chan error, 1)
	go func() {
		_, _ = io.Copy(&boundedStringWriter{builder: &stderrBuf, limit: 2000}, stderr)
		close(stderrDone)
	}()
	go func() {
		scanner := bufio.NewScanner(stdout)
		last := time.Time{}
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "out_time_ms=") || duration <= 0 {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "out_time_ms="))
			micros, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			progress := max(0, min(micros/1_000_000/duration, 0.99))
			if time.Since(last) >= 250*time.Millisecond {
				last = time.Now()
				onProgress(progress)
			}
		}
		doneErr <- scanner.Err()
	}()
	scanErr := <-doneErr
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	<-stderrDone
	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg failed: %s", truncateLocalToolOutput([]byte(stderrBuf.String())))
	}
	if scanErr != nil {
		return scanErr
	}
	// Completion is published only after history/artifact persistence.
	onProgress(0.99)
	return nil
}

type boundedStringWriter struct {
	builder *strings.Builder
	limit   int
}

func (w *boundedStringWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.builder.Len() < w.limit {
		remaining := w.limit - w.builder.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.builder.Write(p)
	}
	return original, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyVideoPreview(ctx context.Context, source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		_ = out.Close()
		if !succeeded {
			_ = os.Remove(destination)
		}
	}()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	succeeded = true
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func cleanupVideoEditStaging() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "atelier-video-edit-") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
		}
	}
}

func copyEditResultToTemp(path, ext string) (string, error) {
	out, err := os.CreateTemp("", "atelier-edit-result-*"+ext)
	if err != nil {
		return "", err
	}
	name := out.Name()
	_ = out.Close()
	if err := copyFile(path, name); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func absDurationDelta(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
