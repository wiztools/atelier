package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// editTestHome redirects HOME at a temp dir so loadReadyConfig (and the
// config.json it reads) never touch the developer's real ~/.atelier, and
// writes the merged config for the bound methods to load.
func editTestHome(t *testing.T) AppConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := defaultAppConfig()
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}
	return config
}

// editTestFixturePNG encodes a solid w×h PNG.
func editTestFixturePNG(t *testing.T, w, h int, fill color.Color) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, editSolidImage(w, h, fill)); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func editTestMaskDataURL(t *testing.T, w, h int) string {
	t.Helper()
	mask := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			mask.Set(x, y, color.White)
		}
	}
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, mask); err != nil {
		t.Fatalf("png.Encode mask: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// writeEditParentFixture writes a chat conversation holding one user turn with
// one image artifact on disk, and returns the artifact id.
func writeEditParentFixture(t *testing.T, config AppConfig, id, title string, projectID string) string {
	t.Helper()
	artifactID := "img_" + id + "source01"
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            id,
		Kind:          "chat",
		Title:         title,
		CreatedAt:     "2026-10-01T10:00:00Z",
		UpdatedAt:     "2026-10-01T10:00:00Z",
		Workspace:     config.Tools.Filesystem.Root,
		ProjectID:     projectID,
		Stats:         HistoryConversationStats{TurnCount: 1, ArtifactCount: 1},
	}
	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             "turn_000001",
		ConversationID: id,
		CreatedAt:      conversation.CreatedAt,
		Kind:           "chat",
		Role:           "user",
		Content: []HistoryContent{{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       "artifacts/" + artifactID + ".png",
			MimeType:   "image/png",
			Width:      8,
			Height:     8,
		}},
	}
	writeSearchConversation(t, config.Storage, "2026/10/"+id, conversation, turn)
	dir := filepath.Join(config.Storage.History, "conversations", "2026", "10", id, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifactID+".png"), editTestFixturePNG(t, 8, 8, color.RGBA{R: 200, A: 255}), 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	return artifactID
}

// editTestOfflineApp builds an App whose HTTP transport refuses every
// request — the hermeticity guard for the execution path: whether or not the
// developer's keychain holds a real fal key, no provider call can leave the
// process.
func editTestOfflineApp() *App {
	app := NewApp()
	app.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("edit test: no network")
	})
	return app
}

