package main

// The Audio Editor's session layer — the video_edit_session.go shape without
// geometry: WebKit plays the artifact's own bytes, so resolve returns identity
// plus probe facts and no preview copy is made (the digest check still pins
// the submit to the bytes the cuts were marked against). A submitted trim
// keeps its segments via a single-pass ffmpeg trim+concat into AAC/m4a,
// mirroring the video editor's local ffmpeg attribution.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AudioEditSubmitRequest struct {
	ParentConversationID  string           `json:"parentConversationId"`
	SourceArtifactID      string           `json:"sourceArtifactId"`
	SessionConversationID string           `json:"sessionConversationId,omitempty"`
	InputArtifactID       string           `json:"inputArtifactId,omitempty"`
	SourceDigest          string           `json:"sourceDigest,omitempty"`
	Trim                  *AudioTrimParams `json:"trim,omitempty"`
}

// requireAudioEditCLIs is the editor's tool gate — the same pair the Video
// Editor needs (ffprobe for probing, ffmpeg for rendering), with Audio Editor
// wording so an install note never names the wrong editor.
func requireAudioEditCLIs(config AppConfig) error {
	if _, ok := resolveLocalFFmpegBinary(config); !ok {
		return errors.New("no local ffmpeg CLI found — install ffmpeg to use the Audio Editor")
	}
	if _, ok := resolveLocalFFprobeBinary(config); !ok {
		return errors.New("no local ffprobe CLI found — it ships with ffmpeg and is required by the Audio Editor")
	}
	return nil
}

func (a *App) ResolveAudioEditSource(conversationID, artifactID string) (EditSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	artifactID = strings.TrimSpace(artifactID)
	if conversationID == "" || artifactID == "" {
		return EditSourceInfo{}, errors.New("a conversation id and artifact id are required to open the audio editor")
	}
	if err := requireAudioEditCLIs(config); err != nil {
		return EditSourceInfo{}, err
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return EditSourceInfo{}, err
	}
	content, originTurnID, ok := findAudioContent(detail, artifactID)
	if !ok {
		return EditSourceInfo{}, fmt.Errorf("audio %q not found in conversation %s", artifactID, conversationID)
	}
	cleanupEditSourcePreviews()
	return audioEditSourceInfoFor(config.Storage, conversationID, detail.Conversation.Title, originTurnID, content)
}

func (a *App) ResolveAudioEditInput(sessionID, artifactID string) (EditSourceInfo, error) {
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
	if detail.Conversation.Kind != editConversationKind || editSessionMediaKind(detail.Conversation.EditSession) != "audio" {
		return EditSourceInfo{}, fmt.Errorf("conversation %s is not an audio edit session", sessionID)
	}
	content, turnID, ok := findAudioContent(detail, artifactID)
	if !ok {
		return EditSourceInfo{}, fmt.Errorf("audio %q not found in session %s", artifactID, sessionID)
	}
	info, err := audioEditSourceInfoFor(config.Storage, sessionID, detail.Conversation.Title, turnID, content)
	if err == nil {
		info.ConversationID = sessionID
	}
	return info, err
}

// audioEditSourceInfoFor hydrates one audio content entry into an
// EditSourceInfo — identity, artifact URL, and the duration the cut timeline
// needs. Unlike the video path there is no preview copy: the URL serves the
// artifact's own bytes and the digest pins the submit to them.
func audioEditSourceInfoFor(storage ConfigStorage, conversationID, title, originTurnID string, content HistoryContent) (EditSourceInfo, error) {
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
	digest, err := fileDigest(absPath)
	if err != nil {
		return EditSourceInfo{}, err
	}
	config, cfgErr := loadReadyConfig()
	duration := 0.0
	if cfgErr == nil {
		if probe, probeErr := probeStagedMedia(context.Background(), config, absPath, "audio"); probeErr == nil {
			duration = probe.Duration
		}
	}
	if duration <= 0 {
		// No ffprobe (or it failed): the MP4 sniffer still covers m4a sources.
		if data, readErr := os.ReadFile(absPath); readErr == nil {
			if parsed, ok := mp4DurationSeconds(data); ok {
				duration = parsed
			}
		}
	}
	if duration <= 0 {
		return EditSourceInfo{}, errors.New("could not read the audio's duration — ffprobe (it ships with ffmpeg) is required by the Audio Editor")
	}
	mimeType := strings.TrimSpace(content.MimeType)
	if mimeType == "" {
		mimeType = mediaTypeForExtension(strings.ToLower(filepath.Ext(absPath)))
	}
	return EditSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		OriginTurnID:      originTurnID,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          mimeType,
		MediaKind:         "audio",
		DurationSeconds:   duration,
		SourceDigest:      digest,
	}, nil
}

