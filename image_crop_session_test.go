package main

import (
	"context"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCropRejectsInvalidDraftWithoutCreatingSession(t *testing.T) {
	config := editTestHome(t)
	artifact := writeEditParentFixture(t, config, "conv_crop_invalid", "Parent", "")
	app := editTestOfflineApp()
	count := func() int {
		n := 0
		_ = filepath.WalkDir(config.Storage.History, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.Name() == "conversation.json" {
				n++
			}
			return nil
		})
		return n
	}
	before := count()
	tests := []struct {
		name   string
		mutate func(*ImageEditSubmitRequest)
	}{
		{"missing crop", func(req *ImageEditSubmitRequest) { req.Crop = nil }},
		{"negative origin", func(req *ImageEditSubmitRequest) { req.Crop.X = -1 }},
		{"outside bounds", func(req *ImageEditSubmitRequest) { req.Crop.Width = 20 }},
		{"stale dimensions", func(req *ImageEditSubmitRequest) { req.Crop.SourceWidth = 9 }},
		{"stale source bytes", func(req *ImageEditSubmitRequest) { req.SourceDigest = "changed" }},
		{"invalid input", func(req *ImageEditSubmitRequest) { req.InputArtifactID = "foreign" }},
		{"malformed ratio", func(req *ImageEditSubmitRequest) { req.Crop.AspectRatio = "1junk:1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := ImageEditSubmitRequest{Kind: "crop", ParentConversationID: "conv_crop_invalid", SourceArtifactID: artifact,
				Crop: &CropOperationParams{Width: 4, Height: 4, SourceWidth: 8, SourceHeight: 8}}
			test.mutate(&req)
			if _, err := app.SubmitImageEdit(req); err == nil {
				t.Fatal("expected invalid crop to be refused")
			}
			if count() != before {
				t.Fatal("invalid draft created an edit session")
			}
		})
	}
}

func TestCropSessionChainingAdoptionAndInpaint(t *testing.T) {
	config := editTestHome(t)
	artifact := writeEditParentFixture(t, config, "conv_crop_chain", "Parent", "")
	app := editTestOfflineApp()
	source, err := app.ResolveEditSource("conv_crop_chain", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceDigest == "" {
		t.Fatal("preview must identify source bytes")
	}
	first, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: "crop", ParentConversationID: "conv_crop_chain", SourceArtifactID: artifact, SourceDigest: source.SourceDigest,
		Crop: &CropOperationParams{X: 1, Y: 1, Width: 6, Height: 6, SourceWidth: 8, SourceHeight: 8, AspectRatio: "1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	firstOp := waitForEditTerminal(t, config.Storage, first.SessionConversationID, first.Operation.ID)
	if firstOp.Status != "completed" {
		t.Fatalf("first crop failed: %s", firstOp.Error)
	}
	reopened, err := app.ListEditSession(first.SessionConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Operations) != 1 || reopened.Operations[0].ResultURL == "" {
		t.Fatal("completed crop missing from reopened session")
	}
	path, err := findConversationPath(config.Storage, first.SessionConversationID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "msk_") {
			t.Fatal("crop created a mask artifact")
		}
	}
	second, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: "crop", SessionConversationID: first.SessionConversationID, InputArtifactID: firstOp.ResultArtifactID,
		Crop: &CropOperationParams{X: 2, Y: 1, Width: 3, Height: 4, SourceWidth: 6, SourceHeight: 6}})
	if err != nil {
		t.Fatal(err)
	}
	secondOp := waitForEditTerminal(t, config.Storage, first.SessionConversationID, second.Operation.ID)
	if secondOp.Status != "completed" || secondOp.InputArtifactID != firstOp.ResultArtifactID || secondOp.ResultWidth != 3 || secondOp.ResultHeight != 4 {
		t.Fatalf("chained crop: %+v", secondOp)
	}
	if _, err := app.AddEditResultToConversation(first.SessionConversationID, secondOp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddEditResultToConversation(first.SessionConversationID, secondOp.ID); err == nil {
		t.Fatal("duplicate adoption was accepted")
	}
	parent, err := getConversation(config.Storage, "conv_crop_chain")
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.Turns) != 2 || !strings.Contains(parent.Turns[1].Content[0].Text, "crop") {
		t.Fatal("parent is missing crop adoption")
	}
	inpaint, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: "inpaint", SessionConversationID: first.SessionConversationID, InputArtifactID: secondOp.ResultArtifactID,
		Prompt: "replace the subject", MaskPng: editTestMaskDataURL(t, 3, 4)})
	if err != nil {
		t.Fatal(err)
	}
	inpaintOp := waitForEditTerminal(t, config.Storage, first.SessionConversationID, inpaint.Operation.ID)
	if inpaintOp.InputArtifactID != secondOp.ResultArtifactID || inpaintOp.Inpaint == nil || inpaintOp.Inpaint.MaskWidth != 3 || inpaintOp.Inpaint.MaskHeight != 4 {
		t.Fatal("inpainting did not use the cropped result")
	}
	if err := deleteConversation(config.Storage, "conv_crop_chain"); err != nil {
		t.Fatal(err)
	}
	third, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: "crop", SessionConversationID: first.SessionConversationID, InputArtifactID: secondOp.ResultArtifactID,
		Crop: &CropOperationParams{Width: 2, Height: 2, SourceWidth: 3, SourceHeight: 4}})
	if err != nil {
		t.Fatalf("crop should survive deletion of the parent: %v", err)
	}
	if op := waitForEditTerminal(t, config.Storage, first.SessionConversationID, third.Operation.ID); op.Status != "completed" {
		t.Fatalf("orphan-session crop failed: %s", op.Error)
	}
}

