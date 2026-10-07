package main

// The image editor's session layer: child-conversation lineage, the operation
// chain, and the bound methods the frontend editor drives. The operation
// envelope and media core live in image_edit.go; provider adapters beside
// their siblings (fal_params.go, replicate_params.go).
//
// Lineage model (the plan's decisions): opening the editor is an unsaved
// draft — the first submitted operation creates the durable child conversation
// (Kind "edit") that owns a normalized copy of the source image plus one turn
// per submitted operation. Result images are ordinary HistoryContent on the
// op turn (they must render in assets and adopt into the parent); the mask is
// an operation-payload artifact (msk_<hex>.png) referenced from the op record
// only, so asset enumeration and model context never see it. Deletion is not
// cascaded in either direction: children keep their source copies and stay
// editable when the parent goes away, and adoption copies result bytes into
// parent-owned storage so the reverse also holds.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// editConversationKind is the HistoryConversation.Kind of an edit session —
// the value loadForAppend refuses for chats, so chat append paths can never
// write into an editor conversation by accident.
const editConversationKind = "edit"

// editEventOp is the Wails event fired on every operation state transition.
const editEventOp = "atelier:edit-op"

// EditSessionMeta pins a child conversation to the image it edits. Stored on
// the child record; never rewritten after creation (the conversation's
// ProjectID/Workspace rules apply to it equally).
type EditSessionMeta struct {
	// ParentConversationID is the conversation the source image lives in. The
	// child does not depend on it after creation (it owns its source copy),
	// but it names the breadcrumb target and the adoption destination.
	ParentConversationID string `json:"parentConversationId"`
	// SourceTurnID/SourceArtifactID record where the source came from — the
	// provenance trail the UI shows and legacy lookups fall back to.
	SourceTurnID     string `json:"sourceTurnId,omitempty"`
	SourceArtifactID string `json:"sourceArtifactId,omitempty"`
	// SourceTitleSnapshot is the parent's title at session creation, so the
	// breadcrumb still reads correctly when the parent is later renamed or
	// deleted.
	SourceTitleSnapshot string `json:"sourceTitleSnapshot,omitempty"`
}

// EditSourceInfo is what the editor needs to open a source image: identity,
// a renderable URL, and display dimensions.
type EditSourceInfo struct {
	ConversationID    string `json:"conversationId"`
	ConversationTitle string `json:"conversationTitle,omitempty"`
	OriginTurnID      string `json:"originTurnId,omitempty"`
	ArtifactID        string `json:"artifactId"`
	URL               string `json:"url"`
	MimeType          string `json:"mimeType,omitempty"`
	Width             int    `json:"width,omitempty"`
	Height            int    `json:"height,omitempty"`
	SourceDigest      string `json:"sourceDigest,omitempty"`
}

// ImageEditSubmitRequest is one submitted editor operation. The first submit
// of a draft (SessionConversationID empty) creates the child conversation;
// iterations carry it. Provider/Model empty means "the effective Settings
// default"; a pair rides together.
type ImageEditSubmitRequest struct {
	Kind                  string `json:"kind,omitempty"`
	ParentConversationID  string `json:"parentConversationId"`
	SourceArtifactID      string `json:"sourceArtifactId"`
	SessionConversationID string `json:"sessionConversationId,omitempty"`
	// InputArtifactID names the canvas state this op consumes (a previous
	// result after "Use as source"). Empty = the session's latest canvas.
	InputArtifactID string `json:"inputArtifactId,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	// MaskPng is the selection mask as a PNG data URL at the source's pixel
	// dimensions — white = editable, black = preserved.
	MaskPng  string               `json:"maskPng,omitempty"`
	Provider string               `json:"provider,omitempty"`
	Model    string               `json:"model,omitempty"`
	Crop     *CropOperationParams `json:"crop,omitempty"`
	// SourceDigest is the SHA-256 digest ResolveEditSource returned for the
	// parent-side artifact. First submit checks it before copying the source so
	// a normalized preview cannot crop stale bytes after an external change.
	SourceDigest string `json:"sourceDigest,omitempty"`
}

// EditOperationState is the immediate response to a submit: the (possibly
// just-created) session, the persisted operation, and the hydrated source URL
// the live editor renders before any reload.
type EditOperationState struct {
	SessionConversationID string        `json:"sessionConversationId"`
	CreatedSession        bool          `json:"createdSession"`
	Operation             EditOperation `json:"operation"`
	SourceURL             string        `json:"sourceUrl,omitempty"`
}

// EditOperationEvent rides the atelier:edit-op event on every transition so
// an open editor (or the session's result strip elsewhere) reflects live
// state without polling.
type EditOperationEvent struct {
	SessionConversationID string        `json:"sessionConversationId"`
	Operation             EditOperation `json:"operation"`
}

// EditSessionState is the reopened editor's full context: lineage, source,
// and the operation chain with hydrated URLs.
type EditSessionState struct {
	ConversationID        string          `json:"conversationId"`
	ParentConversationID  string          `json:"parentConversationId,omitempty"`
	ParentTitle           string          `json:"parentTitle,omitempty"`
	ParentAvailable       bool            `json:"parentAvailable"`
	ParentStreaming       bool            `json:"parentStreaming"`
	Source                EditSourceInfo  `json:"source"`
	Operations            []EditOperation `json:"operations"`
	RunningOperationID    string          `json:"runningOperationId,omitempty"`
	InpaintProvider       string          `json:"inpaintProvider,omitempty"`
	InpaintFalModel       string          `json:"inpaintFalModel,omitempty"`
	InpaintReplicateModel string          `json:"inpaintReplicateModel,omitempty"`
	FalConfigured         bool            `json:"falConfigured"`
	ReplicateConfigured   bool            `json:"replicateConfigured"`
}

// ImageInpaintRequest is the canonical inpaint call the provider adapters map
// onto their model's native inputs. Source and mask ride as data URLs (or
// hosted URLs once resolved); Width/Height are the normalized source's pixel
// dimensions, sent where the model accepts an explicit output size so the
// result can composite without resizing.
type ImageInpaintRequest struct {
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	SourceImage string `json:"sourceImage"`
	MaskImage   string `json:"maskImage"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

// InpaintModelOption is one verified mask-capable model the editor and the
// Settings picker offer. Only models whose contracts were verified against
// their provider's published schema belong here — a name containing "edit"
// or "inpaint" is not evidence of mask support, and an unverified id the
// user types by hand is refused at the resolver (no mask input → hard error
// before any money moves).
type InpaintModelOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// verifiedInpaintModels is the curated catalog, one entry per contract
// verified against the provider's published schema (fal registry OpenAPI,
// Replicate model pages — see the constants' comments in fal_client.go /
// replicate_client.go for the verification record).
func verifiedInpaintModels(provider string) []InpaintModelOption {
	switch provider {
	case "replicate":
		return []InpaintModelOption{
			{ID: defaultReplicateInpaintModel, Label: "FLUX.1 Fill [pro]", Note: "Official, actively maintained. Black areas preserved, white areas filled; the mask must match the image size."},
			{ID: "black-forest-labs/flux-fill-dev", Label: "FLUX.1 Fill [dev]", Note: "Official, open weights, cheaper. Inputs snap to 32-pixel multiples and cap at 1440×1440 — other shapes are refused before the call because their output could not composite."},
		}
	default:
		return []InpaintModelOption{
			{ID: defaultFalInpaintModel, Label: "FLUX.1 Fill [pro]", Note: "Follows the source's shape; fal downscales large inputs (a 1536×1024 source came back 1440×960) and the editor scales the result back uniformly before compositing. Billed per megapixel."},
			{ID: "fal-ai/qwen-image-edit/inpaint", Label: "Qwen Image Edit (inpaint)", Note: "Takes an explicit pixel output size and a negative prompt. Its mask behavior is unverified — live runs showed edits landing OUTSIDE the selection (discarded by the composite), so prefer a Fill model for masked edits."},
			{ID: "fal-ai/flux-lora-fill", Label: "FLUX.1 Fill [dev] + LoRA", Note: "Open weights, cheaper; pastes the original back outside the mask by default."},
			{ID: "fal-ai/fast-sdxl/inpainting", Label: "Fast SDXL Inpainting", Note: "Classic SDXL inpainting; the output size is sent explicitly as pixels."},
		}
	}
}

// ListInpaintModels returns the verified inpainting catalog for one provider —
// the editor's and the Settings picker's model source.
func (a *App) ListInpaintModels(provider string) []InpaintModelOption {
	return verifiedInpaintModels(provider)
}

// editOpRun tracks one in-flight operation: the session it writes to and the
// cancel func CancelImageEdit fires. Keyed by operation ID.
type editOpRun struct {
	conversationID string
	cancel         context.CancelFunc
}

// ResolveEditSource validates an image artifact as an editable source and
// returns what the editor canvas needs to open it. conversationID is the
// conversation whose asset the user clicked — the editor seeds its draft from
// explicit identity, never from a bare URL.
func (a *App) ResolveEditSource(conversationID, artifactID string) (EditSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	artifactID = strings.TrimSpace(artifactID)
	if conversationID == "" || artifactID == "" {
		return EditSourceInfo{}, errors.New("a conversation id and artifact id are required to open the editor")
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return EditSourceInfo{}, err
	}
	content, originTurnID, ok := findImageContent(detail, artifactID)
	if !ok {
		return EditSourceInfo{}, fmt.Errorf("image %q not found in conversation %s", artifactID, conversationID)
	}
	cleanupEditSourcePreviews()
	return editSourceInfoForPreview(config, conversationID, detail.Conversation.Title, originTurnID, content)
}

// ReleaseEditSourcePreview removes a normalized temporary preview returned by
// ResolveEditSource. URLs that do not name Atelier's temp preview directory are
// ignored, so callers can pass the current canvas URL unconditionally.
func (a *App) ReleaseEditSourcePreview(previewURL string) error {
	path := strings.TrimPrefix(strings.TrimSpace(previewURL), artifactPrefix)
	dir := editSourcePreviewDir()
	if path == previewURL || path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != dir || !strings.HasPrefix(filepath.Base(clean), "editpreview") {
		return nil
	}
	if err := os.Remove(clean); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// editSourceInfoFor hydrates one image content entry into an EditSourceInfo —
// identity, artifact URL, and pixel dimensions from the magic-byte sniffer.
func editSourceInfoFor(storage ConfigStorage, conversationID, title, originTurnID string, content HistoryContent) (EditSourceInfo, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return EditSourceInfo{}, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return EditSourceInfo{}, err
	}
	if !isImageBytes(data) {
		return EditSourceInfo{}, fmt.Errorf("artifact %s is not a supported image", content.Path)
	}
	width, height, _ := editImageDimensions(data)
	mimeType := strings.TrimSpace(content.MimeType)
	if mimeType == "" {
		mimeType = mediaTypeForExtension(imageExtensionForBytes(data))
	}
	return EditSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		OriginTurnID:      originTurnID,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          mimeType,
		Width:             width,
		Height:            height,
		SourceDigest:      editSourceDigest(data),
	}, nil
}

