package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConversationModelOverrides is the per-conversation model-selection override
// — the Settings → Models fields plus the primary chat model, scoped to one
// conversation. Every field is omitempty and empty means "inherit the live
// global Settings value" (differential semantics): only fields the user
// explicitly changed in this conversation are stored, so later global changes
// still reach the unset ones.
type ConversationModelOverrides struct {
	// PrimaryProvider/PrimaryModel override the primary chat role. The pair is
	// pinned together in the UI: touching either stores both.
	PrimaryProvider string `json:"primaryProvider,omitempty"`
	PrimaryModel    string `json:"primaryModel,omitempty"`
	// HarnessProvider/HarnessModel override the harness role (triage, skill
	// selection, planning).
	HarnessProvider string `json:"harnessProvider,omitempty"`
	HarnessModel    string `json:"harnessModel,omitempty"`
	// ImageProvider/ImageModel override generate_image's backend and model.
	// ImageModel lands on the slot the effective image provider reads
	// (fal Model / OpenAICompatible Model / Ollama Models.Image).
	ImageProvider string `json:"imageProvider,omitempty"`
	ImageModel    string `json:"imageModel,omitempty"`
	// The fal endpoint fields map 1:1 onto ConfigFal — fal is the only backend
	// for these tools, so there is no provider dimension.
	ImageEditModel    string `json:"imageEditModel,omitempty"`
	UpscaleModel      string `json:"upscaleModel,omitempty"`
	VideoModel        string `json:"videoModel,omitempty"`
	VideoImageModel   string `json:"videoImageModel,omitempty"`
	VideoExtendModel  string `json:"videoExtendModel,omitempty"`
	VideoMotionModel  string `json:"videoMotionModel,omitempty"`
	VideoUpscaleModel string `json:"videoUpscaleModel,omitempty"`
	AudioModel        string `json:"audioModel,omitempty"`
	SoundEffectsModel string `json:"soundEffectsModel,omitempty"`
	AudioCloneModel   string `json:"audioCloneModel,omitempty"`
	AudioExtendModel  string `json:"audioExtendModel,omitempty"`
	TranscribeModel   string `json:"transcribeModel,omitempty"`
	LipsyncImageModel string `json:"lipsyncImageModel,omitempty"`
	LipsyncVideoModel string `json:"lipsyncVideoModel,omitempty"`
	// TranscriptionProvider/WhisperModel/WhisperBinary override transcribe_audio's
	// backend plus the local whisper model and binary (everything the Models
	// tab's Transcription section edits).
	TranscriptionProvider string `json:"transcriptionProvider,omitempty"`
	WhisperModel          string `json:"whisperModel,omitempty"`
	WhisperBinary         string `json:"whisperBinary,omitempty"`
	// The generation defaults the Models tab edits. The planner can still vary
	// these per call — these are the per-conversation defaults the tools fall
	// back to. ImageSteps is the one int: 0 means inherit (normalize clamps
	// negatives away). VideoDuration is the one canonical duration the backend
	// reads for all three video paths; the Models tab's image-to-video and
	// extend duration pickers are frontend-only previews and ride no payload.
	ImageAspectRatio string `json:"imageAspectRatio,omitempty"`
	ImageSizePreset  string `json:"imageSizePreset,omitempty"`
	ImageSteps       int    `json:"imageSteps,omitempty"`
	VideoDuration    string `json:"videoDuration,omitempty"`
	VideoAspectRatio string `json:"videoAspectRatio,omitempty"`
}

// conversationModelOverridesActive reports whether any field is set — the
// record stores the struct nil unless it would change something.
func conversationModelOverridesActive(o ConversationModelOverrides) bool {
	return o != ConversationModelOverrides{}
}

