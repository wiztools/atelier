package main

import (
	"context"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

// conv_6d0c4284f7c739eaf960fd8c is conv_842f67a84cf687ac2f828eff's request
// run again, on the Runware audio provider: "Generate 8s duration audio file
// of underwater scuba swiming sound." The grammar fix held — the planner
// carried the length on the call — but it carried it in the user's spelling,
// duration:"8s", and every resolver parses a duration with strconv.ParseFloat.
// Replayed against the pinned schemas, that spelling dropped on the Runware,
// fal, and Replicate sound resolvers, on the extend pair, and on
// videoDurationClamped, and soundLengthNotice (the same parse) stood silent
// beside it. The conv itself shipped 164s only because its pinned model had no
// duration control at all; on the default mirelo the same call would have
// rendered the model's 10s for an 8s ask. The executors now re-spell a call's
// own value before the resolvers see it.
const (
	conv6d0cDuration = "8s"
	conv6d0cPrompt   = "underwater scuba swimming sound, bubbles, gentle water movement"
)

// TestNormalizeDurationSeconds pins the helper: a number with an optional
// seconds unit becomes the bare decimal, and everything else — "auto", the
// empty value, prose, another unit — reaches the resolver unchanged, so its
// own unreadable-value notice still speaks.
func TestNormalizeDurationSeconds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"8s", "8"},
		{"8", "8"},
		{" 7.5 s ", "7.5"},
		{"8 seconds", "8"},
		{"8 Seconds", "8"},
		{"8S", "8"},
		{"8sec", "8"},
		{"auto", "auto"},
		{"  ", ""},
		{"about 8 seconds", "about 8 seconds"}, // prose: the resolver's notice names it
		{"2min", "2min"},                       // another unit is not re-read as seconds
		{"8s.", "8s."},
	}
	for _, tc := range cases {
		if got := normalizeDurationSeconds(tc.in); got != tc.want {
			t.Errorf("normalizeDurationSeconds(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSuffixedDurationReachesTheResolvers runs the conv's call value through
// each of the three executors that read a call's duration — the planner and
// the manual tool path both land here — and checks that the request each
// builds carries the bare decimal. No recovery diagnostic rides: nothing was
// recovered, the planner stated the length and the call keeps it.
func TestSuffixedDurationReachesTheResolvers(t *testing.T) {
	t.Run("generate_video", func(t *testing.T) {
		req, out := runVideoRecovery(t,
			HarnessToolCall{Name: "generate_video", Content: "morph", Duration: conv6d0cDuration},
			HarnessToolExecutionContext{AttachedImages: []string{"data:image/png;base64,AAA"}, TriageTask: convEfaTriageTask})
		if req.Duration != "8" {
			t.Fatalf("captured.Duration = %q, want the bare 8", req.Duration)
		}
		if hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want no recovery when the call carries a duration", out.Diagnostics)
		}
	})

	t.Run("generate_sound", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_sound", Content: conv6d0cPrompt, Duration: conv6d0cDuration, Model: conv842SoundModel}
		req, out, _ := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig(), TriageTask: conv842ContractTask}, mediasnifftest.WAV(24000))
		if req.Duration != "8" {
			t.Fatalf("captured.Duration = %q, want the bare 8", req.Duration)
		}
		if hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want no recovery when the call carries a duration", out.Diagnostics)
		}
	})

	t.Run("extend_audio", func(t *testing.T) {
		var sawDuration string
		config := defaultAppConfig()
		config.Generation.Audio.ExtendDuration = "20"
		tools := HarnessToolExecutionContext{
			Config:         config,
			AttachedAudios: []string{"data:audio/mpeg;base64,QQ=="},
			GenerateAudioExtend: func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
				sawDuration = req.Duration
				return GeneratedAudio{Data: []byte("fake-mp3"), MimeType: "audio/mpeg"}, nil
			},
		}
		call := HarnessToolCall{Name: "extend_audio", Content: "continue in the same style", Duration: "30s"}
		if _, _, err := extendAudioToolDefinition("fal").Execute(context.Background(), tools, call); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if sawDuration != "30" {
			t.Fatalf("GenerateAudioExtend saw duration %q, want the bare 30", sawDuration)
		}
	})
}

// TestSuffixedDurationLengthNotice: with the call re-spelled, the length
// backstop compares again. The conv's call against a 3s delivery names the
// disagreement; before the re-spelling the notice's own parse refused "8s"
// and the result said nothing about the length.
func TestSuffixedDurationLengthNotice(t *testing.T) {
	call := HarnessToolCall{Name: "generate_sound", Content: conv6d0cPrompt, Duration: conv6d0cDuration, Model: conv842SoundModel}
	_, out, _ := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig()}, mediasnifftest.WAV(24000))
	notices := out.ToolNotices()
	want := "Requested 8s of audio, but " + conv842SoundModel + " delivered 3.0s. Atelier did not adjust the clip's length."
	if len(notices) != 1 || notices[0] != want {
		t.Fatalf("notices = %v, want exactly %q", notices, want)
	}
}