// waitForEditTerminal polls the session until the operation reaches a
// terminal state (the execution runs on a goroutine).
func waitForEditTerminal(t *testing.T, storage ConfigStorage, sessionID, opID string) EditOperation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := getConversation(storage, sessionID)
		if err == nil {
			for _, turn := range detail.Turns {
				if op, ok := editOperationFromTurn(turn); ok && op.ID == opID {
					switch op.Status {
					case editOperationStatusCompleted, editOperationStatusFailed, editOperationStatusCancelled:
						return op
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("operation never reached a terminal state")
	return EditOperation{}
}

func TestSubmitImageEditValidatesBeforePersisting(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_p1", "Parent", "")
	app := editTestOfflineApp()

	// Count conversation dirs before; every invalid request must create none.
	historyRoot := filepath.Join(config.Storage.History, "conversations")
	dirCount := func() int {
		entries, _ := os.ReadDir(historyRoot)
		return len(entries)
	}
	before := dirCount()

	valid := ImageEditSubmitRequest{
		ParentConversationID: "conv_edit_p1",
		SourceArtifactID:     artifactID,
		Prompt:               "replace the background with a beach",
		MaskPng:              editTestMaskDataURL(t, 8, 8),
	}

	cases := []struct {
		name string
		mut  func(req *ImageEditSubmitRequest)
	}{
		{"missing prompt", func(r *ImageEditSubmitRequest) { r.Prompt = " " }},
		{"missing mask", func(r *ImageEditSubmitRequest) { r.MaskPng = "" }},
		{"non-image mask", func(r *ImageEditSubmitRequest) { r.MaskPng = "data:image/png;base64,bm90LWEtcG5n" }},
		{"dimension mismatch", func(r *ImageEditSubmitRequest) { r.MaskPng = editTestMaskDataURL(t, 4, 4) }},
		{"unknown provider", func(r *ImageEditSubmitRequest) { r.Provider = "openai" }},
		{"unknown artifact", func(r *ImageEditSubmitRequest) { r.SourceArtifactID = "img_missing0001" }},
		{"unknown kind", func(r *ImageEditSubmitRequest) { r.Kind = "straighten" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mut(&req)
			if _, err := app.SubmitImageEdit(req); err == nil {
				t.Fatal("expected validation error")
			}
			if got := dirCount(); got != before {
				t.Fatalf("invalid request created conversation dirs: %d → %d", before, got)
			}
		})
	}
}

func TestSubmitImageEditCropCompletesLocally(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_crop1", "Parent", "")
	app := editTestOfflineApp()

	state, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		Kind:                 editOperationKindCrop,
		ParentConversationID: "conv_edit_crop1",
		SourceArtifactID:     artifactID,
		Crop: &CropOperationParams{
			X: 2, Y: 1, Width: 3, Height: 4,
			SourceWidth: 8, SourceHeight: 8,
			AspectRatio: "free",
		},
	})
	if err != nil {
		t.Fatalf("SubmitImageEdit crop returned error: %v", err)
	}
	op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
	if op.Status != editOperationStatusCompleted {
		t.Fatalf("crop op status = %q (%s), want completed", op.Status, op.Error)
	}
	if op.Crop == nil || op.Inpaint != nil || op.Provider != "" || op.Model != "" || op.Backend != "local" {
		t.Fatalf("crop op attribution/payload = %+v", op)
	}
	if op.ResultWidth != 3 || op.ResultHeight != 4 || op.ResultArtifactID == "" {
		t.Fatalf("crop result = %+v", op)
	}
	detail, err := getConversation(config.Storage, state.SessionConversationID)
	if err != nil {
		t.Fatalf("session record: %v", err)
	}
	resultContent, _, ok := findImageContent(detail, op.ResultArtifactID)
	if !ok {
		t.Fatal("completed crop result is not a session image")
	}
	resultPath, err := contentArtifactPath(config.Storage, state.SessionConversationID, resultContent)
	if err != nil {
		t.Fatalf("result path: %v", err)
	}
	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	resultImg, err := png.Decode(bytes.NewReader(resultData))
	if err != nil {
		t.Fatalf("crop result is not PNG: %v", err)
	}
	if resultImg.Bounds().Dx() != 3 || resultImg.Bounds().Dy() != 4 {
		t.Fatalf("crop result bounds = %v", resultImg.Bounds())
	}
}

func TestSubmitImageEditCreatesSessionAndRecordsFailure(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_p2", "Parent", "proj_edit_1")
	app := editTestOfflineApp()

	state, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		ParentConversationID: "conv_edit_p2",
		SourceArtifactID:     artifactID,
		Prompt:               "replace the background with a beach",
		MaskPng:              editTestMaskDataURL(t, 8, 8),
	})
	if err != nil {
		t.Fatalf("SubmitImageEdit returned error: %v", err)
	}
	if !state.CreatedSession || state.SessionConversationID == "" {
		t.Fatalf("first submit must create a session, got %+v", state)
	}

	detail, err := getConversation(config.Storage, state.SessionConversationID)
	if err != nil {
		t.Fatalf("session record missing: %v", err)
	}
	conversation := detail.Conversation
	if conversation.Kind != editConversationKind {
		t.Fatalf("session kind = %q", conversation.Kind)
	}
	meta := conversation.EditSession
	if meta == nil {
		t.Fatal("session record carries no EditSession meta")
	}
	if meta.ParentConversationID != "conv_edit_p2" || meta.SourceArtifactID != artifactID || meta.SourceTitleSnapshot != "Parent" {
		t.Fatalf("session provenance = %+v", meta)
	}
	// Inheritance: project, workspace, and the differential override pointer.
	if conversation.ProjectID != "proj_edit_1" {
		t.Fatalf("session project = %q, want the parent's", conversation.ProjectID)
	}
	if conversation.Workspace != config.Tools.Filesystem.Root {
		t.Fatalf("session workspace = %q, want the parent's", conversation.Workspace)
	}
	// The child owns its source copy.
	if len(detail.Turns) < 1 || detail.Turns[0].Role != "user" || len(detail.Turns[0].Content) != 1 {
		t.Fatalf("session source turn = %+v", detail.Turns)
	}
	sourceEntry := detail.Turns[0].Content[0]
	if sourceEntry.ArtifactID == artifactID {
		t.Fatal("source copy must be a child-owned artifact, not a reference to the parent's")
	}
	sourcePath, err := contentArtifactPath(config.Storage, state.SessionConversationID, sourceEntry)
	if err != nil || sourceEntry.Path == "" {
		t.Fatalf("source copy path: %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("source copy missing on disk: %v", err)
	}
	if state.SourceURL == "" {
		t.Fatal("submit state must carry the hydrated source URL")
	}

	// The queued op turn exists; execution (offline) lands it in a terminal
	// failed state with the mask retained for retry.
	queuedFound := false
	for _, turn := range detail.Turns {
		if op, ok := editOperationFromTurn(turn); ok && op.ID == state.Operation.ID {
			queuedFound = true
			if op.Status != editOperationStatusQueued && op.Status != editOperationStatusFailed && op.Status != editOperationStatusRunning {
				t.Fatalf("unexpected op status %q", op.Status)
			}
			if op.Provider != "fal" || op.Model != defaultFalInpaintModel {
				t.Fatalf("effective pair = %s/%s, want the merged default %s", op.Provider, op.Model, defaultFalInpaintModel)
			}
			if op.Inpaint == nil || op.Inpaint.Prompt == "" || op.Inpaint.MaskArtifactID == "" {
				t.Fatalf("op payload = %+v", op.Inpaint)
			}
			maskPath, err := contentArtifactPath(config.Storage, state.SessionConversationID, HistoryContent{Path: op.Inpaint.MaskPath})
			if err != nil {
				t.Fatalf("mask artifact path: %v", err)
			}
			if _, err := os.Stat(maskPath); err != nil {
				t.Fatalf("mask artifact missing: %v", err)
			}
		}
	}
	if !queuedFound {
		t.Fatal("operation turn not persisted at submit time")
	}

	op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
	if op.Status != editOperationStatusFailed {
		t.Fatalf("offline execution should fail the op, got %q (%s)", op.Status, op.Error)
	}
	if op.Error == "" || op.ResultArtifactID != "" {
		t.Fatalf("failed op must carry an error and no result: %+v", op)
	}
	// The failure is retryable: inputs retained.
	if op.Inpaint == nil || op.Inpaint.MaskArtifactID == "" || op.InputArtifactID == "" {
		t.Fatalf("failed op must keep its inputs: %+v", op)
	}
}