// normalizeConversationModelOverrides trims every field and zeroes the ones
// that end up empty, so whitespace-only values inherit instead of overriding
// with a blank model.
func normalizeConversationModelOverrides(o ConversationModelOverrides) ConversationModelOverrides {
	fields := []*string{
		&o.PrimaryProvider, &o.PrimaryModel,
		&o.HarnessProvider, &o.HarnessModel,
		&o.ImageProvider, &o.ImageModel,
		&o.ImageEditModel, &o.UpscaleModel,
		&o.VideoModel, &o.VideoImageModel, &o.VideoExtendModel, &o.VideoMotionModel, &o.VideoUpscaleModel,
		&o.AudioModel, &o.SoundEffectsModel, &o.AudioCloneModel, &o.AudioExtendModel, &o.TranscribeModel,
		&o.LipsyncImageModel, &o.LipsyncVideoModel,
		&o.TranscriptionProvider, &o.WhisperModel, &o.WhisperBinary,
		&o.ImageAspectRatio, &o.ImageSizePreset, &o.VideoDuration, &o.VideoAspectRatio,
	}
	for _, field := range fields {
		*field = strings.TrimSpace(*field)
	}
	if o.ImageSteps < 0 {
		o.ImageSteps = 0
	}
	return o
}

// validateConversationModelOverrides checks the provider-name fields against
// the same whitelists mergeAppConfig enforces. Model ids are deliberately not
// validated — openai-compatible accepts arbitrary strings and there is no
// offline catalog to check against.
func validateConversationModelOverrides(o ConversationModelOverrides) error {
	for name, value := range map[string]string{
		"primaryProvider":       o.PrimaryProvider,
		"harnessProvider":       o.HarnessProvider,
		"imageProvider":         o.ImageProvider,
		"transcriptionProvider": o.TranscriptionProvider,
	} {
		switch name {
		case "imageProvider":
			if value != "" && value != "ollama" && value != "fal" && value != "openai-compatible" {
				return fmt.Errorf("unknown image provider %q", value)
			}
		case "transcriptionProvider":
			if value != "" && value != transcriptionProviderFal && value != transcriptionProviderLocalWhisper {
				return fmt.Errorf("unknown transcription provider %q", value)
			}
		default:
			if value != "" && value != "ollama" && value != "openrouter" && value != "openai-compatible" {
				return fmt.Errorf("unknown %s %q", name, value)
			}
		}
	}
	return nil
}

// readConversationModelOverrides loads a conversation record solely to read
// its model overrides. Nil means no override (including legacy records
// written before the field existed). Like readConversationWorkspace this is a
// second small read of conversation.json at turn start, not per-round.
func readConversationModelOverrides(storage ConfigStorage, conversationID string) (*ConversationModelOverrides, error) {
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		return nil, err
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		return nil, err
	}
	return conversation.ModelOverrides, nil
}

// requestModelOverrides returns the normalized turn-1 model override a
// ChatRequest carries for the conversation being created — nil when absent,
// all-empty, or whitespace-only. Both creation paths (writeChatConversation,
// writePendingChatConversation) pin it onto the new record; for existing
// conversations the field is ignored and the record wins.
func requestModelOverrides(req ChatRequest) *ConversationModelOverrides {
	if req.ModelOverrides == nil {
		return nil
	}
	normalized := normalizeConversationModelOverrides(*req.ModelOverrides)
	if !conversationModelOverridesActive(normalized) {
		return nil
	}
	return &normalized
}

// applyConversationModelOverrides overlays the conversation's model overrides
// onto the per-stream config copy before the HarnessEngine is built — the
// resolveTurnWorkspace pattern: one rewrite scopes harness routing
// (resolveHarnessTarget), tool-registry gating, every tool's default model
// (tools.Config), and gateway routing through h.config, with no per-callsite
// branching. The returned config is never persisted. Turn 2+ reads the
// override from the record. Turn 1 has no record yet, but the request may
// carry one for the conversation being created (ChatRequest.ModelOverrides,
// the workspace/project lifecycle): it is applied the same way so the first
// turn runs on it, and StartChatTurn persists it onto the new record
// (requestModelOverrides). A primary override also rewrites the request's
// model fields: the override wins over whatever the request carries.
func applyConversationModelOverrides(config AppConfig, req ChatRequest) (AppConfig, ChatRequest, error) {
	overrides := req.ModelOverrides
	if strings.TrimSpace(req.ConversationID) != "" {
		recordOverrides, err := readConversationModelOverrides(config.Storage, req.ConversationID)
		if err != nil {
			return config, req, err
		}
		// The record wins for an existing conversation — the request field is
		// turn-1-only and must never rewrite a live conversation's override.
		overrides = recordOverrides
	}
	if overrides == nil {
		return config, req, nil
	}
	// Normalize before overlay: a request-borne override hasn't been through
	// the mutator's trimming, and the first turn must run on exactly the
	// values the creation paths persist. Idempotent for record overrides,
	// which the mutator already normalized.
	normalized := normalizeConversationModelOverrides(*overrides)
	if !conversationModelOverridesActive(normalized) {
		return config, req, nil
	}
	return overlayModelOverrides(config, req, normalized)
}