// editSourceInfoForPreview returns the same identity as editSourceInfoFor, but
// the URL/dimensions point at the normalized upright pixels the first
// operation will copy into the edit session. That keeps crop coordinates
// aligned for EXIF-rotated and backend-normalized sources without creating a
// durable draft conversation.
func editSourceInfoForPreview(config AppConfig, conversationID, title, originTurnID string, content HistoryContent) (EditSourceInfo, error) {
	info, err := editSourceInfoFor(config.Storage, conversationID, title, originTurnID, content)
	if err != nil {
		return EditSourceInfo{}, err
	}
	absPath, err := contentArtifactPath(config.Storage, conversationID, content)
	if err != nil {
		return EditSourceInfo{}, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return EditSourceInfo{}, err
	}
	normalized, ext, err := normalizeEditSourceBytes(context.Background(), config, data, strings.ToLower(filepath.Ext(absPath)))
	if err != nil {
		return EditSourceInfo{}, err
	}
	width, height, ok := editImageDimensions(normalized)
	if !ok {
		return EditSourceInfo{}, errors.New("could not read the normalized image's dimensions")
	}
	info.Width = width
	info.Height = height
	info.MimeType = mediaTypeForExtension(ext)
	if bytes.Equal(normalized, data) {
		return info, nil
	}
	dir := editSourcePreviewDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return EditSourceInfo{}, err
	}
	filename := randomID("editpreview") + ext
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, normalized, 0o644); err != nil {
		return EditSourceInfo{}, err
	}
	info.URL = artifactPrefix + path
	return info, nil
}

func editSourceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func editSourcePreviewDir() string {
	return filepath.Join(os.TempDir(), "atelier-edit-previews")
}

func cleanupEditSourcePreviews() {
	dir := editSourcePreviewDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "editpreview") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

// findImageContent locates an image content entry by artifact ID (or relative
// path fallback) across a conversation's turns, returning the owning turn id
// for provenance. Newest entry wins, matching assetContentIndex.
func findImageContent(detail ConversationDetail, artifactID string) (HistoryContent, string, bool) {
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.Type != "image" {
				continue
			}
			if content.ArtifactID == artifactID || (content.ArtifactID == "" && content.Path == artifactID) {
				return content, detail.Turns[i].ID, true
			}
		}
	}
	return HistoryContent{}, "", false
}

// firstSessionSourceImage returns the session's own source copy — the first
// user turn's image entry — which is the anchor every reader of the child
// conversation uses for "the image being edited". Operation turns are all
// assistant-role, so the first user turn is always the source turn the
// creation path wrote.
func firstSessionSourceImage(detail ConversationDetail) (HistoryContent, string, bool) {
	for _, turn := range detail.Turns {
		if turn.Role != "user" {
			continue
		}
		for _, content := range turn.Content {
			if content.Type == "image" && content.ArtifactID != "" {
				return content, turn.ID, true
			}
		}
	}
	return HistoryContent{}, "", false
}