func TestSubmitImageEditRejectsConcurrentSubmit(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_p3", "Parent", "")
	app := editTestOfflineApp()

	state, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		ParentConversationID: "conv_edit_p3",
		SourceArtifactID:     artifactID,
		Prompt:               "make it night",
		MaskPng:              editTestMaskDataURL(t, 8, 8),
	})
	if err != nil {
		t.Fatalf("first submit returned error: %v", err)
	}
	// Reserve a separate live operation so the first operation’s completion
	// cannot remove the concurrency guard during this assertion.
	app.editOpsMu.Lock()
	app.editOps["concurrent-test-op"] = &editOpRun{conversationID: state.SessionConversationID, cancel: func() {}}
	app.editOpsMu.Unlock()
	defer func() {
		app.editOpsMu.Lock()
		delete(app.editOps, "concurrent-test-op")
		app.editOpsMu.Unlock()
	}()

	if _, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		ParentConversationID:  "conv_edit_p3",
		SourceArtifactID:      artifactID,
		SessionConversationID: state.SessionConversationID,
		Prompt:                "make it day",
		MaskPng:               editTestMaskDataURL(t, 8, 8),
	}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("already running")) {
		t.Fatalf("second submit must be refused while one is running, got %v", err)
	}

	if err := app.CancelImageEdit(state.SessionConversationID, state.Operation.ID); err != nil {
		t.Fatalf("CancelImageEdit returned error: %v", err)
	}
	waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
}

