package main

import (
	"context"
	"strings"
	"testing"
)

// convEfaUserBrief is conv_efa4b078cd9a24df88d76203's request: a six-second
// keyframed transition with a beat-by-beat brief the planner replaced with a
// generic sentence, dropping the stated length as well.
const convEfaUserBrief = `Create 6s pixar style 3d video:

Start frame: @ast_10da0a61

End frame: @img_f06f522b

Animate from the supplied bright start frame to the supplied shadowed end frame in one continuous shot. Lock the camera at plaza level. Keep the shell-and-pearl centerpiece, columns, and floor pattern fixed in the frame.

**0–1 sec.** Venu and Mom hover side by side in the bright plaza. The jade and indigo mermaids watch from the left arch. The coral and turquoise mermaids peek from separate columns on the right.

**1–3 sec.** A broad, soft-edged shadow sweeps across the floor and up the columns, dimming the sunbeams and caustics while the pearl lamps and centerpiece stay warmly lit. The mermaids dart behind the arches.`

const convEfaPlannerPrompt = "A cinematic animation transitioning from the first image to the second. The character's expression shifts subtly, the lighting evolves to match the end frame, and there is a smooth, fluid motion connecting the two states. High detail, professional lighting, seamless transition."

const convEfaTriageTask = `generate_video with imageRole:"keyframes", openingFrame:"first" (since @ast_10da0a61 is the start frame and @img_f06f522b is the end frame), duration:"6", and the detailed prompt describing the animation, lighting, and character movements.`

func runVideoRecovery(t *testing.T, call HarnessToolCall, exec HarnessToolExecutionContext) (VideoGenerateRequest, ToolVideoResult) {
	t.Helper()
	config := defaultAppConfig()
	config.Providers.Fal.VideoKeyframeModel = "fal-ai/test/keyframe"
	config.Providers.Fal.VideoImageModel = "fal-ai/test/image-to-video"
	config.Generation.Video.Duration = "10"
	exec.Config = config
	var captured VideoGenerateRequest
	exec.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
		captured = req
		return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4"}, nil
	}
	def := videoGenerationToolDefinition(AppConfig{}, false)
	result, _, err := def.Execute(context.Background(), exec, call)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	typed, _ := result.(ToolVideoResult)
	return captured, typed
}

func hasDiagnostic(diags []string, fragment string) bool {
	for _, d := range diags {
		if strings.Contains(d, fragment) {
			return true
		}
	}
	return false
}

func TestTriageDurationSeconds(t *testing.T) {
	cases := []struct {
		task string
		want string
	}{
		{convEfaTriageTask, "6"},
		{`generate_video with duration: 8 and a prompt`, "8"},
		{`generate_video {"duration":"5s"}`, "5"},
		{`generate_video duration=4.5`, "4.5"},
		{`generate_video with a 6 second duration`, ""}, // prose, not the contract spelling
		{`generate_video with duration:"auto"`, ""},
		{`generate_video duration:"0"`, ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := triageDurationSeconds(tc.task); got != tc.want {
			t.Errorf("triageDurationSeconds(%q) = %q, want %q", tc.task, got, tc.want)
		}
	}
}

func TestGenerateVideoDurationRecoveredFromTriage(t *testing.T) {
	frames := []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"}

	t.Run("a dropped duration binds triage's stated length", func(t *testing.T) {
		req, out := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: "morph", AspectRatio: "16:9"},
			HarnessToolExecutionContext{AttachedImages: frames, TriageTask: convEfaTriageTask})
		if req.Duration != "6" {
			t.Fatalf("captured.Duration = %q, want the triaged 6 (not the configured 10)", req.Duration)
		}
		if !hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want one naming the recovered duration", out.Diagnostics)
		}
	})

	t.Run("the planner's own duration wins", func(t *testing.T) {
		req, out := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: "morph", Duration: "8"},
			HarnessToolExecutionContext{AttachedImages: frames, TriageTask: convEfaTriageTask})
		if req.Duration != "8" {
			t.Fatalf("captured.Duration = %q, want the planner's 8", req.Duration)
		}
		if hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want no recovery when the call carries a duration", out.Diagnostics)
		}
	})

	t.Run("no triage length keeps the configured default", func(t *testing.T) {
		req, _ := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: "morph"},
			HarnessToolExecutionContext{AttachedImages: frames, TriageTask: "generate_video for a cinematic shot"})
		if req.Duration != "10" {
			t.Fatalf("captured.Duration = %q, want the configured 10", req.Duration)
		}
	})
}