// SubmitImageEdit validates and persists one editor operation, then executes
// it in the background. Validation is complete before anything durable or
// billable happens (the zero-remote-call rule): source identity, mask
// decoding/dimensions/nonemptiness, provider pair, and a per-session
// one-op-at-a-time guard. The first submit of a draft creates the child
// conversation — copying a normalized source — before the provider call, so a
// remote failure leaves a retryable recorded edit rather than losing the
// user's mask and prompt.
func (a *App) SubmitImageEdit(req ImageEditSubmitRequest) (EditOperationState, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditOperationState{}, err
	}
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = editOperationKindInpaint
	}
	if kind != editOperationKindInpaint && kind != editOperationKindCrop {
		return EditOperationState{}, fmt.Errorf("unsupported edit operation kind %q", kind)
	}

	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	prompt := strings.TrimSpace(req.Prompt)
	if kind == editOperationKindInpaint {
		// Effective provider/model pair: the request's explicit pair wins; each
		// half falls back to the effective provider's configured default so a
		// half-specified pair still resolves. The recorded pair is what executes.
		switch provider {
		case "":
			provider = config.Models.InpaintProvider
		case "fal", "replicate":
		default:
			return EditOperationState{}, fmt.Errorf("unknown inpaint provider %q — choose fal.ai or Replicate", provider)
		}
		if model == "" {
			if provider == "replicate" {
				model = config.Providers.Replicate.InpaintModel
			} else {
				model = config.Providers.Fal.InpaintModel
			}
		}
		if model == "" {
			return EditOperationState{}, fmt.Errorf("no inpainting model configured for %s — pick one in Settings → Models → Inpainting", provider)
		}
		if prompt == "" {
			return EditOperationState{}, errors.New("a prompt describing the change is required for inpainting")
		}
	} else {
		provider = ""
		model = ""
	}

	parentID := strings.TrimSpace(req.ParentConversationID)
	store := newHistoryStore(config.Storage)
	nowText := time.Now().Format(time.RFC3339)
	ctx := context.Background()

	sessionID := strings.TrimSpace(req.SessionConversationID)
	createdSession := false
	var sourceNotices []string
	if sessionID == "" {
		if strings.TrimSpace(req.InputArtifactID) != "" {
			return EditOperationState{}, errors.New("an input artifact can only be selected within an existing edit session")
		}
		// First submit: the source is resolved in its OWNING conversation
		// (the parent), normalized (orientation baked, compositor-decodable),
		// and copied into a fresh child that owns it from here on.
		sourceArtifactID := strings.TrimSpace(req.SourceArtifactID)
		if parentID == "" || sourceArtifactID == "" {
			return EditOperationState{}, errors.New("the source conversation and artifact must be named")
		}
		parentDetail, err := getConversation(config.Storage, parentID)
		if err != nil {
			return EditOperationState{}, fmt.Errorf("the conversation this image belongs to is unavailable: %w", err)
		}
		sourceContent, sourceTurnID, ok := findImageContent(parentDetail, sourceArtifactID)
		if !ok {
			return EditOperationState{}, fmt.Errorf("image %q not found in conversation %s", sourceArtifactID, parentID)
		}
		sourcePath, err := contentArtifactPath(config.Storage, parentID, sourceContent)
		if err != nil {
			return EditOperationState{}, err
		}
		sourceRaw, err := os.ReadFile(sourcePath)
		if err != nil {
			return EditOperationState{}, err
		}
		if !isImageBytes(sourceRaw) {
			return EditOperationState{}, errors.New("the source artifact is not a supported image")
		}
		if digest := strings.TrimSpace(req.SourceDigest); digest != "" && digest != editSourceDigest(sourceRaw) {
			return EditOperationState{}, errors.New("the source image changed after the editor opened — reopen it and try again")
		}
		if imageExtensionForBytes(sourceRaw) == ".gif" {
			sourceNotices = append(sourceNotices, "GIF sources are edited as a still image from the first frame.")
		}
		normalized, ext, err := normalizeEditSourceBytes(ctx, config, sourceRaw, strings.ToLower(filepath.Ext(sourcePath)))
		if err != nil {
			return EditOperationState{}, err
		}
		width, height, ok := editImageDimensions(normalized)
		if !ok {
			return EditOperationState{}, errors.New("could not read the image's dimensions")
		}
		if kind == editOperationKindInpaint {
			mask, _, err := decodeMediaDataURL(req.MaskPng)
			if err != nil || len(mask) == 0 {
				return EditOperationState{}, errors.New("the selection mask is missing or unreadable — paint a selection before generating")
			}
			if _, err := validateInpaintMask(mask, width, height); err != nil {
				return EditOperationState{}, err
			}
		}
		if kind == editOperationKindCrop {
			crop := req.Crop
			if crop == nil {
				return EditOperationState{}, errors.New("crop parameters are required")
			}
			if err := validateCropParams(crop, width, height); err != nil {
				return EditOperationState{}, err
			}
		}
		sessionID, _, err = createEditSession(store, config, parentDetail, sourceContent, sourceTurnID, normalized, ext, nowText)
		if err != nil {
			return EditOperationState{}, err
		}
		createdSession = true
	}

	loaded, err := loadForEditAppend(config.Storage, sessionID)
	if err != nil {
		return EditOperationState{}, err
	}
	// Lineage authority is the record's own meta: the parent id the request
	// names (if any) must agree with it, and iterations never re-resolve the
	// parent-side source — the session owns its copy precisely so the parent
	// can go away without breaking the chain.
	meta := loaded.Conversation.EditSession
	if meta == nil {
		return EditOperationState{}, fmt.Errorf("edit session %s carries no lineage", sessionID)
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

	// Canvas state: the named input artifact, else the session's latest
	// completed result, else the session's own source copy — the chain
	// advances op over op.
	inputArtifactID := strings.TrimSpace(req.InputArtifactID)
	if inputArtifactID == "" {
		inputArtifactID = latestCanvasArtifactID(sessionDetail)
	}
	inputContent, _, ok := findImageContent(sessionDetail, inputArtifactID)
	if !ok {
		return EditOperationState{}, fmt.Errorf("the image this edit builds on (%s) is not part of the session — reopen the session and pick a result it owns", inputArtifactID)
	}
	inputPath, err := contentArtifactPath(config.Storage, sessionID, inputContent)
	if err != nil {
		return EditOperationState{}, err
	}
	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		return EditOperationState{}, err
	}
	inputWidth, inputHeight, ok := editImageDimensions(inputData)
	if !ok {
		return EditOperationState{}, errors.New("could not read the image's dimensions")
	}

	var maskData []byte
	op := EditOperation{
		ID:              randomID("editop"),
		Kind:            kind,
		Status:          editOperationStatusQueued,
		CreatedAt:       nowText,
		InputArtifactID: inputArtifactID,
		Notices:         sourceNotices,
	}
	if kind == editOperationKindInpaint {
		// Mask validation — the last zero-cost gate before anything is
		// persisted or billed.
		maskData, _, err = decodeMediaDataURL(req.MaskPng)
		if err != nil || len(maskData) == 0 {
			return EditOperationState{}, errors.New("the selection mask is missing or unreadable — paint a selection before generating")
		}
		if _, err := validateInpaintMask(maskData, inputWidth, inputHeight); err != nil {
			return EditOperationState{}, err
		}
		op.Provider = provider
		op.Model = model
		op.Inpaint = &InpaintOperationParams{
			Prompt:    prompt,
			MaskWidth: inputWidth, MaskHeight: inputHeight,
		}
		maskID := randomID("msk")
		if err := os.WriteFile(filepath.Join(loaded.ArtifactsDir, maskID+".png"), maskData, 0o644); err != nil {
			return EditOperationState{}, err
		}
		op.Inpaint.MaskArtifactID = maskID
		op.Inpaint.MaskPath = filepath.ToSlash(filepath.Join("artifacts", maskID+".png"))
	} else {
		crop := req.Crop
		if crop == nil {
			return EditOperationState{}, errors.New("crop parameters are required")
		}
		if err := validateCropParams(crop, inputWidth, inputHeight); err != nil {
			return EditOperationState{}, err
		}
		copied := *crop
		op.Backend = "local"
		op.Crop = &copied
	}

	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: sessionID,
		CreatedAt:      nowText,
		Kind:           editConversationKind,
		Role:           "assistant",
		Provider:       provider,
		Model:          model,
		Request:        map[string]any{"operation": op},
	}
	// Reserve the run before publishing its queued turn. Readers must never
	// mistake a just-submitted operation for an interrupted run.
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

	// Execute in the background; SubmitImageEdit returns the queued state
	// immediately and every transition lands via the atelier:edit-op event.
	launched = true
	go a.executeImageEditOperation(opCtx, opCancel, config, sessionID, turn.ID, op, inputData, maskData)

	sourceURL := ""
	if createdSession {
		// The child's own source copy is its first user turn's image entry —
		// never the parent artifact id the lineage meta records.
		for _, turn := range sessionDetail.Turns {
			if turn.Role != "user" {
				continue
			}
			for _, content := range turn.Content {
				if content.Type != "image" {
					continue
				}
				if abs, perr := contentArtifactPath(config.Storage, sessionID, content); perr == nil {
					sourceURL = artifactPrefix + abs
				}
			}
			break
		}
	}
	return EditOperationState{
		SessionConversationID: sessionID,
		CreatedSession:        createdSession,
		Operation:             op,
		SourceURL:             sourceURL,
	}, nil
}

