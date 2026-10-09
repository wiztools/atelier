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
	"math"
	"os"
	"path/filepath"
	"strconv"
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
	// MediaKind is "image" for legacy and current image sessions, "video" for
	// Video Editor sessions. Empty is treated as image for old records.
	MediaKind string `json:"mediaKind,omitempty"`
}

// EditSourceInfo is what the editor needs to open a source image: identity,
// a renderable URL, and display dimensions.
type EditSourceInfo struct {
	ConversationID    string   `json:"conversationId"`
	ConversationTitle string   `json:"conversationTitle,omitempty"`
	OriginTurnID      string   `json:"originTurnId,omitempty"`
	ArtifactID        string   `json:"artifactId"`
	URL               string   `json:"url"`
	MimeType          string   `json:"mimeType,omitempty"`
	Width             int      `json:"width,omitempty"`
	Height            int      `json:"height,omitempty"`
	MediaKind         string   `json:"mediaKind,omitempty"`
	DurationSeconds   float64  `json:"durationSeconds,omitempty"`
	SourceDigest      string   `json:"sourceDigest,omitempty"`
	Thumbnails        []string `json:"thumbnails,omitempty"`
	Notices           []string `json:"notices,omitempty"`
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
// dimensions, sent where the model accepts an explicit output size.
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
			{ID: "black-forest-labs/flux-fill-dev", Label: "FLUX.1 Fill [dev]", Note: "Official, open weights, cheaper. Inputs snap to 32-pixel multiples and cap at 1440×1440; the result keeps the model’s output dimensions."},
		}
	default:
		return []InpaintModelOption{
			{ID: defaultFalInpaintModel, Label: "FLUX.1 Fill [pro]", Note: "Follows the source’s shape, but fal may downscale large inputs. The result keeps the model’s output dimensions. Billed per megapixel."},
			{ID: "fal-ai/qwen-image-edit/inpaint", Label: "Qwen Image Edit (inpaint)", Note: "Takes an explicit pixel output size and a negative prompt. May change pixels outside the selection; the full result is kept with a notice."},
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
	return a.ReleaseVideoEditSourcePreview(previewURL)
}

// ReleaseVideoEditSourcePreview removes one preview file returned by either
// editor source resolver. The videoSourcePreviews entry matching the URL is
// forgotten so the submit path cannot reuse a released preview, but deletion
// is scoped to the named file: a sibling release must never cascade into the
// generation's other files, which a live editor may still be rendering (for
// example under React's dev StrictMode unmount replay). URLs outside
// Atelier's guarded preview directory are ignored.
func (a *App) ReleaseVideoEditSourcePreview(previewURL string) error {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	a.videoPreviewMu.Lock()
	for key, info := range a.videoSourcePreviews {
		if info.URL == previewURL {
			delete(a.videoSourcePreviews, key)
			break
		}
	}
	a.videoPreviewMu.Unlock()
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
		MediaKind:         "image",
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
	return findMediaContent(detail, artifactID, "image")
}

func findVideoContent(detail ConversationDetail, artifactID string) (HistoryContent, string, bool) {
	return findMediaContent(detail, artifactID, "video")
}