// overlayModelOverrides is the pure applyConversationModelOverrides step: it
// never reads disk, so tests can drive it directly.
func overlayModelOverrides(config AppConfig, req ChatRequest, o ConversationModelOverrides) (AppConfig, ChatRequest, error) {
	// Primary pair. An unset field inherits from the global config — the
	// provider from Models.PrimaryProvider, the model from that provider's
	// global slot — and if that slot is empty too, the request's own model
	// (what the composer was showing) is the next best value rather than a
	// blank that would fail the turn.
	if o.PrimaryProvider != "" || o.PrimaryModel != "" {
		provider := chatProviderOrDefault(o.PrimaryProvider, config.Models.PrimaryProvider)
		model := o.PrimaryModel
		if model == "" {
			model = primaryModelSlot(config, provider)
		}
		if model == "" {
			model = strings.TrimSpace(req.Model)
		}
		if model == "" {
			model = strings.TrimSpace(req.SelectedModel)
		}
		if model != "" {
			config.Models.PrimaryProvider = provider
			setPrimaryModelSlot(&config, provider, model)
			req.Provider = provider
			req.Model = model
			req.SelectedModel = model
		}
	}
	// Harness pair, same inheritance rules. resolveHarnessTarget falls back
	// to the primary target when the provider's slot ends up empty, so an
	// inherited-empty model degrades gracefully instead of failing.
	if o.HarnessProvider != "" || o.HarnessModel != "" {
		provider := chatProviderOrDefault(o.HarnessProvider, config.Models.HarnessProvider)
		config.Models.HarnessProvider = provider
		if model := o.HarnessModel; model != "" {
			setHarnessModelSlot(&config, provider, model)
		}
	}
	if o.ImageProvider != "" {
		config.Models.ImageProvider = o.ImageProvider
	}
	if o.ImageModel != "" {
		// The image provider dimension includes "fal", which the chat-provider
		// normalizer must never see; validation at mutator time keeps the
		// values inside {ollama, fal, openai-compatible, ""}.
		provider := o.ImageProvider
		if provider == "" {
			provider = config.Models.ImageProvider
		}
		switch provider {
		case "fal":
			config.Providers.Fal.Model = o.ImageModel
		case "openai-compatible":
			config.Providers.OpenAICompatible.Model = o.ImageModel
		default:
			config.Providers.Ollama.Models.Image = o.ImageModel
		}
	}
	if o.ImageEditModel != "" {
		config.Providers.Fal.ImageEditModel = o.ImageEditModel
	}
	if o.UpscaleModel != "" {
		config.Providers.Fal.UpscaleModel = o.UpscaleModel
	}
	if o.VideoModel != "" {
		config.Providers.Fal.VideoModel = o.VideoModel
	}
	if o.VideoImageModel != "" {
		config.Providers.Fal.VideoImageModel = o.VideoImageModel
	}
	if o.VideoExtendModel != "" {
		config.Providers.Fal.VideoExtendModel = o.VideoExtendModel
	}
	if o.VideoMotionModel != "" {
		config.Providers.Fal.VideoMotionModel = o.VideoMotionModel
	}
	if o.VideoUpscaleModel != "" {
		config.Providers.Fal.VideoUpscaleModel = o.VideoUpscaleModel
	}
	if o.AudioModel != "" {
		config.Providers.Fal.AudioModel = o.AudioModel
	}
	if o.SoundEffectsModel != "" {
		config.Providers.Fal.SoundEffectsModel = o.SoundEffectsModel
	}
	if o.AudioCloneModel != "" {
		config.Providers.Fal.AudioCloneModel = o.AudioCloneModel
	}
	if o.AudioExtendModel != "" {
		config.Providers.Fal.AudioExtendModel = o.AudioExtendModel
	}
	if o.TranscribeModel != "" {
		config.Providers.Fal.TranscribeModel = o.TranscribeModel
	}
	if o.LipsyncImageModel != "" {
		config.Providers.Fal.LipsyncImageModel = o.LipsyncImageModel
	}
	if o.LipsyncVideoModel != "" {
		config.Providers.Fal.LipsyncVideoModel = o.LipsyncVideoModel
	}
	if o.TranscriptionProvider != "" {
		config.Models.TranscriptionProvider = o.TranscriptionProvider
	}
	if o.WhisperModel != "" {
		config.Providers.Local.Whisper.Model = o.WhisperModel
	}
	if o.WhisperBinary != "" {
		config.Providers.Local.Whisper.Binary = o.WhisperBinary
	}
	if o.ImageAspectRatio != "" {
		config.Generation.Image.AspectRatio = o.ImageAspectRatio
	}
	if o.ImageSizePreset != "" {
		config.Generation.Image.SizePreset = o.ImageSizePreset
	}
	if o.ImageSteps > 0 {
		config.Generation.Image.Steps = o.ImageSteps
	}
	if o.VideoDuration != "" {
		config.Generation.Video.Duration = o.VideoDuration
	}
	if o.VideoAspectRatio != "" {
		config.Generation.Video.AspectRatio = o.VideoAspectRatio
	}
	return config, req, nil
}