// registerImageEditOperation reserves the run before its queued turn becomes
// visible to readers; CancelImageEdit can act as soon as submission returns.
func (a *App) registerImageEditOperation(sessionID, operationID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	a.editOpsMu.Lock()
	a.editOps[operationID] = &editOpRun{conversationID: sessionID, cancel: cancel}
	a.editOpsMu.Unlock()
	return ctx, cancel
}

// executeImageEditOperation dispatches one operation and persists each state
// transition. Only inpainting uses the provider and outside-mask composite.
func (a *App) executeImageEditOperation(ctx context.Context, cancel context.CancelFunc, config AppConfig, sessionID, turnID string, op EditOperation, inputData, maskData []byte) {
	defer func() {
		a.editOpsMu.Lock()
		delete(a.editOps, op.ID)
		a.editOpsMu.Unlock()
		cancel()
	}()

	store := newHistoryStore(config.Storage)
	persistAndEmit := func(op EditOperation) error {
		loaded, err := loadForEditAppend(config.Storage, sessionID)
		if err != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, op)
			return err
		}
		loaded.Conversation.UpdatedAt = time.Now().Format(time.RFC3339)
		if op.Status == editOperationStatusCompleted {
			loaded.Conversation.Stats.ArtifactCount++
		}
		if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, op)
			return err
		}
		if err := store.writeTurn(loaded.TurnsDir, HistoryTurn{
			SchemaVersion:  1,
			ID:             turnID,
			ConversationID: sessionID,
			CreatedAt:      op.CreatedAt,
			Kind:           editConversationKind,
			Role:           "assistant",
			Provider:       op.Provider,
			Model:          op.Model,
			Request:        map[string]any{"operation": op},
			Content:        editOperationContents(op),
		}); err != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the edit state could not be saved: %v", err)
			a.emitEditOperation(sessionID, op)
			return err
		}
		// ResultPath is relative to the CONVERSATION directory (the
		// HistoryContent.Path convention); joining it against the artifacts
		// directory would double the segment and 404 the preview.
		if op.ResultPath != "" {
			op.ResultURL = artifactPrefix + filepath.Join(filepath.Dir(loaded.Path), filepath.FromSlash(op.ResultPath))
		}
		a.emitEditOperation(sessionID, op)
		return nil
	}

	op.Status = editOperationStatusRunning
	if err := persistAndEmit(op); err != nil {
		return
	}

	var (
		resultData  []byte
		costMicros  int64
		costUnknown bool
		notices     = append([]string(nil), op.Notices...)
		execErr     error
	)
	switch op.Kind {
	case editOperationKindInpaint:
		var providerNotices []string
		resultData, costMicros, costUnknown, providerNotices, execErr = a.executeInpaintProvider(ctx, config, op, inputData, maskData)
		notices = append(notices, providerNotices...)
	case editOperationKindCrop:
		if op.Crop == nil {
			execErr = errors.New("operation carries no crop payload")
		} else {
			if imageExtensionForBytes(inputData) == ".gif" {
				notices = append(notices, "Animated GIF sources are cropped as a still image from the decoded first frame.")
			}
			resultData, execErr = executeCropOperation(inputData, *op.Crop)
		}
	default:
		execErr = fmt.Errorf("unsupported edit operation kind %q", op.Kind)
	}
	if execErr == nil && ctx.Err() != nil {
		execErr = ctx.Err()
	}
	op.CompletedAt = time.Now().Format(time.RFC3339)
	op.Notices = notices
	op.CostMicros = costMicros
	op.CostUnknown = costUnknown
	switch {
	case execErr != nil:
		op.Status = editOperationStatusFailed
		if ctx.Err() != nil {
			op.Status = editOperationStatusCancelled
		}
		op.Error = execErr.Error()
	case execErr == nil && op.Kind == editOperationKindInpaint:
		// Strict outside-mask preservation: the composite runs before anything
		// is persisted as a result. A differently-shaped output fails the
		// operation (inputs retained for retry); a UNIFORMLY rescaled output
		// (fal's fill pro downscales large inputs, schema be damned) is
		// rescaled back with a notice — safe because preserved pixels come
		// from the source bit-exactly either way.
		resultData, scaleNotice, scaled := alignInpaintOutputScale(inputData, resultData)
		if scaled {
			op.Notices = append(op.Notices, scaleNotice)
		}
		composited, err := compositeInpaintResult(inputData, resultData, maskData)
		if err != nil {
			op.Status = editOperationStatusFailed
			op.Error = err.Error()
			break
		}
		// The composite is strict by construction; what it cannot say is
		// whether the model actually changed anything. Compare the model's
		// raw output against the source through the mask and surface the two
		// silent-failure shapes as notices (change-nothing inside =
		// capability/prompt; big edits outside = inverted-polarity evidence).
		op.Notices = append(op.Notices, inpaintChangeNotices(inputData, resultData, maskData)...)
		loaded, loadErr := loadForEditAppend(config.Storage, sessionID)
		if loadErr != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the edit ran but its result could not be saved: %v", loadErr)
			break
		}
		contents, writeErr := writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{
			Width:  op.Inpaint.MaskWidth,
			Height: op.Inpaint.MaskHeight,
		}, []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(composited)})
		if writeErr != nil || len(contents) == 0 {
			op.Status = editOperationStatusFailed
			if writeErr == nil {
				writeErr = errors.New("the composite produced no image")
			}
			op.Error = fmt.Sprintf("the edit ran but its result could not be saved: %v", writeErr)
			break
		}
		result := contents[0]
		op.Status = editOperationStatusCompleted
		op.ResultArtifactID = result.ArtifactID
		op.ResultPath = result.Path
		op.ResultMimeType = result.MimeType
		op.ResultWidth = result.Width
		op.ResultHeight = result.Height
	case execErr == nil && op.Kind == editOperationKindCrop:
		loaded, loadErr := loadForEditAppend(config.Storage, sessionID)
		if loadErr != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the crop ran but its result could not be saved: %v", loadErr)
			break
		}
		contents, writeErr := writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{
			Width:  op.Crop.Width,
			Height: op.Crop.Height,
		}, []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(resultData)})
		if writeErr != nil || len(contents) == 0 {
			op.Status = editOperationStatusFailed
			if writeErr == nil {
				writeErr = errors.New("the crop produced no image")
			}
			op.Error = fmt.Sprintf("the crop ran but its result could not be saved: %v", writeErr)
			break
		}
		result := contents[0]
		op.Status = editOperationStatusCompleted
		op.ResultArtifactID = result.ArtifactID
		op.ResultPath = result.Path
		op.ResultMimeType = result.MimeType
		op.ResultWidth = result.Width
		op.ResultHeight = result.Height
	}
	_ = persistAndEmit(op)
}