func TestCropSubmitRefusesBusySession(t *testing.T) {
	config := editTestHome(t)
	app := editTestOfflineApp()
	source, _, _ := writeEditSessionFixture(t, config, "conv_crop_busy_parent", "conv_crop_busy")
	_, cancel := app.registerImageEditOperation("conv_crop_busy", "busy")
	defer cancel()
	defer func() { app.editOpsMu.Lock(); delete(app.editOps, "busy"); app.editOpsMu.Unlock() }()
	_, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: "crop", SessionConversationID: "conv_crop_busy", InputArtifactID: source,
		Crop: &CropOperationParams{Width: 4, Height: 4, SourceWidth: 8, SourceHeight: 8}})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("busy session must refuse a crop, got %v", err)
	}
}

func TestReleaseEditSourcePreviewCannotDeleteOtherArtifacts(t *testing.T) {
	app := editTestOfflineApp()
	dir := editSourcePreviewDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(dir, randomID("editpreview")+".png")
	if err := os.WriteFile(preview, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(preview) })
	regular := filepath.Join(t.TempDir(), "editpreview.png")
	if err := os.WriteFile(regular, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.ReleaseEditSourcePreview(artifactPrefix + regular); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(regular); err != nil {
		t.Fatal("cleanup removed an original artifact")
	}
	if err := app.ReleaseEditSourcePreview(artifactPrefix + preview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preview); !os.IsNotExist(err) {
		t.Fatal("temporary preview was not removed")
	}
	if err := app.ReleaseEditSourcePreview(artifactPrefix + preview); err != nil {
		t.Fatal("cleanup must be idempotent")
	}
}

func TestCropCancelledBeforeExecutionCreatesNoResult(t *testing.T) {
	config := editTestHome(t)
	app := editTestOfflineApp()
	source, _, _ := writeEditSessionFixture(t, config, "conv_crop_cancel_parent", "conv_crop_cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	op := EditOperation{ID: "cancelled_crop", Kind: "crop", Status: "queued", InputArtifactID: source, Crop: &CropOperationParams{Width: 2, Height: 2, SourceWidth: 8, SourceHeight: 8}}
	app.executeImageEditOperation(ctx, cancel, config, "conv_crop_cancel", "turn_000003", op, editTestFixturePNG(t, 8, 8, color.White), nil)
	state, err := app.ListEditSession("conv_crop_cancel")
	if err != nil {
		t.Fatal(err)
	}
	got := state.Operations[len(state.Operations)-1]
	if got.Status != "cancelled" || got.ResultArtifactID != "" {
		t.Fatalf("cancelled crop persisted a result: %+v", got)
	}
}