// chatProviderOrDefault normalizes a chat provider id the way
// resolvedPrimaryModelAndProvider does: unknown or empty falls back to the
// given default ("ollama" behavior for anything unrecognized).
func chatProviderOrDefault(provider, fallback string) string {
	switch strings.TrimSpace(provider) {
	case "openrouter", "openai-compatible", "ollama":
		return strings.TrimSpace(provider)
	}
	if fallback == "openrouter" || fallback == "openai-compatible" {
		return fallback
	}
	return "ollama"
}

func primaryModelSlot(config AppConfig, provider string) string {
	switch provider {
	case "openrouter":
		return strings.TrimSpace(config.Providers.OpenRouter.Primary)
	case "openai-compatible":
		return strings.TrimSpace(config.Providers.OpenAICompatible.Primary)
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Primary)
}

func setPrimaryModelSlot(config *AppConfig, provider, model string) {
	switch provider {
	case "openrouter":
		config.Providers.OpenRouter.Primary = model
	case "openai-compatible":
		config.Providers.OpenAICompatible.Primary = model
	default:
		config.Providers.Ollama.Models.Primary = model
	}
}

func setHarnessModelSlot(config *AppConfig, provider, model string) {
	switch provider {
	case "openrouter":
		config.Providers.OpenRouter.Harness = model
	case "openai-compatible":
		config.Providers.OpenAICompatible.Harness = model
	default:
		config.Providers.Ollama.Models.Harness = model
	}
}

// SetConversationModelOverrides replaces (or clears, when every field is
// empty) the per-conversation model-selection override. Refused while the
// conversation is streaming — the append path rewrites conversation.json from
// its own load, and a concurrent override would clobber one of the two
// writes. This is the one sanctioned mutation of ModelOverrides after
// creation, like MoveConversationToProject for ProjectID.
func (a *App) SetConversationModelOverrides(conversationID string, overrides ConversationModelOverrides) (ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	if a.conversationStreaming(conversationID) {
		return ConversationSummary{}, errors.New("this conversation is still running; wait for it to finish before changing its model overrides")
	}
	normalized := normalizeConversationModelOverrides(overrides)
	if err := validateConversationModelOverrides(normalized); err != nil {
		return ConversationSummary{}, err
	}
	return setConversationModelOverrides(config.Storage, conversationID, normalized)
}

// setConversationModelOverrides is the package-level record rewrite behind
// the bound mutator: read, reject deleted, swap the field (nil when nothing
// is active), bump UpdatedAt, rewrite conversation.json only — artifacts and
// turns never move.
func setConversationModelOverrides(storage ConfigStorage, conversationID string, overrides ConversationModelOverrides) (ConversationSummary, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationSummary{}, err
	}
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationSummary{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationSummary{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	if conversationModelOverridesActive(overrides) {
		conversation.ModelOverrides = &overrides
	} else {
		conversation.ModelOverrides = nil
	}
	conversation.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return ConversationSummary{}, err
	}
	return conversationSummaryFrom(conversation), nil
}