// editOperationContents builds the turn content for an operation turn: the
// result image entry on success, nothing otherwise (the op record carries the
// state). The entry mirrors writeChatImageArtifacts' shape so the child
// conversation's asset fold treats it like any generated image.
func editOperationContents(op EditOperation) []HistoryContent {
	if op.Status != editOperationStatusCompleted || op.ResultArtifactID == "" {
		return []HistoryContent{}
	}
	return []HistoryContent{{
		Type:       "image",
		ArtifactID: op.ResultArtifactID,
		Path:       op.ResultPath,
		MimeType:   op.ResultMimeType,
		Width:      op.ResultWidth,
		Height:     op.ResultHeight,
	}}
}

// executeInpaintProvider dispatches the canonical inpaint request to the
// recorded provider and returns the model's raw output bytes plus cost
// attribution. Both verified defaults speak Atelier's canonical mask polarity
// (white = editable), so no inversion rides this path — see
// inpaintMaskPolarity in fal_params.go.
func (a *App) executeInpaintProvider(ctx context.Context, config AppConfig, op EditOperation, inputData, maskData []byte) (resultData []byte, costMicros int64, costUnknown bool, notices []string, err error) {
	params, ok := op.operationPayload()
	if !ok {
		return nil, 0, false, nil, fmt.Errorf("operation %s carries no %s payload", op.ID, op.Kind)
	}
	req := ImageInpaintRequest{
		Model:       op.Model,
		Prompt:      params.Prompt,
		SourceImage: imageDataURLForBytes(inputData),
		MaskImage:   "data:image/png;base64," + base64.StdEncoding.EncodeToString(maskData),
		Width:       params.MaskWidth,
		Height:      params.MaskHeight,
	}

	if op.Provider == "replicate" {
		apiKey, err := loadReplicateAPIKey()
		if err != nil {
			return nil, 0, false, nil, err
		}
		if strings.TrimSpace(apiKey) == "" {
			return nil, 0, false, nil, errReplicateKeyNotConfigured
		}
		if err := replicateInpaintSizeConstraint(op.Model, req.Width, req.Height); err != nil {
			return nil, 0, false, nil, err
		}
		client := newReplicateClient(a.client, apiKey)
		if resolved, rerr := client.ResolveMediaURL(ctx, req.SourceImage, "image/jpeg", "edit-source.jpg"); rerr == nil && resolved != "" {
			req.SourceImage = resolved
		}
		if resolved, rerr := client.ResolveMediaURL(ctx, req.MaskImage, "image/png", "edit-mask.png"); rerr == nil && resolved != "" {
			req.MaskImage = resolved
		}
		schema := newReplicateSchemaCache(a.client, config.Storage.Root).Get(ctx, op.Model)
		input, resolveNotices, err := resolveReplicateInpaintInput(schema, req)
		if err != nil {
			return nil, 0, false, nil, err
		}
		notices = resolveNotices
		job := &mediaJob{}
		client.jobSink = job
		resp, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
			func(jctx context.Context) (ollamaGenerateResponse, error) {
				return client.GenerateImage(jctx, op.Model, input)
			},
			client.CancelMediaJob,
			func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
				return client.RecoverImageJob(jctx, j, op.Model)
			},
		)
		if genErr != nil {
			return nil, 0, false, notices, genErr
		}
		if recovered {
			notices = append(notices, mediaGenerationRecoveredNotice)
		}
		// Replicate reports no cost — the unknown-cost flag renders "?" rather
		// than a misleading $0.00.
		data, derr := firstInpaintImage(resp)
		if derr != nil {
			return nil, 0, false, notices, derr
		}
		return data, 0, true, notices, nil
	}

	// fal (the default backend).
	apiKey, err := loadFalAPIKey()
	if err != nil {
		return nil, 0, false, nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, 0, false, nil, errFalKeyNotConfigured
	}
	client := newFalClient(a.client, apiKey)
	if resolved, rerr := client.resolveMediaURL(ctx, req.SourceImage, "image/jpeg", "edit-source.jpg"); rerr == nil && resolved != "" {
		req.SourceImage = resolved
	}
	if resolved, rerr := client.resolveMediaURL(ctx, req.MaskImage, "image/png", "edit-mask.png"); rerr == nil && resolved != "" {
		req.MaskImage = resolved
	}
	schema := newFalSchemaCache(a.client, config.Storage.Root).Get(ctx, op.Model)
	body, resolveNotices, err := resolveInpaintBody(schema, req, loadFalOverrides(config.Storage.Root))
	if err != nil {
		return nil, 0, false, nil, err
	}
	notices = resolveNotices
	job := &mediaJob{}
	client.jobSink = job
	resp, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
		func(jctx context.Context) (ollamaGenerateResponse, error) {
			r, _, err := client.GenerateImage(jctx, op.Model, body)
			return r, err
		},
		client.CancelMediaJob,
		func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
			return client.RecoverImageJob(jctx, j, op.Model)
		},
	)
	if genErr != nil {
		return nil, 0, false, notices, genErr
	}
	if recovered {
		notices = append(notices, mediaGenerationRecoveredNotice)
	}
	data, derr := firstInpaintImage(resp)
	if derr != nil {
		return nil, 0, false, notices, derr
	}
	imageCount := len(resp.Images)
	if imageCount == 0 && resp.Image != "" {
		imageCount = 1
	}
	cost := a.estimateFalGenerationCost(context.WithoutCancel(ctx), config, op.Model, falBillingHints{
		Images:     imageCount,
		Requests:   1,
		Megapixels: imageResultMegapixels(resp.Images, resp.Image),
	})
	return data, cost, false, notices, nil
}

// imageDataURLForBytes wraps raw image bytes in a data URL labeled by the
// sniffed container — never by an assumed one (a mislabeled MIME rides the
// header into provider uploads).
func imageDataURLForBytes(data []byte) string {
	mediaType := mediaTypeForExtension(imageExtensionForBytes(data))
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// firstInpaintImage extracts the first image the provider returned as raw
// bytes.
func firstInpaintImage(resp ollamaGenerateResponse) ([]byte, error) {
	payload := resp.Image
	if payload == "" && len(resp.Images) > 0 {
		payload = resp.Images[0]
	}
	if payload == "" {
		return nil, errors.New("the provider returned no image")
	}
	data, _, err := decodeMediaDataURL(payload)
	if err != nil {
		return nil, fmt.Errorf("could not decode the generated image: %w", err)
	}
	return data, nil
}

// CancelImageEdit cancels one running operation. The provider-side job is
// stopped by runMediaGeneration's abandonment path (the cancel context fires
// it), the operation records its terminal state, and the event lands as
// usual.
func (a *App) CancelImageEdit(sessionConversationID, operationID string) error {
	sessionConversationID = strings.TrimSpace(sessionConversationID)
	operationID = strings.TrimSpace(operationID)
	if sessionConversationID == "" || operationID == "" {
		return errors.New("a session and operation id are required")
	}
	a.editOpsMu.Lock()
	run, ok := a.editOps[operationID]
	a.editOpsMu.Unlock()
	if !ok || run.conversationID != sessionConversationID {
		return errors.New("that operation is not running")
	}
	run.cancel()
	return nil
}

// editOpRunning reports whether an operation is in flight for the session —
// the one-op-at-a-time guard.
func (a *App) editOpRunning(sessionID string) bool {
	a.editOpsMu.Lock()
	defer a.editOpsMu.Unlock()
	for _, run := range a.editOps {
		if run.conversationID == sessionID {
			return true
		}
	}
	return false
}

// emitEditOperation fires the live-state event when a UI is attached. URL
// hydration happens at the persist site, where the artifacts dir is known.
func (a *App) emitEditOperation(sessionID string, op EditOperation) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, editEventOp, EditOperationEvent{SessionConversationID: sessionID, Operation: op})
}