func findMediaContent(detail ConversationDetail, artifactID, mediaKind string) (HistoryContent, string, bool) {
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.Type != mediaKind {
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
	return firstSessionSourceMedia(detail, "image")
}

func firstSessionSourceVideo(detail ConversationDetail) (HistoryContent, string, bool) {
	return firstSessionSourceMedia(detail, "video")
}

func firstSessionSourceMedia(detail ConversationDetail, mediaKind string) (HistoryContent, string, bool) {
	for _, turn := range detail.Turns {
		if turn.Role != "user" {
			continue
		}
		for _, content := range turn.Content {
			if content.Type == mediaKind && content.ArtifactID != "" {
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
		// (the parent), normalized (orientation baked, Go-decodable),
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
// transition. Inpainting keeps the provider output and reports changes outside the mask.
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
	case execErr == nil && (op.Kind == editOperationKindInpaint || op.Kind == editOperationKindCrop):
		// Keep the provider's complete output. The selection is guidance;
		// outside-mask changes are reported, never removed from the result.
		if op.Kind == editOperationKindInpaint {
			op.Notices = append(op.Notices, inpaintChangeNotices(inputData, resultData, maskData)...)
		}
		width, height, valid := editImageDimensions(resultData)
		if !valid {
			op.Status = editOperationStatusFailed
			op.Error = "the edit returned an invalid image"
			break
		}
		loaded, loadErr := loadForEditAppend(config.Storage, sessionID)
		if loadErr != nil {
			op.Status = editOperationStatusFailed
			op.Error = fmt.Sprintf("the edit ran but its result could not be saved: %v", loadErr)
			break
		}
		contents, writeErr := writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{
			Width:  width,
			Height: height,
		}, []string{imageDataURLForBytes(resultData)})
		if writeErr != nil || len(contents) == 0 {
			op.Status = editOperationStatusFailed
			if writeErr == nil {
				writeErr = errors.New("the edit produced no image")
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
	contentType := editResultMediaKind(op)
	return []HistoryContent{{
		Type:       contentType,
		ArtifactID: op.ResultArtifactID,
		Path:       op.ResultPath,
		MimeType:   op.ResultMimeType,
		Width:      op.ResultWidth,
		Height:     op.ResultHeight,
	}}
}

func editResultMediaKind(op EditOperation) string {
	if strings.HasPrefix(op.ResultMimeType, "video/") || op.Kind == editOperationKindReframe {
		return "video"
	}
	return "image"
}

// executeInpaintProvider dispatches the canonical inpaint request to the
// recorded provider and returns the model's raw output bytes plus cost
// attribution. Both verified defaults speak Atelier's canonical mask polarity
// (white = editable), so no inversion rides this path — see
// the mask export in frontend/src/editor/MaskBrushTool.ts.
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

// DeleteEditOperation removes one edit from a session: its turn file and the
// artifact files the operation owns — the result (image or video, with the
// poster and filmstrip siblings the media conventions write beside a clip)
// and an inpaint op's selection mask. What an operation consumed is never
// deleted (it is the session's source copy or an earlier op's result), and a
// result adopted into the parent conversation is a copy, so both survive. A
// later op that consumed the deleted result keeps its own output; only its
// before-reference dangles. Refused while any operation in the session is
// running — the executor rewrites the conversation record, and a concurrent
// record write would clobber one of the two — and for a target that is itself
// queued/running (cancel it first). Deletion leaves a numbering gap in the
// turns directory, which nextEditTurnNumber absorbs.
func (a *App) DeleteEditOperation(sessionConversationID, operationID string) error {
	config, err := loadReadyConfig()
	if err != nil {
		return err
	}
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	sessionID := strings.TrimSpace(sessionConversationID)
	operationID = strings.TrimSpace(operationID)
	if sessionID == "" || operationID == "" {
		return errors.New("a session and operation id are required")
	}
	loaded, err := loadForEditAppend(config.Storage, sessionID)
	if err != nil {
		return err
	}
	if a.editOpRunning(sessionID) {
		return errors.New("an edit operation is running in this session — wait for it to finish or cancel it first")
	}
	detail, err := getConversation(config.Storage, sessionID)
	if err != nil {
		return err
	}
	for _, turn := range detail.Turns {
		op, ok := editOperationFromTurn(turn)
		if !ok || op.ID != operationID {
			continue
		}
		if op.Status == editOperationStatusQueued || op.Status == editOperationStatusRunning {
			return errors.New("this edit is still running — cancel it first")
		}
		if err := os.Remove(filepath.Join(loaded.TurnsDir, turn.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removeEditOperationFiles(filepath.Dir(loaded.Path), op)
		loaded.Conversation.UpdatedAt = time.Now().Format(time.RFC3339)
		loaded.Conversation.Stats.TurnCount = max(0, loaded.Conversation.Stats.TurnCount-1)
		if op.Status == editOperationStatusCompleted && op.ResultArtifactID != "" {
			loaded.Conversation.Stats.ArtifactCount = max(0, loaded.Conversation.Stats.ArtifactCount-1)
		}
		return newHistoryStore(config.Storage).writeConversation(loaded.Path, loaded.Conversation)
	}
	return fmt.Errorf("operation %s not found in session %s", operationID, sessionID)
}

// removeEditOperationFiles deletes the artifact files one operation owns,
// best-effort: the result plus the poster/filmstrip siblings written beside a
// video clip (absent for images), and the inpaint selection mask. Missing
// files are the expected case for failed operations.
func removeEditOperationFiles(conversationDir string, op EditOperation) {
	remove := func(relSlash string) {
		if relSlash == "" {
			return
		}
		_ = os.Remove(filepath.Join(conversationDir, filepath.FromSlash(relSlash)))
	}
	if op.ResultPath != "" {
		remove(op.ResultPath)
		resultAbs := filepath.Join(conversationDir, filepath.FromSlash(op.ResultPath))
		stem := strings.TrimSuffix(resultAbs, filepath.Ext(resultAbs))
		_ = os.Remove(stem + "_poster.jpg")
		entries, err := os.ReadDir(filepath.Dir(stem))
		if err == nil {
			prefix := filepath.Base(stem) + "_thumb_"
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jpg") {
					_ = os.Remove(filepath.Join(filepath.Dir(stem), name))
				}
			}
		}
	}
	if op.Inpaint != nil {
		remove(op.Inpaint.MaskPath)
	}
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
		ConversationID:       sessionID,
		ParentConversationID: meta.ParentConversationID,
		ParentTitle:          meta.SourceTitleSnapshot,
	}
	mediaKind := editSessionMediaKind(meta)
	if mediaKind == "image" {
		state.InpaintProvider = config.Models.InpaintProvider
		state.InpaintFalModel = config.Providers.Fal.InpaintModel
		state.InpaintReplicateModel = config.Providers.Replicate.InpaintModel
		if falKey, err := loadFalAPIKey(); err == nil && strings.TrimSpace(falKey) != "" {
			state.FalConfigured = true
		}
		if repKey, err := loadReplicateAPIKey(); err == nil && strings.TrimSpace(repKey) != "" {
			state.ReplicateConfigured = true
		}
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
	if mediaKind == "video" {
		if content, turnID, ok := firstSessionSourceVideo(detail); ok {
			info, err := videoEditSourceInfoFor(config.Storage, sessionID, state.ParentTitle, turnID, content, nil)
			if err != nil {
				return EditSessionState{}, fmt.Errorf("could not open the saved video source: %w", err)
			}
			info.ConversationID = sessionID
			state.Source = info
		} else {
			return EditSessionState{}, errors.New("the saved video source is missing")
		}
	} else {
		if content, turnID, ok := firstSessionSourceImage(detail); ok {
			if info, err := editSourceInfoFor(config.Storage, sessionID, state.ParentTitle, turnID, content); err == nil {
				info.ConversationID = sessionID
				state.Source = info
			}
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
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
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

	mediaKind := editResultMediaKind(op)
	// Read the session-owned result.
	resultContent, _, ok := findMediaContent(detail, op.ResultArtifactID, mediaKind)
	if !ok {
		return ConversationSummary{}, errors.New("the result artifact is missing from the session")
	}
	resultPath, err := contentArtifactPath(config.Storage, sessionID, resultContent)
	if err != nil {
		return ConversationSummary{}, fmt.Errorf("could not read the result: %w", err)
	}

	// Mark the op adopted FIRST so a crash between the two writes can only
	// lose the adoption (retryable), never duplicate the parent entry. On a
	// parent-write failure the mark is reverted best-effort.
	if err := markEditOperationAdopted(config.Storage, sessionID, operationID); err != nil {
		return ConversationSummary{}, err
	}
	summary, adoptErr := appendEditAdoptionTurn(config, parentID, meta, op, resultPath, sessionID)
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
func appendEditAdoptionTurn(config AppConfig, parentID string, meta *EditSessionMeta, op EditOperation, resultPath, sessionID string) (ConversationSummary, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(parentID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return ConversationSummary{}, err
	}
	nowText := time.Now().Format(time.RFC3339)
	var contents []HistoryContent
	if editResultMediaKind(op) == "video" {
		tempCopy, copyErr := copyEditResultToTemp(resultPath, ".mp4")
		if copyErr != nil {
			return ConversationSummary{}, copyErr
		}
		defer os.Remove(tempCopy)
		contents, _, err = writeChatVideoArtifacts(config, loaded.ArtifactsDir, []ToolVideoFile{{TempPath: tempCopy, MimeType: "video/mp4"}})
	} else {
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			return ConversationSummary{}, readErr
		}
		contents, err = writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{
			Width:  op.ResultWidth,
			Height: op.ResultHeight,
		}, []string{imageDataURLForBytes(data)})
	}
	if err != nil || len(contents) == 0 {
		if err == nil {
			err = errors.New("the result could not be copied")
		}
		return ConversationSummary{}, err
	}
	contents[0].Width, contents[0].Height = op.ResultWidth, op.ResultHeight
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
				"sessionId":   sessionID,
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
	case editOperationKindReframe:
		if op.Reframe == nil {
			return "reframe"
		}
		return fmt.Sprintf("reframe: %s, %d × %d", op.Reframe.AspectRatio, op.ResultWidth, op.ResultHeight)
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
	mediaKind := editSessionMediaKind(detail.Conversation.EditSession)
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
			if content.Type == mediaKind && content.ArtifactID != "" {
				return content.ArtifactID
			}
		}
	}
	return ""
}

func editSessionMediaKind(meta *EditSessionMeta) string {
	if meta != nil && strings.TrimSpace(meta.MediaKind) == "video" {
		return "video"
	}
	return "image"
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
		NextTurnNumber: nextEditTurnNumber(turnsDir),
	}, nil
}

// nextEditTurnNumber returns one past the highest turn number on disk. Edit
// sessions can delete operation turns (DeleteEditOperation), leaving gaps, so
// a file count would eventually drop below the highest surviving number and
// the next append would overwrite that turn.
func nextEditTurnNumber(turnsDir string) int {
	next := 1
	entries, err := os.ReadDir(turnsDir)
	if err != nil {
		return next
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw := strings.TrimSuffix(entry.Name(), ".json")
		raw = strings.TrimPrefix(raw, "turn_")
		if n, err := strconv.Atoi(raw); err == nil && n >= next {
			next = n + 1
		}
	}
	return next
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

// inpaintChangeNotices reports unchanged selections and substantial changes
// outside the selection. A comparison copy is resized for near-matching aspect
// ratios; the saved provider output is never resized or composited. Different
// shapes cannot be compared reliably and receive a size notice instead.
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
	if mask.Bounds().Dx() != width || mask.Bounds().Dy() != height {
		return nil
	}
	var notices []string
	outWidth, outHeight := result.Bounds().Dx(), result.Bounds().Dy()
	resized := outWidth != width || outHeight != height
	if resized {
		notices = append(notices, fmt.Sprintf("The model returned %dx%d for the %dx%d source. The result keeps the model's original dimensions.", outWidth, outHeight, width, height))
		sourceRatio := float64(width) / float64(height)
		outputRatio := float64(outWidth) / float64(outHeight)
		if math.Abs(sourceRatio-outputRatio) > sourceRatio*0.02 {
			return append(notices, "The output shape differs from the source, so changes outside the selection could not be checked reliably. The full model output was kept.")
		}
		result = resizeImageBilinear(result, width, height)
	}
	const (
		insideUnchangedThreshold = 3.0
		outsideDriftThreshold    = 12.0
	)
	var insideSum float64
	var insideN int
	outsideChanged := false
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			strength := maskPixelStrength(mask.At(mask.Bounds().Min.X+x, mask.Bounds().Min.Y+y))
			diff := imagePixelMeanDiff(source.At(source.Bounds().Min.X+x, source.Bounds().Min.Y+y), result.At(result.Bounds().Min.X+x, result.Bounds().Min.Y+y))
			if strength > 0 {
				insideSum += diff
				insideN++
			} else {
				outsideChanged = outsideChanged || diff > outsideDriftThreshold
			}
		}
	}
	if insideN > 0 && insideSum/float64(insideN) < insideUnchangedThreshold {
		notices = append(notices, "The model returned the selected region essentially unchanged. The full model output was kept.")
	}
	if outsideChanged {
		notice := "The model changed pixels outside the selection. These changes were kept in the result."
		if resized {
			notice = "The model output differs outside the selection after aligning dimensions for comparison; resizing may contribute to these differences. The full model output was kept."
		}
		notices = append(notices, notice)
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