func writeEditSessionFixture(t *testing.T, config AppConfig, parentID, sessionID string) (sourceID, resultID, opID string) {
	t.Helper()
	sourceID = "img_" + sessionID + "src001"
	resultID = "img_" + sessionID + "res001"
	opID = "editop_" + sessionID
	sessionDir := filepath.Join(config.Storage.History, "conversations", "2026", "10", sessionID)
	artifacts := filepath.Join(sessionDir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatalf("MkdirAll session artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, sourceID+".png"), editTestFixturePNG(t, 8, 8, color.RGBA{G: 100, A: 255}), 0o644); err != nil {
		t.Fatalf("write source copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, resultID+".png"), editTestFixturePNG(t, 8, 8, color.RGBA{B: 220, A: 255}), 0o644); err != nil {
		t.Fatalf("write result: %v", err)
	}
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            sessionID,
		Kind:          editConversationKind,
		Title:         "Edit · Parent",
		CreatedAt:     "2026-10-02T10:00:00Z",
		UpdatedAt:     "2026-10-02T10:00:00Z",
		EditSession: &EditSessionMeta{
			ParentConversationID: parentID,
			SourceTurnID:         "turn_000001",
			// Production records the PARENT-side artifact id here (where the
			// image came from) while the child's own copy rides under a fresh
			// id — mirror that so the source lookup stays honest.
			SourceArtifactID:    "img_" + parentID + "source01",
			SourceTitleSnapshot: "Parent",
		},
		Stats: HistoryConversationStats{TurnCount: 2, ArtifactCount: 2},
	}
	sourceTurn := HistoryTurn{
		SchemaVersion: 1, ID: "turn_000001", ConversationID: sessionID,
		CreatedAt: conversation.CreatedAt, Kind: editConversationKind, Role: "user",
		Content: []HistoryContent{{Type: "image", ArtifactID: sourceID, Path: "artifacts/" + sourceID + ".png", MimeType: "image/png", Width: 8, Height: 8}},
	}
	op := EditOperation{
		ID: opID, Kind: editOperationKindInpaint, Status: editOperationStatusCompleted,
		CreatedAt: "2026-10-02T10:01:00Z", CompletedAt: "2026-10-02T10:01:30Z",
		InputArtifactID: sourceID, ResultArtifactID: resultID,
		ResultPath: "artifacts/" + resultID + ".png", ResultMimeType: "image/png",
		ResultWidth: 8, ResultHeight: 8,
		Provider: "fal", Model: "fal-test-inpaint", CostMicros: 4200,
		Inpaint: &InpaintOperationParams{
			Prompt:         "add a lighthouse",
			MaskArtifactID: "msk_" + sessionID,
			MaskPath:       "artifacts/msk_" + sessionID + ".png",
			MaskWidth:      8, MaskHeight: 8,
		},
	}
	opTurn := HistoryTurn{
		SchemaVersion: 1, ID: "turn_000002", ConversationID: sessionID,
		CreatedAt: op.CreatedAt, Kind: editConversationKind, Role: "assistant",
		Provider: op.Provider, Model: op.Model,
		Request: map[string]any{"operation": op},
		Content: editOperationContents(op),
	}
	writeSearchConversation(t, config.Storage, "2026/10/"+sessionID, conversation, sourceTurn, opTurn)
	return sourceID, resultID, opID
}

func TestAddEditResultToConversation(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_p4", "Parent", "")
	_, _, opID := writeEditSessionFixture(t, config, "conv_edit_p4", "conv_edit_s4")
	app := NewApp()

	summary, err := app.AddEditResultToConversation("conv_edit_s4", opID)
	if err != nil {
		t.Fatalf("AddEditResultToConversation returned error: %v", err)
	}
	if summary.ID != "conv_edit_p4" || summary.TurnCount != 2 {
		t.Fatalf("parent summary after adoption = %+v", summary)
	}
	parentDetail, err := getConversation(config.Storage, "conv_edit_p4")
	if err != nil {
		t.Fatalf("parent record: %v", err)
	}
	var adoptionTurn *HistoryTurn
	for i := range parentDetail.Turns {
		if parentDetail.Turns[i].ID == "turn_000002" {
			adoptionTurn = &parentDetail.Turns[i]
		}
	}
	if adoptionTurn == nil {
		t.Fatal("adoption turn not appended to the parent")
	}
	if len(adoptionTurn.Content) != 2 || adoptionTurn.Content[1].Type != "image" {
		t.Fatalf("adoption turn content = %+v", adoptionTurn.Content)
	}
	if adoptionTurn.Content[1].ArtifactID == "img_conv_edit_s4res001" {
		t.Fatal("adoption must copy bytes into parent-owned storage, not reference the session's artifact")
	}
	if adoptionTurn.ProviderResponse["editAdoption"] == nil {
		t.Fatal("adoption turn carries no child provenance")
	}
	copiedPath, err := contentArtifactPath(config.Storage, "conv_edit_p4", adoptionTurn.Content[1])
	if err != nil {
		t.Fatalf("copied artifact path: %v", err)
	}
	if _, err := os.Stat(copiedPath); err != nil {
		t.Fatalf("copied artifact missing: %v", err)
	}
	// The op records its adoption — the idempotency mark.
	adoptedDetail, err := getConversation(config.Storage, "conv_edit_s4")
	if err != nil {
		t.Fatalf("session record: %v", err)
	}
	adopted := false
	for _, turn := range adoptedDetail.Turns {
		if op, ok := editOperationFromTurn(turn); ok && op.ID == opID {
			if op.AdoptedAt == "" {
				t.Fatal("op adoption mark not recorded")
			}
			adopted = true
		}
	}
	if !adopted {
		t.Fatal("adopted op not found")
	}

	// Repeated adoption is refused and adds nothing.
	if _, err := app.AddEditResultToConversation("conv_edit_s4", opID); err == nil {
		t.Fatal("repeated adoption must be refused")
	}
	again, err := getConversation(config.Storage, "conv_edit_p4")
	if err != nil {
		t.Fatalf("parent record after refused adoption: %v", err)
	}
	if len(again.Turns) != len(parentDetail.Turns) {
		t.Fatalf("refused adoption must not append another turn (%d → %d)", len(parentDetail.Turns), len(again.Turns))
	}
}

func TestSweepInterruptedEditOps(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_p5", "Parent", "")
	_, _, opID := writeEditSessionFixture(t, config, "conv_edit_p5", "conv_edit_s5")

	// Push the completed op back to queued, then sweep.
	if err := mutateEditOperation(config.Storage, "conv_edit_s5", opID, func(op *EditOperation) {
		op.Status = editOperationStatusQueued
		op.CompletedAt = ""
	}); err != nil {
		t.Fatalf("mutateEditOperation: %v", err)
	}
	sweepInterruptedEditOps(config.Storage)

	detail, err := getConversation(config.Storage, "conv_edit_s5")
	if err != nil {
		t.Fatalf("session record: %v", err)
	}
	for _, turn := range detail.Turns {
		op, ok := editOperationFromTurn(turn)
		if !ok || op.ID != opID {
			continue
		}
		if op.Status != editOperationStatusFailed {
			t.Fatalf("swept op status = %q, want failed", op.Status)
		}
		if op.Error == "" {
			t.Fatal("swept op must explain the interruption")
		}
	}
}

func TestListEditSessionReportsParentAvailability(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_p6", "Parent", "")
	sourceID, _, opID := writeEditSessionFixture(t, config, "conv_edit_p6", "conv_edit_s6")
	app := NewApp()

	state, err := app.ListEditSession("conv_edit_s6")
	if err != nil {
		t.Fatalf("ListEditSession returned error: %v", err)
	}
	if !state.ParentAvailable || state.ParentTitle != "Parent" {
		t.Fatalf("parent availability = %+v", state)
	}
	if len(state.Operations) != 1 || state.Operations[0].ID != opID {
		t.Fatalf("operations = %+v", state.Operations)
	}
	resultURL := state.Operations[0].ResultURL
	if resultURL == "" {
		t.Fatal("result URL must be hydrated for the editor")
	}
	// The URL must resolve to the real artifact on disk — a double-joined
	// "artifacts/artifacts/…" path (ResultPath already carries the segment)
	// would 404 the preview while the file exists.
	resultPath := strings.TrimPrefix(resultURL, artifactPrefix)
	if strings.Contains(resultPath, "artifacts/artifacts") {
		t.Fatalf("result URL double-joins the artifacts segment: %s", resultURL)
	}
	if _, err := os.Stat(resultPath); err != nil {
		t.Fatalf("result URL does not resolve on disk (%s): %v", resultURL, err)
	}
	// The source info must describe the session's OWN copy — the first user
	// turn's image entry — not the parent-side artifact id the meta records.
	if state.Source.ArtifactID == "" || state.Source.URL == "" {
		t.Fatalf("source = %+v", state.Source)
	}
	if state.Source.ArtifactID != sourceID {
		t.Fatalf("source artifact = %q, want the session's own copy %q", state.Source.ArtifactID, sourceID)
	}
	if _, err := os.Stat(strings.TrimPrefix(state.Source.URL, artifactPrefix)); err != nil {
		t.Fatalf("source URL does not resolve on disk (%s): %v", state.Source.URL, err)
	}

	// Deleting the parent degrades the breadcrumb, not the load.
	if err := deleteConversation(config.Storage, "conv_edit_p6"); err != nil {
		t.Fatalf("deleteConversation: %v", err)
	}
	state, err = app.ListEditSession("conv_edit_s6")
	if err != nil {
		t.Fatalf("ListEditSession after parent deletion: %v", err)
	}
	if state.ParentAvailable {
		t.Fatal("deleted parent must report unavailable")
	}
	if state.ParentTitle != "Parent" {
		t.Fatalf("breadcrumb must fall back to the title snapshot, got %q", state.ParentTitle)
	}
}

func TestResolveEditSource(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_p7", "Parent", "")
	app := NewApp()

	info, err := app.ResolveEditSource("conv_edit_p7", artifactID)
	if err != nil {
		t.Fatalf("ResolveEditSource returned error: %v", err)
	}
	if info.ConversationID != "conv_edit_p7" || info.ArtifactID != artifactID || info.Width != 8 || info.Height != 8 {
		t.Fatalf("source info = %+v", info)
	}
	if info.URL == "" {
		t.Fatal("source info must carry a renderable URL")
	}
	if _, err := app.ResolveEditSource("conv_edit_p7", "img_missing0001"); err == nil {
		t.Fatal("unknown artifact must be refused")
	}
	if _, err := app.ResolveEditSource("conv_missing00", artifactID); err == nil {
		t.Fatal("unknown conversation must be refused")
	}
}

func TestSubmitImageEditIterationOnReopenedSession(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_p8", "Parent", "")
	_, resultID, _ := writeEditSessionFixture(t, config, "conv_edit_p8", "conv_edit_s8")
	app := editTestOfflineApp()

	// The reopen flow sends the session's own source-copy id as the source —
	// an iteration must resolve lineage from the session record, never
	// re-resolve the parent-side artifact.
	state, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		ParentConversationID:  "conv_edit_p8",
		SourceArtifactID:      "img_conv_edit_s8src001",
		SessionConversationID: "conv_edit_s8",
		Prompt:                "make it sunset",
		MaskPng:               editTestMaskDataURL(t, 8, 8),
	})
	if err != nil {
		t.Fatalf("iteration submit returned error: %v", err)
	}
	if state.CreatedSession {
		t.Fatal("an iteration must not create another session")
	}
	op := waitForEditTerminal(t, config.Storage, "conv_edit_s8", state.Operation.ID)
	if op.Status != editOperationStatusFailed {
		t.Fatalf("offline iteration should fail, got %q (%s)", op.Status, op.Error)
	}
	// The chain advances: an unnamed input defaults to the latest completed
	// result, not the source copy.
	if op.InputArtifactID != resultID {
		t.Fatalf("iteration input = %q, want the latest result %q", op.InputArtifactID, resultID)
	}
}