// ListEditSession loads everything the reopened editor renders: lineage,
// source, and the operation chain with hydrated URLs. A deleted parent does
// not fail the load — the child stays editable and the breadcrumb reports
// the original as unavailable (the plan's broken-lineage decision).
func (a *App) ListEditSession(sessionConversationID string) (EditSessionState, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return EditSessionState{}, err
	}
	sessionID := strings.TrimSpace(sessionConversationID)
	if sessionID == "" {
		return EditSessionState{}, errors.New("a session conversation id is required")
	}
	detail, err := getConversation(config.Storage, sessionID)
	if err != nil {
		return EditSessionState{}, err
	}
	conversation := detail.Conversation
	if conversation.Kind != editConversationKind {
		return EditSessionState{}, fmt.Errorf("conversation %s is not an edit session", sessionID)
	}
	meta := conversation.EditSession
	if meta == nil {
		return EditSessionState{}, fmt.Errorf("edit session %s carries no lineage", sessionID)
	}

	state := EditSessionState{
		ConversationID:        sessionID,
		ParentConversationID:  meta.ParentConversationID,
		ParentTitle:           meta.SourceTitleSnapshot,
		InpaintProvider:       config.Models.InpaintProvider,
		InpaintFalModel:       config.Providers.Fal.InpaintModel,
		InpaintReplicateModel: config.Providers.Replicate.InpaintModel,
	}
	if falKey, err := loadFalAPIKey(); err == nil && strings.TrimSpace(falKey) != "" {
		state.FalConfigured = true
	}
	if repKey, err := loadReplicateAPIKey(); err == nil && strings.TrimSpace(repKey) != "" {
		state.ReplicateConfigured = true
	}
	// Parent breadcrumb: a record that loads and is not deleted means the
	// original is reachable; its live title wins over the snapshot.
	if parent, err := getConversation(config.Storage, meta.ParentConversationID); err == nil {
		state.ParentAvailable = true
		state.ParentTitle = parent.Conversation.Title
		state.ParentStreaming = a.conversationStreaming(meta.ParentConversationID)
	}

	// The session's own source copy is structural: the first user turn's
	// image entry. The meta's SourceArtifactID is PARENT-side provenance (the
	// artifact the image came from), not the copy's id — the copy was written
	// under a fresh one.
	if content, turnID, ok := firstSessionSourceImage(detail); ok {
		if info, err := editSourceInfoFor(config.Storage, sessionID, state.ParentTitle, turnID, content); err == nil {
			info.ConversationID = sessionID
			state.Source = info
		}
	}

	absConversationDir := ""
	if conversationPath, err := findConversationPath(config.Storage, sessionID); err == nil {
		absConversationDir, _ = filepath.Abs(filepath.Dir(conversationPath))
	}
	operations := []EditOperation{}
	for _, turn := range detail.Turns {
		op, ok := editOperationFromTurn(turn)
		if !ok {
			continue
		}
		if absConversationDir != "" {
			hydrateEditOperationURLs(&op, absConversationDir)
		}
		if op.Status == editOperationStatusQueued || op.Status == editOperationStatusRunning {
			a.editOpsMu.Lock()
			_, live := a.editOps[op.ID]
			a.editOpsMu.Unlock()
			if !live {
				// A transient status without a live run is a restart orphan;
				// report it failed (the startup sweep persists that).
				op.Status = editOperationStatusFailed
				op.Error = "interrupted — the app restarted while this edit was running"
			} else {
				state.RunningOperationID = op.ID
			}
		}
		operations = append(operations, op)
	}
	state.Operations = operations
	return state, nil
}

// AddEditResultToConversation copies one completed operation's result into the
// parent conversation's own storage and appends a result entry citing the
// edit session. Idempotent per operation: the adoption mark on the op record
// is written before the parent append, and a repeated call is refused instead
// of duplicating the parent entry. Refused while the parent streams (the same
// guard the override mutator uses) and when the parent is gone.
func (a *App) AddEditResultToConversation(sessionConversationID, operationID string) (ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	sessionID := strings.TrimSpace(sessionConversationID)
	operationID = strings.TrimSpace(operationID)
	if sessionID == "" || operationID == "" {
		return ConversationSummary{}, errors.New("a session and operation id are required")
	}
	detail, err := getConversation(config.Storage, sessionID)
	if err != nil {
		return ConversationSummary{}, err
	}
	if detail.Conversation.Kind != editConversationKind || detail.Conversation.EditSession == nil {
		return ConversationSummary{}, fmt.Errorf("conversation %s is not an edit session", sessionID)
	}
	meta := detail.Conversation.EditSession

	var op EditOperation
	found := false
	for _, turn := range detail.Turns {
		if candidate, ok := editOperationFromTurn(turn); ok && candidate.ID == operationID {
			op = candidate
			found = true
			break
		}
	}
	if !found {
		return ConversationSummary{}, fmt.Errorf("operation %s not found in session %s", operationID, sessionID)
	}
	if op.Status != editOperationStatusCompleted || op.ResultArtifactID == "" {
		return ConversationSummary{}, errors.New("only a completed result can be added to the original conversation")
	}
	if op.AdoptedAt != "" {
		return ConversationSummary{}, errors.New("this result has already been added to the original conversation")
	}

	parentID := meta.ParentConversationID
	if a.conversationStreaming(parentID) {
		return ConversationSummary{}, errors.New("the original conversation is still running — wait for it to finish before adding the result")
	}
	if _, err := getConversation(config.Storage, parentID); err != nil {
		return ConversationSummary{}, fmt.Errorf("the original conversation is unavailable: %w", err)
	}

	// Read the session-owned result bytes.
	resultContent, _, ok := findImageContent(detail, op.ResultArtifactID)
	if !ok {
		return ConversationSummary{}, errors.New("the result artifact is missing from the session")
	}
	resultURL, err := readArtifactAsDataURL(config.Storage, sessionID, resultContent)
	if err != nil {
		return ConversationSummary{}, fmt.Errorf("could not read the result: %w", err)
	}

	// Mark the op adopted FIRST so a crash between the two writes can only
	// lose the adoption (retryable), never duplicate the parent entry. On a
	// parent-write failure the mark is reverted best-effort.
	if err := markEditOperationAdopted(config.Storage, sessionID, operationID); err != nil {
		return ConversationSummary{}, err
	}
	summary, adoptErr := appendEditAdoptionTurn(config, parentID, meta, op, resultURL)
	if adoptErr != nil {
		_ = clearEditOperationAdopted(config.Storage, sessionID, operationID)
		return ConversationSummary{}, adoptErr
	}
	return summary, nil
}