// findAudioContent locates an audio content entry by artifact ID (or relative
// path fallback) across a conversation's turns, returning the owning turn id
// for provenance.
func findAudioContent(detail ConversationDetail, artifactID string) (HistoryContent, string, bool) {
	return findMediaContent(detail, artifactID, "audio")
}

func firstSessionSourceAudio(detail ConversationDetail) (HistoryContent, string, bool) {
	return firstSessionSourceMedia(detail, "audio")
}

// checkAudioEditSourceMatch refuses a submit whose cut set was derived from a
// different copy of the source than the one that will render — a stale draft
// must fail with guidance instead of cutting the wrong audio.
func checkAudioEditSourceMatch(trim *AudioTrimParams, duration float64, where, remedy string) error {
	if trim != nil && absDurationDelta(trim.Source.DurationSeconds, duration) > 0.25 {
		return fmt.Errorf("the submitted trim duration no longer matches %s — %s", where, remedy)
	}
	return nil
}

func (a *App) CancelAudioEdit(sessionConversationID, operationID string) error {
	return a.CancelImageEdit(sessionConversationID, operationID)
}

func (a *App) SubmitAudioEdit(req AudioEditSubmitRequest) (EditOperationState, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditOperationState{}, err
	}
	if err := requireAudioEditCLIs(config); err != nil {
		return EditOperationState{}, err
	}
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if req.Trim == nil {
		return EditOperationState{}, errors.New("trim parameters are required")
	}
	trim, err := validateAudioTrim(*req.Trim)
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
			return EditOperationState{}, errors.New("an input artifact can only be selected within an existing audio edit session")
		}
		sourceArtifactID := strings.TrimSpace(req.SourceArtifactID)
		if parentID == "" || sourceArtifactID == "" {
			return EditOperationState{}, errors.New("the source conversation and audio artifact must be named")
		}
		parentDetail, err := getConversation(config.Storage, parentID)
		if err != nil {
			return EditOperationState{}, fmt.Errorf("the conversation this audio belongs to is unavailable: %w", err)
		}
		if parentDetail.Conversation.Kind != "chat" {
			return EditOperationState{}, errors.New("this audio already belongs to an edit session — reopen that editor instead")
		}
		sourceContent, sourceTurnID, ok := findAudioContent(parentDetail, sourceArtifactID)
		if !ok {
			return EditOperationState{}, fmt.Errorf("audio %q not found in conversation %s", sourceArtifactID, parentID)
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
				return EditOperationState{}, errors.New("the source audio changed after the editor opened — reopen it and try again")
			}
		}
		duration, err := audioEditSourceDuration(config, sourcePath)
		if err != nil {
			return EditOperationState{}, err
		}
		if err := checkAudioEditSourceMatch(&trim, duration, "the source audio", "reopen the editor"); err != nil {
			return EditOperationState{}, err
		}
		sessionID, sourceURL, err = createAudioEditSession(store, parentDetail, sourceContent, sourceTurnID, sourcePath, nowText)
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
	if meta == nil || editSessionMediaKind(meta) != "audio" {
		return EditOperationState{}, fmt.Errorf("edit session %s is not an audio session", sessionID)
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
	inputContent, _, ok := findAudioContent(sessionDetail, inputArtifactID)
	if !ok {
		return EditOperationState{}, fmt.Errorf("the audio this edit builds on (%s) is not part of the session", inputArtifactID)
	}
	inputPath, err := contentArtifactPath(config.Storage, sessionID, inputContent)
	if err != nil {
		return EditOperationState{}, err
	}
	duration, err := audioEditSourceDuration(config, inputPath)
	if err != nil {
		return EditOperationState{}, err
	}
	if err := checkAudioEditSourceMatch(&trim, duration, "the selected audio source", "use the result as source again and retry"); err != nil {
		return EditOperationState{}, err
	}
	op := EditOperation{
		ID:              randomID("editop"),
		Kind:            editOperationKindAudioTrim,
		Status:          editOperationStatusQueued,
		CreatedAt:       nowText,
		InputArtifactID: inputArtifactID,
		Backend:         "ffmpeg",
		AudioTrim:       &trim,
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
	go a.executeAudioEditOperation(opCtx, opCancel, config, sessionID, turn.ID, op, inputPath)
	return EditOperationState{SessionConversationID: sessionID, CreatedSession: createdSession, Operation: op, SourceURL: sourceURL}, nil
}

// audioEditSourceDuration reports the render-facing duration of a source file:
// ffprobe first (codec-aware), the MP4 sniffer as the no-ffprobe fallback for
// m4a sources.
func audioEditSourceDuration(config AppConfig, path string) (float64, error) {
	probe, err := probeStagedMedia(context.Background(), config, path, "audio")
	if err == nil && probe.Duration > 0 {
		return probe.Duration, nil
	}
	if data, readErr := os.ReadFile(path); readErr == nil {
		if duration, ok := mp4DurationSeconds(data); ok && duration > 0 {
			return duration, nil
		}
	}
	if err != nil {
		return 0, fmt.Errorf("could not probe the audio: %w", err)
	}
	return 0, errors.New("could not read the audio's duration")
}

// createAudioEditSession writes the child conversation: record with lineage, a
// source turn carrying the session's own copy of the audio, and nothing else.
// The copy keeps the source's extension so the artifact stays byte-identical
// (no re-encode on open).
func createAudioEditSession(store HistoryStore, parentDetail ConversationDetail, sourceContent HistoryContent, sourceTurnID, sourcePath, nowText string) (string, string, error) {
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
	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp3"
	}
	artifactID := randomID("aud")
	filename := artifactID + extension
	dest := filepath.Join(workspace.ArtifactsDir, filename)
	if err := copyFile(sourcePath, dest); err != nil {
		return "", "", err
	}
	sourceEntry := HistoryContent{Type: "audio", ArtifactID: artifactID, Path: filepath.ToSlash(filepath.Join("artifacts", filename)), MimeType: mediaTypeForExtension(extension)}
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
			MediaKind:            "audio",
		},
	}
	sourceTurn := HistoryTurn{SchemaVersion: 1, ID: "turn_000001", ConversationID: workspace.ID, CreatedAt: nowText, Kind: editConversationKind, Role: "user", Content: []HistoryContent{sourceEntry}}
	if err := store.writeSnapshot(workspace, conversation, sourceTurn); err != nil {
		return "", "", err
	}
	created = true
	return workspace.ID, artifactPrefix + dest, nil
}