func TestInpaintChangeNotices(t *testing.T) {
	source := editTestFixturePNG(t, 32, 32, color.RGBA{R: 120, G: 80, B: 40, A: 255})
	maskImg := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			maskImg.Set(x, y, color.White)
		}
	}
	mask := editTestPNG(t, maskImg)

	// Model returned the source verbatim: the unchanged-selection notice
	// fires, the outside-change notice does not (nothing changed outside).
	same := source
	notices := inpaintChangeNotices(source, same, mask)
	if len(notices) != 1 || !bytes.Contains([]byte(notices[0]), []byte("essentially unchanged")) {
		t.Fatalf("identical output notices = %v", notices)
	}

	// Model edited the COMPLEMENT (everything outside the selection went
	// black): both diagnostics fire; outside changes are kept.
	inverted := editSolidImage(32, 32, color.Black)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			inverted.Set(x, y, color.RGBA{R: 120, G: 80, B: 40, A: 255})
		}
	}
	notices = inpaintChangeNotices(source, editTestPNG(t, inverted), mask)
	if len(notices) != 2 || !strings.Contains(notices[1], "changes were kept") || strings.Contains(notices[1], "polarity") {
		t.Fatalf("outside-change notices = %v", notices)
	}

	// Model edited the selection (white block area now dark) and left the
	// rest alone: no notices.
	edited := editSolidImage(32, 32, color.RGBA{R: 120, G: 80, B: 40, A: 255})
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			edited.Set(x, y, color.RGBA{R: 10, G: 10, B: 10, A: 255})
		}
	}
	if notices := inpaintChangeNotices(source, editTestPNG(t, edited), mask); len(notices) != 0 {
		t.Fatalf("edited-selection notices = %v", notices)
	}

	// A small change outside the mask must not be diluted by the whole image.
	edited.Set(31, 31, color.White)
	if notices := inpaintChangeNotices(source, editTestPNG(t, edited), mask); len(notices) != 1 || !strings.Contains(notices[0], "changes were kept") {
		t.Fatalf("localized outside change notices = %v", notices)
	}

	// Size differences are accepted and compared on a temporary aligned copy.
	if notices := inpaintChangeNotices(source, editTestFixturePNG(t, 8, 8, color.Black), mask); len(notices) != 2 || !strings.Contains(notices[0], "original dimensions") || !strings.Contains(notices[1], "resizing may contribute") {
		t.Fatalf("resized output notices = %v", notices)
	}
	// Different shapes cannot be meaningfully compared through the source mask.
	if notices := inpaintChangeNotices(source, editTestFixturePNG(t, 8, 16, color.Black), mask); len(notices) != 2 || !strings.Contains(notices[1], "could not be checked") {
		t.Fatalf("different shape notices = %v", notices)
	}
	// A malformed mask fails soft rather than causing an out-of-bounds comparison.
	if notices := inpaintChangeNotices(source, source, editTestFixturePNG(t, 1, 1, color.White)); notices != nil {
		t.Fatalf("mismatched mask notices = %v", notices)
	}
}