// appendEditAdoptionTurn writes the parent-owned result: a chat-kind
// assistant turn whose content cites the edit session and carries the copied
// image bytes (img_<hex> in the PARENT's artifacts dir — the child keeps its
// own). No new generation, no charge; the cost stays on the child's
// operation record. Deliberately not built on appendChatAssistantTurnWithImages:
// its providerResponse.tool metadata would render a bogus "image generation"
// row in the parent's media ledger for a copy that generated nothing.
func appendEditAdoptionTurn(config AppConfig, parentID string, meta *EditSessionMeta, op EditOperation, resultDataURL string) (ConversationSummary, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(parentID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return ConversationSummary{}, err
	}
	nowText := time.Now().Format(time.RFC3339)
	contents, err := writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{
		Width:  op.ResultWidth,
		Height: op.ResultHeight,
	}, []string{resultDataURL})
	if err != nil || len(contents) == 0 {
		if err == nil {
			err = errors.New("the result could not be copied")
		}
		return ConversationSummary{}, err
	}
	text := fmt.Sprintf("Added from the edit session of this conversation (%s).", editOperationAdoptionSummary(op))
	turnContents := append([]HistoryContent{{Type: "text", Text: text}}, contents...)
	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: parentID,
		CreatedAt:      nowText,
		Kind:           "chat",
		Role:           "assistant",
		Content:        turnContents,
		ProviderResponse: map[string]any{
			"editAdoption": map[string]any{
				"sessionId":   loaded.Conversation.ID,
				"sessionKind": editConversationKind,
				"operationId": op.ID,
				"kind":        op.Kind,
				"provider":    op.Provider,
				"model":       op.Model,
				"backend":     op.Backend,
			},
		},
	}
	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(contents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return ConversationSummary{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, turn); err != nil {
		return ConversationSummary{}, err
	}
	return conversationSummaryFrom(loaded.Conversation), nil
}

// truncateEditAdoptionPrompt keeps the adoption note readable for long
// prompts (rune-safe: cut at 80 runes, not bytes).
func truncateEditAdoptionPrompt(prompt string) string {
	runes := []rune(strings.TrimSpace(prompt))
	if len(runes) <= 80 {
		return string(runes)
	}
	return string(runes[:77]) + "…"
}

func editOperationAdoptionSummary(op EditOperation) string {
	switch op.Kind {
	case editOperationKindCrop:
		if op.Crop == nil {
			return "crop"
		}
		ratio := strings.TrimSpace(op.Crop.AspectRatio)
		if ratio == "" {
			ratio = "free"
		}
		return fmt.Sprintf("crop: %s, %d × %d", ratio, op.Crop.Width, op.Crop.Height)
	case editOperationKindInpaint:
		if op.Inpaint == nil {
			return "inpaint"
		}
		return fmt.Sprintf("inpaint: %s", truncateEditAdoptionPrompt(op.Inpaint.Prompt))
	default:
		return op.Kind
	}
}

// markEditOperationAdopted / clearEditOperationAdopted rewrite the op record's
// adoption mark in place (turn file only — the conversation record needs no
// stat change).
func markEditOperationAdopted(storage ConfigStorage, sessionID, operationID string) error {
	return mutateEditOperation(storage, sessionID, operationID, func(op *EditOperation) {
		op.AdoptedAt = time.Now().Format(time.RFC3339)
	})
}

func clearEditOperationAdopted(storage ConfigStorage, sessionID, operationID string) error {
	return mutateEditOperation(storage, sessionID, operationID, func(op *EditOperation) {
		op.AdoptedAt = ""
	})
}

// mutateEditOperation loads the session's turns, applies mut to the named
// operation, and rewrites its turn file.
func mutateEditOperation(storage ConfigStorage, sessionID, operationID string, mut func(*EditOperation)) error {
	detail, err := getConversation(storage, sessionID)
	if err != nil {
		return err
	}
	conversationPath, err := findConversationPath(storage, sessionID)
	if err != nil {
		return err
	}
	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	for _, turn := range detail.Turns {
		op, ok := editOperationFromTurn(turn)
		if !ok || op.ID != operationID {
			continue
		}
		mut(&op)
		turn.Request = map[string]any{"operation": op}
		turn.Content = editOperationContents(op)
		return writeJSONFile(filepath.Join(turnsDir, turn.ID+".json"), turn)
	}
	return fmt.Errorf("operation %s not found in session %s", operationID, sessionID)
}

// createEditSession writes the child conversation: record with lineage, a
// source turn carrying the normalized copy, and nothing else. Returns the
// session id and the source copy's hydrated info.
func createEditSession(store HistoryStore, config AppConfig, parentDetail ConversationDetail, sourceContent HistoryContent, sourceTurnID string, normalizedData []byte, ext, nowText string) (string, EditSourceInfo, error) {
	parent := parentDetail.Conversation
	workspace, err := store.newWorkspace(time.Now())
	if err != nil {
		return "", EditSourceInfo{}, err
	}
	if err := os.MkdirAll(workspace.ArtifactsDir, 0o755); err != nil {
		return "", EditSourceInfo{}, err
	}
	artifactID := randomID("img")
	filename := artifactID + ext
	if err := os.WriteFile(filepath.Join(workspace.ArtifactsDir, filename), normalizedData, 0o644); err != nil {
		return "", EditSourceInfo{}, err
	}
	sourceEntry := HistoryContent{
		Type:       "image",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   mediaTypeForExtension(ext),
	}
	width, height, _ := editImageDimensions(normalizedData)
	sourceEntry.Width = width
	sourceEntry.Height = height

	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          editConversationKind,
		Title:         fmt.Sprintf("Edit · %s", parent.Title),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider:      parent.Provider,
		Defaults:      parent.Defaults,
		Stats: HistoryConversationStats{
			TurnCount:     1,
			ArtifactCount: 1,
		},
		Workspace: parent.Workspace,
		ProjectID: parent.ProjectID,
		// The differential override rides along: the session inherits exactly
		// what the parent had pinned at creation, and its inpaint fields
		// resolve against the live Settings defaults otherwise.
		ModelOverrides: parent.ModelOverrides,
		EditSession: &EditSessionMeta{
			ParentConversationID: parent.ID,
			SourceTurnID:         sourceTurnID,
			SourceArtifactID:     sourceContent.ArtifactID,
			SourceTitleSnapshot:  parent.Title,
		},
	}
	sourceTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             "turn_000001",
		ConversationID: workspace.ID,
		CreatedAt:      nowText,
		Kind:           editConversationKind,
		Role:           "user",
		Content:        []HistoryContent{sourceEntry},
	}
	if err := store.writeSnapshot(workspace, conversation, sourceTurn); err != nil {
		return "", EditSourceInfo{}, err
	}
	info := EditSourceInfo{
		ConversationID:    workspace.ID,
		ConversationTitle: conversation.Title,
		OriginTurnID:      sourceTurn.ID,
		ArtifactID:        artifactID,
		URL:               artifactPrefix + filepath.Join(workspace.ArtifactsDir, filename),
		MimeType:          sourceEntry.MimeType,
		Width:             width,
		Height:            height,
	}
	return workspace.ID, info, nil
}