func (a *App) executeAudioEditOperation(ctx context.Context, cancel context.CancelFunc, config AppConfig, sessionID, turnID string, op EditOperation, inputPath string) {
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
			next.ResultDurationSeconds = 0
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
	// ffmpeg only processes the kept audio, so the progress denominator and
	// the expected result duration are the kept seconds, not the source's.
	processDuration, err := audioTrimKeptSeconds(*op.AudioTrim)
	staging := ""
	if err == nil {
		staging, err = os.MkdirTemp("", "atelier-audio-edit-*")
	}
	if staging != "" {
		defer os.RemoveAll(staging)
	}
	staged := ""
	if err == nil {
		staged = filepath.Join(staging, "trimmed.m4a")
		var args []string
		args, err = audioTrimFFmpegArgs(*op.AudioTrim, inputPath, staged)
		if err == nil {
			err = runLocalFFmpegProgress(ctx, config, processDuration, args, func(progress float64) {
				op.Progress = progress
				a.emitEditOperation(sessionID, op)
			})
		}
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		resultProbe, probeErr := probeStagedMedia(ctx, config, staged, "audio")
		if probeErr != nil {
			err = fmt.Errorf("could not verify the rendered audio: %w", probeErr)
		} else {
			err = verifyAudioEditResult(resultProbe, processDuration)
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
	tempResult := filepath.Join(staging, "result.m4a")
	if err := copyFile(staged, tempResult); err != nil {
		op.Status = editOperationStatusFailed
		op.Error = fmt.Sprintf("the edit ran but its result could not be staged: %v", err)
		_ = persistAndEmit(op)
		return
	}
	contents, _, writeErr := writeChatAudioArtifacts(config, loaded.ArtifactsDir, []ToolAudioFile{{TempPath: tempResult, MimeType: "audio/mp4"}})
	if writeErr != nil || len(contents) == 0 {
		op.Status = editOperationStatusFailed
		if writeErr == nil {
			writeErr = errors.New("the edit produced no audio")
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
	// Trim changes time, never anything else.
	op.ResultDurationSeconds = processDuration
	if err := persistAndEmit(op); err != nil {
		resultPath := filepath.Join(loaded.ArtifactsDir, filepath.Base(result.Path))
		_ = os.Remove(resultPath)
	}
}

// verifyAudioEditResult refuses a render that did not preserve what its
// operation promised: the kept duration, as playable audio.
func verifyAudioEditResult(result ToolProbeResult, expectedDuration float64) error {
	if result.AudioCodec == "" {
		return errors.New("the rendered audio has no audio track")
	}
	if absDurationDelta(result.Duration, expectedDuration) > 0.25 {
		return fmt.Errorf("the rendered audio did not keep the requested duration (%.2fs, want %.2fs)", result.Duration, expectedDuration)
	}
	return nil
}

func cleanupAudioEditStaging() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "atelier-audio-edit-") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
		}
	}
}