func TestDeleteEditOperationRemovesTurnAndArtifacts(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_del1", "Parent", "")
	sourceID, resultID, opID := writeEditSessionFixture(t, config, "conv_edit_del1", "conv_edit_sdel1")
	app := NewApp()

	sessionDir := filepath.Join(config.Storage.History, "conversations", "2026", "10", "conv_edit_sdel1")
	turnPath := filepath.Join(sessionDir, "turns", "turn_000002.json")
	resultPath := filepath.Join(sessionDir, "artifacts", resultID+".png")
	maskPath := filepath.Join(sessionDir, "artifacts", "msk_conv_edit_sdel1.png")
	sourcePath := filepath.Join(sessionDir, "artifacts", sourceID+".png")
	// The fixture records the mask on the op record without writing the file.
	if err := os.WriteFile(maskPath, editTestFixturePNG(t, 8, 8, color.White), 0o644); err != nil {
		t.Fatalf("write mask fixture: %v", err)
	}
	for _, path := range []string{turnPath, resultPath, maskPath, sourcePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fixture file missing: %s", path)
		}
	}

	if err := app.DeleteEditOperation("conv_edit_sdel1", opID); err != nil {
		t.Fatalf("DeleteEditOperation returned error: %v", err)
	}
	for _, path := range []string{turnPath, resultPath, maskPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted edit's file still on disk: %s", path)
		}
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("the session's source copy must survive the delete: %s", sourcePath)
	}
	detail, err := getConversation(config.Storage, "conv_edit_sdel1")
	if err != nil {
		t.Fatalf("session record: %v", err)
	}
	if detail.Conversation.Stats.TurnCount != 1 || detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("stats after delete = %+v, want 1/1", detail.Conversation.Stats)
	}
	state, err := app.ListEditSession("conv_edit_sdel1")
	if err != nil {
		t.Fatalf("ListEditSession: %v", err)
	}
	if len(state.Operations) != 0 {
		t.Fatalf("operations after delete = %d, want 0", len(state.Operations))
	}
	// The session itself stays usable, and a repeat delete fails cleanly.
	if err := app.DeleteEditOperation("conv_edit_sdel1", opID); err == nil {
		t.Fatal("deleting an already-deleted operation must fail")
	}
}