// latestCanvasArtifactID walks the session's turns newest-first for the
// newest completed result; the fallback is the session's OWN source copy —
// the first user turn's image entry. It is never the parent's artifact id
// the lineage meta records: the child owns a renamed copy.
func latestCanvasArtifactID(detail ConversationDetail) string {
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		if op, ok := editOperationFromTurn(detail.Turns[i]); ok && op.Status == editOperationStatusCompleted && op.ResultArtifactID != "" {
			return op.ResultArtifactID
		}
	}
	for _, turn := range detail.Turns {
		if turn.Role != "user" {
			continue
		}
		for _, content := range turn.Content {
			if content.Type == "image" && content.ArtifactID != "" {
				return content.ArtifactID
			}
		}
	}
	return ""
}

// loadedEditConversation is loadForAppend's shape for edit sessions — the
// chat loader refuses non-chat kinds, so the editor keeps its own.
type loadedEditConversation struct {
	Path           string
	TurnsDir       string
	ArtifactsDir   string
	Conversation   HistoryConversation
	NextTurnNumber int
}

// loadForEditAppend loads one edit-session record for mutation: kind-checked,
// deletion-checked, with the next turn number counted.
func loadForEditAppend(storage ConfigStorage, sessionID string) (loadedEditConversation, error) {
	conversationPath, err := findConversationPath(storage, sessionID)
	if err != nil {
		return loadedEditConversation{}, err
	}
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return loadedEditConversation{}, err
	}
	if conversation.DeletedAt != "" {
		return loadedEditConversation{}, fmt.Errorf("conversation %s is deleted", sessionID)
	}
	if conversation.Kind != editConversationKind {
		return loadedEditConversation{}, fmt.Errorf("conversation %s is not an edit session", sessionID)
	}
	conversationDir := filepath.Dir(conversationPath)
	turnsDir := filepath.Join(conversationDir, "turns")
	return loadedEditConversation{
		Path:           conversationPath,
		TurnsDir:       turnsDir,
		ArtifactsDir:   filepath.Join(conversationDir, "artifacts"),
		Conversation:   conversation,
		NextTurnNumber: countTurnFiles(turnsDir) + 1,
	}, nil
}

// editOperationFromTurn extracts the op record from a turn's Request map
// (JSON round-trip — the map is what persistence stores).
func editOperationFromTurn(turn HistoryTurn) (EditOperation, bool) {
	if turn.Kind != editConversationKind || turn.Request == nil {
		return EditOperation{}, false
	}
	raw, err := json.Marshal(turn.Request["operation"])
	if err != nil || len(raw) == 0 {
		return EditOperation{}, false
	}
	var op EditOperation
	if err := json.Unmarshal(raw, &op); err != nil || op.ID == "" {
		return EditOperation{}, false
	}
	return op, true
}

// hydrateEditOperationURLs fills the response-only URL fields from the
// session's conversation directory — ResultPath ("artifacts/<file>") is
// relative to it, matching the HistoryContent.Path convention.
func hydrateEditOperationURLs(op *EditOperation, absConversationDir string) {
	if op.ResultPath != "" {
		op.ResultURL = artifactPrefix + filepath.Join(absConversationDir, filepath.FromSlash(op.ResultPath))
	}
}

// sweepInterruptedEditOps downgrades queued/running operations left by a
// previous process to failed, so a reopened session never shows a phantom
// in-flight edit. Startup-only; a missing history tree is a no-op.
func sweepInterruptedEditOps(storage ConfigStorage) {
	_ = forEachConversationRecord(storage, func(path string, conversation HistoryConversation) error {
		if conversation.Kind != editConversationKind || conversation.DeletedAt != "" {
			return nil
		}
		turnsDir := filepath.Join(filepath.Dir(path), "turns")
		entries, err := os.ReadDir(turnsDir)
		if err != nil {
			return nil
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			var turn HistoryTurn
			if err := readJSONFile(filepath.Join(turnsDir, entry.Name()), &turn); err != nil {
				continue
			}
			op, ok := editOperationFromTurn(turn)
			if !ok || (op.Status != editOperationStatusQueued && op.Status != editOperationStatusRunning) {
				continue
			}
			op.Status = editOperationStatusFailed
			op.Error = "interrupted — the app restarted while this edit was running"
			op.CompletedAt = time.Now().Format(time.RFC3339)
			turn.Request = map[string]any{"operation": op}
			_ = writeJSONFile(filepath.Join(turnsDir, turn.ID+".json"), turn)
		}
		return nil
	})
}

// inpaintChangeNotices compares the model's RAW output against the source
// through the mask and returns diagnostic notices for the two silent-failure
// shapes a "completed" inpaint can have:
//
//   - the selected region came back essentially unchanged (mean per-channel
//     difference under noise level) — a capability/prompt problem, not a
//     pipeline one: fill-style models respond to a description of the desired
//     content, not an action;
//   - the model changed pixels WELL outside the selection (mean difference
//     far above re-render noise) — those were discarded by the strict
//     composite, and the pattern is the signature of a model treating the
//     mask with inverted polarity.
//
// Sampled every 2px, fail-soft (any decode problem returns nil), notice-only:
// the composite remains the delivered result either way.
func inpaintChangeNotices(sourceData, resultData, maskData []byte) []string {
	source, err := decodeComposableImage(sourceData)
	if err != nil {
		return nil
	}
	result, err := decodeComposableImage(resultData)
	if err != nil {
		return nil
	}
	mask, err := png.Decode(bytes.NewReader(maskData))
	if err != nil {
		return nil
	}
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if result.Bounds().Dx() != width || result.Bounds().Dy() != height {
		return nil
	}
	const (
		insideUnchangedThreshold = 3.0
		outsideDriftThreshold    = 12.0
	)
	var insideSum, outsideSum float64
	var insideN, outsideN int
	for y := 0; y < height; y += 2 {
		for x := 0; x < width; x += 2 {
			strength := maskPixelStrength(mask.At(mask.Bounds().Min.X+x, mask.Bounds().Min.Y+y))
			diff := imagePixelMeanDiff(source.At(source.Bounds().Min.X+x, source.Bounds().Min.Y+y), result.At(result.Bounds().Min.X+x, result.Bounds().Min.Y+y))
			if strength > 0 {
				insideSum += diff
				insideN++
			} else {
				outsideSum += diff
				outsideN++
			}
		}
	}
	var notices []string
	if insideN > 0 && insideSum/float64(insideN) < insideUnchangedThreshold {
		notices = append(notices, "The model returned the selected region essentially unchanged — this reads as a prompt/model limitation, not a pipeline fault. Fill-style inpainting responds to a description of what the region should CONTAIN (\"an unlit vintage lamp, dark glass\") rather than an action (\"turn off this light\"), and the selection should cover the whole lamp, not just its glow.")
	}
	if outsideN > 0 && outsideSum/float64(outsideN) > outsideDriftThreshold {
		notices = append(notices, "The model also changed pixels well outside the selection; those were discarded by the preserved-region composite. That pattern usually means this model treats the mask with inverted polarity — if edits keep landing outside, switch to a model with a verified mask contract and report this one.")
	}
	return notices
}

// imagePixelMeanDiff is the mean absolute per-channel difference between two
// pixels, 0–255 scale (alpha excluded).
func imagePixelMeanDiff(a, b color.Color) float64 {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	total := 0
	for _, pair := range [3][2]uint32{{ar, br}, {ag, bg}, {ab, bb}} {
		d := int(pair[0]>>8) - int(pair[1]>>8)
		if d < 0 {
			d = -d
		}
		total += d
	}
	return float64(total) / 3
}