func TestGenerateVideoBriefRecoveredFromUserRequest(t *testing.T) {
	frames := []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"}

	t.Run("a prompt that dropped the user's brief carries the brief instead", func(t *testing.T) {
		req, out := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: convEfaPlannerPrompt, AspectRatio: "16:9"},
			HarnessToolExecutionContext{AttachedImages: frames, TriageTask: convEfaTriageTask, UserRequest: convEfaUserBrief})
		for _, want := range []string{"Venu and Mom", "mermaids", "shell-and-pearl centerpiece", "Lock the camera at plaza level"} {
			if !strings.Contains(req.Prompt, want) {
				t.Fatalf("captured.Prompt lacks %q:\n%s", want, req.Prompt)
			}
		}
		if strings.Contains(req.Prompt, "@ast_") || strings.Contains(req.Prompt, "@img_") {
			t.Fatalf("captured.Prompt kept a mention token the video model cannot resolve:\n%s", req.Prompt)
		}
		if out.Prompt != req.Prompt {
			t.Fatalf("result Prompt = %q, want the prompt actually sent", out.Prompt)
		}
		if !hasDiagnostic(out.Diagnostics, "user's own request") {
			t.Fatalf("diagnostics %v, want one naming the restored brief", out.Diagnostics)
		}
	})

	t.Run("a faithful condensation is left alone", func(t *testing.T) {
		faithful := "Pixar style 3D: one continuous shot locked at plaza level from the bright start frame to the shadowed end frame. Venu and Mom hover side by side; jade and indigo mermaids watch from the left arch, coral and turquoise mermaids peek from the columns. A soft-edged shadow sweeps across the floor and columns, dimming sunbeams and caustics while pearl lamps and the shell-and-pearl centerpiece stay warmly lit; the mermaids dart behind the arches."
		req, out := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: faithful},
			HarnessToolExecutionContext{AttachedImages: frames, UserRequest: convEfaUserBrief})
		if req.Prompt != faithful {
			t.Fatalf("captured.Prompt = %q, want the planner's faithful prompt", req.Prompt)
		}
		if hasDiagnostic(out.Diagnostics, "user's own request") {
			t.Fatalf("diagnostics %v, want no restore", out.Diagnostics)
		}
	})

	t.Run("a short request leaves the planner's elaboration alone", func(t *testing.T) {
		req, _ := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: "A tabby cat leaps across rooftops at dusk, cinematic tracking shot."},
			HarnessToolExecutionContext{UserRequest: "make a video of a cat jumping"})
		if !strings.HasPrefix(req.Prompt, "A tabby cat") {
			t.Fatalf("captured.Prompt = %q, want the planner's prompt", req.Prompt)
		}
	})

	t.Run("a brief past the safe prompt length is not restored", func(t *testing.T) {
		long := convEfaUserBrief + strings.Repeat("\nThe lanterns sway gently as kelp ribbons drift through the colonnade.", 40)
		req, _ := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: convEfaPlannerPrompt},
			HarnessToolExecutionContext{AttachedImages: frames, UserRequest: long})
		if req.Prompt != convEfaPlannerPrompt {
			t.Fatalf("captured.Prompt was replaced by an over-length brief")
		}
	})
}

// The triage prompt must teach the exact spelling triageDurationSeconds reads,
// or the recovery has nothing to recover.
func TestTriagePromptTeachesDurationSpelling(t *testing.T) {
	prompt := triageSystemPrompt(HarnessToolRegistry{}, nil, "/tmp/workspace", false)
	if !strings.Contains(prompt, `duration:"N"`) {
		t.Fatalf("triage prompt lacks the duration:\"N\" toolTask spelling")
	}
	if triageDurationSeconds(`generate_video with duration:"6"`) != "6" {
		t.Fatalf("the taught spelling does not parse")
	}
}