func TestDeleteEditOperationNumberingSurvivesGap(t *testing.T) {
	config := editTestHome(t)
	artifactID := writeEditParentFixture(t, config, "conv_edit_del2", "Parent", "")
	sourceID, _, opID := writeEditSessionFixture(t, config, "conv_edit_del2", "conv_edit_sdel2")
	app := editTestOfflineApp()

	// A second op turn sits above the fixture's turn_000002, so deleting the
	// first op leaves the highest-numbered turn in place — the exact shape
	// that turns a count-based next number into an overwrite.
	secondOp := EditOperation{
		ID: "editop_sdel2_second", Kind: editOperationKindCrop, Status: editOperationStatusFailed,
		CreatedAt: "2026-10-02T10:02:00Z", CompletedAt: "2026-10-02T10:02:10Z",
		InputArtifactID: sourceID, Error: "offline",
		Crop: &CropOperationParams{X: 0, Y: 0, Width: 4, Height: 4, SourceWidth: 8, SourceHeight: 8},
	}
	secondTurn := HistoryTurn{
		SchemaVersion: 1, ID: "turn_000003", ConversationID: "conv_edit_sdel2",
		CreatedAt: secondOp.CreatedAt, Kind: editConversationKind, Role: "assistant",
		Request: map[string]any{"operation": secondOp},
		Content: editOperationContents(secondOp),
	}
	turnsDir := filepath.Join(config.Storage.History, "conversations", "2026", "10", "conv_edit_sdel2", "turns")
	if err := writeJSONFile(filepath.Join(turnsDir, "turn_000003.json"), secondTurn); err != nil {
		t.Fatalf("write second op turn: %v", err)
	}

	if err := app.DeleteEditOperation("conv_edit_sdel2", opID); err != nil {
		t.Fatalf("DeleteEditOperation returned error: %v", err)
	}
	loaded, err := loadForEditAppend(config.Storage, "conv_edit_sdel2")
	if err != nil {
		t.Fatalf("loadForEditAppend: %v", err)
	}
	if loaded.NextTurnNumber != 4 {
		t.Fatalf("NextTurnNumber after gap = %d, want 4", loaded.NextTurnNumber)
	}

	// The next append must land past the gap, never on the surviving turn.
	state, err := app.SubmitImageEdit(ImageEditSubmitRequest{
		ParentConversationID:  "conv_edit_del2",
		SourceArtifactID:      artifactID,
		SessionConversationID: "conv_edit_sdel2",
		Prompt:                "make it night",
		MaskPng:               editTestMaskDataURL(t, 8, 8),
	})
	if err != nil {
		t.Fatalf("iteration submit returned error: %v", err)
	}
	defer waitForEditTerminal(t, config.Storage, "conv_edit_sdel2", state.Operation.ID)
	var survivor HistoryTurn
	if err := readJSONFile(filepath.Join(turnsDir, "turn_000003.json"), &survivor); err != nil {
		t.Fatalf("read turn_000003 after new submit: %v", err)
	}
	if op, ok := editOperationFromTurn(survivor); !ok || op.ID != secondOp.ID {
		t.Fatalf("the surviving turn was overwritten: %+v", survivor)
	}
	var latest HistoryTurn
	if err := readJSONFile(filepath.Join(turnsDir, "turn_000004.json"), &latest); err != nil {
		t.Fatalf("new op turn missing at turn_000004: %v", err)
	}
	if op, ok := editOperationFromTurn(latest); !ok || op.ID != state.Operation.ID {
		t.Fatalf("turn_000004 does not carry the new operation: %+v", latest)
	}
}

func TestDeleteEditOperationRefusesWhileBusy(t *testing.T) {
	config := editTestHome(t)
	writeEditParentFixture(t, config, "conv_edit_del3", "Parent", "")
	_, _, opID := writeEditSessionFixture(t, config, "conv_edit_del3", "conv_edit_sdel3")
	app := NewApp()

	// A live op in the session blocks every delete: the executor may be
	// rewriting the conversation record concurrently.
	app.editOpsMu.Lock()
	app.editOps["busy-op"] = &editOpRun{conversationID: "conv_edit_sdel3", cancel: func() {}}
	app.editOpsMu.Unlock()
	if err := app.DeleteEditOperation("conv_edit_sdel3", opID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("delete must be refused while the session has a live op, got %v", err)
	}
	app.editOpsMu.Lock()
	delete(app.editOps, "busy-op")
	app.editOpsMu.Unlock()

	// With the session idle, a queued/running TARGET is still refused —
	// cancel it first.
	turnPath := filepath.Join(config.Storage.History, "conversations", "2026", "10", "conv_edit_sdel3", "turns", "turn_000002.json")
	var turn HistoryTurn
	if err := readJSONFile(turnPath, &turn); err != nil {
		t.Fatalf("read op turn: %v", err)
	}
	op, _ := editOperationFromTurn(turn)
	op.Status = editOperationStatusRunning
	turn.Request = map[string]any{"operation": op}
	if err := writeJSONFile(turnPath, turn); err != nil {
		t.Fatalf("rewrite op turn as running: %v", err)
	}
	if err := app.DeleteEditOperation("conv_edit_sdel3", opID); err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("delete of a running operation must be refused, got %v", err)
	}
	if _, err := os.Stat(turnPath); err != nil {
		t.Fatalf("refused delete removed the turn file: %v", err)
	}
	if err := app.DeleteEditOperation("conv_edit_sdel3", "editop_unknown1"); err == nil {
		t.Fatal("unknown operation id must fail")
	}
}
