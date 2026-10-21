package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compositionMP4Fixture builds ftyp + moov{mvhd, video trak, [audio trak]} —
// the shape the MP4 box sniff measures when ffprobe is absent (TestMain pins
// every binary lookup off): mvhd carries the duration, the video trak's tkhd
// the presentation size, and the optional 'soun' trak the audio presence.
func compositionMP4Fixture(seconds float64, width, height uint32, withAudio bool) []byte {
	const timescale = 1000
	mvhd := []byte{0, 0, 0, 0}
	mvhd = append(mvhd, make([]byte, 8)...)
	mvhd = binary.BigEndian.AppendUint32(mvhd, timescale)
	mvhd = binary.BigEndian.AppendUint32(mvhd, uint32(math.Round(seconds*timescale)))
	if pad := 0x64 - len(mvhd); pad > 0 {
		mvhd = append(mvhd, make([]byte, pad)...)
	}
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], width<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], height<<16)
	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")
	videoTrak := mp4TestBox("trak", append(mp4TestBox("tkhd", tkhd), mp4TestBox("mdia", mp4TestBox("hdlr", videoHdlr))...))
	moov := append(mp4TestBox("mvhd", mvhd), videoTrak...)
	if withAudio {
		audioHdlr := make([]byte, 24)
		copy(audioHdlr[8:12], "soun")
		moov = append(moov, mp4TestBox("trak", mp4TestBox("mdia", mp4TestBox("hdlr", audioHdlr)))...)
	}
	return append(tinyMP4(), mp4TestBox("moov", moov)...)
}

// compositionConversationFixture writes conv_comp with two video artifacts
// (one silent), one WAV, and an image whose file is deliberately absent.
func compositionConversationFixture(t *testing.T, storage ConfigStorage) string {
	t.Helper()
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_comp", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	files := map[string][]byte{
		"vid_a.mp4": compositionMP4Fixture(8, 1280, 720, true),
		"vid_b.mp4": compositionMP4Fixture(4, 640, 480, false),
		"aud_a.wav": buildWAV(16000), // 2s at the 8000 byte/s fixture rate
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(artifacts, name), data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeSearchConversation(t, storage, "2026/10/conv_comp",
		searchConversationFixture("conv_comp", "Clips", "2026-10-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "video", ArtifactID: "vid_a", Path: "artifacts/vid_a.mp4", MimeType: "video/mp4"}),
		searchTurnFixture("turn_000002", "assistant", "2026-10-01T10:05:00Z",
			HistoryContent{Type: "video", ArtifactID: "vid_b", Path: "artifacts/vid_b.mp4", MimeType: "video/mp4"},
			HistoryContent{Type: "audio", ArtifactID: "aud_a", Path: "artifacts/aud_a.wav", MimeType: "audio/wav"},
			HistoryContent{Type: "image", ArtifactID: "img_x", Path: "artifacts/img_x.png", MimeType: "image/png"}),
	)
	return artifacts
}

func testVideoRef(id string, seconds float64, hasAudio bool) CompositionMediaRef {
	return CompositionMediaRef{ArtifactID: id, Path: "/tmp/" + id + ".mp4", Kind: "video", DurationSeconds: seconds, Width: 1280, Height: 720, FPS: 30, HasAudio: hasAudio}
}

func testAudioRef(id string, seconds float64) CompositionMediaRef {
	return CompositionMediaRef{ArtifactID: id, Path: "/tmp/" + id + ".wav", Kind: "audio", DurationSeconds: seconds, HasAudio: true}
}

func TestCompositionArtifactExtension(t *testing.T) {
	cases := []struct {
		content HistoryContent
		want    string
	}{
		{HistoryContent{Type: "composition", Path: "artifacts/cmp_a.json", MimeType: "application/json"}, ".json"},
		{HistoryContent{Type: "composition", MimeType: "application/json"}, ".json"},
		{HistoryContent{Type: "composition", Path: "artifacts/cmp_a.txt", MimeType: "text/plain"}, ""},
		{HistoryContent{Type: "markdown", Path: "artifacts/md_a.json", MimeType: "application/json"}, ""},
		{HistoryContent{Type: "video", Path: "artifacts/vid_a.json"}, ""},
	}
	for _, tc := range cases {
		if got := compositionArtifactExtension(tc.content); got != tc.want {
			t.Fatalf("compositionArtifactExtension(%+v) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestValidateCompositionDocNormalizes(t *testing.T) {
	doc := CompositionDoc{
		Name:   "  ",
		Output: CompositionOutput{Width: 1281, Height: 721},
		Video: []CompositionClip{
			{ID: "c1", Ref: testVideoRef("vid_1", 10, true), InSeconds: -1, OutSeconds: 10.0005, Audio: CompositionOriginalAudio{GainDb: -99},
				Transition: &CompositionTransition{Kind: "dissolve", Seconds: 4, AudioCrossfade: true}},
			{ID: "c2", Ref: testVideoRef("vid_2", 3, true), InSeconds: 0, OutSeconds: 3,
				Transition: &CompositionTransition{Kind: "dissolve"}},
			{ID: "c3", Ref: testVideoRef("vid_3", 8, true), InSeconds: 0, OutSeconds: 8,
				Transition: &CompositionTransition{Kind: "cut", Seconds: 2}},
			{ID: "c4", Ref: testVideoRef("vid_4", 8, true), InSeconds: 0, OutSeconds: 8,
				Transition: &CompositionTransition{Kind: "dissolve", Seconds: 1}},
		},
		AudioLanes: []CompositionLane{{
			ID: "l1", Name: " Music ", GainDb: 40,
			Segments: []CompositionSegment{
				{ID: "s2", Ref: testAudioRef("aud_2", 4), InSeconds: 0, OutSeconds: 4, StartSeconds: 10, FadeInSeconds: 3, FadeOutSeconds: -1},
				{ID: "s1", Ref: testAudioRef("aud_1", 6), InSeconds: 0, OutSeconds: 6, StartSeconds: 0, GainDb: -6,
					Ducks: []CompositionDuck{{StartSeconds: 4, EndSeconds: 9, GainDb: -12}, {StartSeconds: 1, EndSeconds: 2, GainDb: -80}}},
			},
		}},
	}
	if err := validateCompositionDoc(&doc); err != nil {
		t.Fatalf("validateCompositionDoc returned error: %v", err)
	}
	if doc.Name != compositionDefaultName || doc.SchemaVersion != compositionSchemaVersion {
		t.Fatalf("name/schema = %q/%d", doc.Name, doc.SchemaVersion)
	}
	if doc.Output.Fit != compositionFitPad || doc.Output.Width != 1280 || doc.Output.Height != 720 {
		t.Fatalf("output = %+v, want pad default and even dimensions", doc.Output)
	}
	c1 := doc.Video[0]
	if c1.InSeconds != 0 || c1.OutSeconds != 10 || c1.Audio.GainDb != compositionGainMinDb {
		t.Fatalf("clip 1 = in %g out %g gain %g, want clamped range and gain", c1.InSeconds, c1.OutSeconds, c1.Audio.GainDb)
	}
	// A 4s dissolve between a 10s and a 3s clip clamps to half the shorter
	// neighbor (1.5s); the audio crossfade flag rides along.
	if c1.Transition == nil || c1.Transition.Seconds != 1.5 || !c1.Transition.AudioCrossfade {
		t.Fatalf("clip 1 transition = %+v, want a 1.5s dissolve with audio crossfade", c1.Transition)
	}
	// A dissolve with no length takes the default (1s fits between 3s and 8s).
	if c2 := doc.Video[1]; c2.Transition == nil || c2.Transition.Seconds != compositionTransitionDefaultSeconds {
		t.Fatalf("clip 2 transition = %+v, want the default dissolve", c2.Transition)
	}
	// A cut normalizes to nil regardless of stray seconds; the last clip's
	// transition is dropped (nothing follows it).
	if doc.Video[2].Transition != nil || doc.Video[3].Transition != nil {
		t.Fatalf("clips 3/4 transitions = %+v / %+v, want nil", doc.Video[2].Transition, doc.Video[3].Transition)
	}
	lane := doc.AudioLanes[0]
	if lane.Name != "Music" || lane.GainDb != compositionGainMaxDb {
		t.Fatalf("lane = %+v, want trimmed name and clamped gain", lane)
	}
	if len(lane.Segments) != 2 || lane.Segments[0].ID != "s1" || lane.Segments[1].ID != "s2" {
		t.Fatalf("segments = %+v, want sorted by start", lane.Segments)
	}
	s1 := lane.Segments[0]
	if len(s1.Ducks) != 2 || s1.Ducks[0].StartSeconds != 1 || s1.Ducks[0].GainDb != compositionGainMinDb || s1.Ducks[1].EndSeconds != 6 {
		t.Fatalf("ducks = %+v, want sorted, clamped gain, end bounded to the segment", s1.Ducks)
	}
	s2 := lane.Segments[1]
	if s2.FadeInSeconds != 2 || s2.FadeOutSeconds != 0 {
		t.Fatalf("segment 2 fades = %g/%g, want clamped to half the segment and a negative fade zeroed", s2.FadeInSeconds, s2.FadeOutSeconds)
	}

	// Nil slices normalize to empty arrays so the wire never carries null.
	empty := CompositionDoc{}
	if err := validateCompositionDoc(&empty); err != nil {
		t.Fatalf("empty doc returned error: %v", err)
	}
	if empty.Video == nil || empty.AudioLanes == nil {
		t.Fatal("empty doc kept nil slices")
	}
}

func TestValidateCompositionDocRefusals(t *testing.T) {
	video := func(clips ...CompositionClip) CompositionDoc {
		return CompositionDoc{Video: clips}
	}
	lane := func(segments ...CompositionSegment) CompositionDoc {
		return CompositionDoc{AudioLanes: []CompositionLane{{ID: "l1", Name: "Lane", Segments: segments}}}
	}
	clip := func(id string, ref CompositionMediaRef, in, out float64) CompositionClip {
		return CompositionClip{ID: id, Ref: ref, InSeconds: in, OutSeconds: out}
	}
	segment := func(id string, ref CompositionMediaRef, start float64) CompositionSegment {
		return CompositionSegment{ID: id, Ref: ref, InSeconds: 0, OutSeconds: ref.DurationSeconds, StartSeconds: start}
	}
	tooMany := make([]CompositionClip, compositionMaxClips+1)
	for i := range tooMany {
		tooMany[i] = clip("c"+string(rune('a'+i%26))+string(rune('a'+i/26)), testVideoRef("vid", 5, true), 0, 5)
	}
	withDuck := segment("s1", testAudioRef("aud", 6), 0)
	withDuck.Ducks = []CompositionDuck{{StartSeconds: 0, EndSeconds: 3, GainDb: -6}, {StartSeconds: 2, EndSeconds: 4, GainDb: -6}}
	tinyDuck := segment("s1", testAudioRef("aud", 6), 0)
	tinyDuck.Ducks = []CompositionDuck{{StartSeconds: 1, EndSeconds: 1.0001, GainDb: -6}}
	cases := []struct {
		name string
		doc  CompositionDoc
		want string
	}{
		{"too many clips", video(tooMany...), "at most 50 clips"},
		{"too many lanes", CompositionDoc{AudioLanes: make([]CompositionLane, compositionMaxAudioLanes+1)}, "at most 4 audio lanes"},
		{"unknown fit", CompositionDoc{Output: CompositionOutput{Fit: "stretch"}}, "output fit must be"},
		{"negative size", CompositionDoc{Output: CompositionOutput{Width: -2}}, "cannot be negative"},
		{"fps too high", CompositionDoc{Output: CompositionOutput{FPS: 120}}, "output fps must be"},
		{"empty id", video(clip("", testVideoRef("vid", 5, true), 0, 5)), "empty id"},
		{"duplicate id", video(clip("c1", testVideoRef("vid", 5, true), 0, 5), clip("c1", testVideoRef("vid", 5, true), 0, 5)), "reuses id"},
		{"audio on video lane", video(clip("c1", testAudioRef("aud", 5), 0, 5)), "video lane holds video clips"},
		{"unmeasured clip", video(clip("c1", testVideoRef("vid", 0, true), 0, 5)), "never measured"},
		{"range too short", video(clip("c1", testVideoRef("vid", 5, true), 2, 2.001)), "too short"},
		{"range not finite", video(clip("c1", testVideoRef("vid", 5, true), math.NaN(), 5)), "non-finite"},
		{"unknown transition", video(CompositionClip{ID: "c1", Ref: testVideoRef("vid", 5, true), OutSeconds: 5, Transition: &CompositionTransition{Kind: "wipe"}}, clip("c2", testVideoRef("vid2", 5, true), 0, 5)), "must be \"cut\" or \"dissolve\""},
		{"dissolve does not fit", video(CompositionClip{ID: "c1", Ref: testVideoRef("vid", 5, true), OutSeconds: 0.15, Transition: &CompositionTransition{Kind: "dissolve", Seconds: 1}}, clip("c2", testVideoRef("vid2", 5, true), 0, 5)), "does not fit"},
		{"segment overlap", lane(segment("s1", testAudioRef("aud", 6), 0), segment("s2", testAudioRef("aud2", 6), 3)), "overlaps the previous segment"},
		{"segment negative start", lane(segment("s1", testAudioRef("aud", 6), -1)), "before the composition begins"},
		{"image on lane", lane(segment("s1", CompositionMediaRef{ArtifactID: "img", Kind: "image", DurationSeconds: 1, Path: "/tmp/img.png"}, 0)), "audio lanes hold audio"},
		{"silent video on lane", lane(segment("s1", testVideoRef("vid", 6, false), 0)), "without an audio track"},
		{"duck overlap", lane(withDuck), "overlaps the previous duck"},
		{"duck too short", lane(tinyDuck), "too short to hear"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			err := validateCompositionDoc(&doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateCompositionDoc error = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	// A video ref WITH audio is legal on a lane — that is detached audio.
	detached := lane(segment("s1", testVideoRef("vid", 6, true), 0))
	if err := validateCompositionDoc(&detached); err != nil {
		t.Fatalf("a video ref with audio on a lane must validate, got %v", err)
	}
}

func TestCompositionClipLayout(t *testing.T) {
	doc := CompositionDoc{Video: []CompositionClip{
		{ID: "c1", Ref: testVideoRef("a", 10, true), InSeconds: 0, OutSeconds: 10, Transition: &CompositionTransition{Kind: "dissolve", Seconds: 1}},
		{ID: "c2", Ref: testVideoRef("b", 20, true), InSeconds: 5, OutSeconds: 10},
		{ID: "c3", Ref: testVideoRef("c", 8, true), InSeconds: 0, OutSeconds: 8},
	}}
	starts, duration := compositionClipLayout(doc)
	if len(starts) != 3 || starts[0] != 0 || starts[1] != 9 || starts[2] != 14 {
		t.Fatalf("starts = %v, want [0 9 14]", starts)
	}
	if duration != 22 {
		t.Fatalf("duration = %g, want 22 (10+5+8 minus the 1s dissolve)", duration)
	}
	if got := compositionTimelineDuration(CompositionDoc{}); got != 0 {
		t.Fatalf("empty duration = %g", got)
	}
	if summary := compositionDocSummary(doc); !strings.Contains(summary, "3 clips") || !strings.Contains(summary, "no audio lanes") || !strings.Contains(summary, "22s") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestSeedCompositionDoc(t *testing.T) {
	refs := []CompositionMediaRef{
		testAudioRef("aud_1", 2),
		testVideoRef("vid_1", 8, true),
		testAudioRef("aud_2", 3),
		testVideoRef("vid_2", 4, false),
	}
	doc := seedCompositionDoc("", refs)
	if doc.Name != compositionDefaultName || doc.Output.Fit != compositionFitPad {
		t.Fatalf("doc = name %q fit %q", doc.Name, doc.Output.Fit)
	}
	if len(doc.Video) != 2 || doc.Video[0].Ref.ArtifactID != "vid_1" || doc.Video[1].Ref.ArtifactID != "vid_2" {
		t.Fatalf("video = %+v, want vid_1 then vid_2 in reference order", doc.Video)
	}
	if doc.Video[0].OutSeconds != 8 || doc.Video[0].Transition != nil || !strings.HasPrefix(doc.Video[0].ID, "clp_") {
		t.Fatalf("clip 1 = %+v, want its full range, no transition, a clp_ id", doc.Video[0])
	}
	if len(doc.AudioLanes) != 1 || len(doc.AudioLanes[0].Segments) != 2 {
		t.Fatalf("lanes = %+v, want one lane with two segments", doc.AudioLanes)
	}
	segments := doc.AudioLanes[0].Segments
	if segments[0].Ref.ArtifactID != "aud_1" || segments[0].StartSeconds != 0 || segments[1].Ref.ArtifactID != "aud_2" || segments[1].StartSeconds != 2 {
		t.Fatalf("segments = %+v, want aud_1 at 0 then aud_2 queued at 2", segments)
	}
	if err := validateCompositionDoc(&doc); err != nil {
		t.Fatalf("a seeded doc must validate, got %v", err)
	}
	if empty := seedCompositionDoc("Trailer", nil); empty.Name != "Trailer" || len(empty.Video) != 0 || empty.Video == nil || empty.AudioLanes == nil {
		t.Fatalf("empty seed = %+v", empty)
	}
}

func TestResolveCompositionMediaMeasuresWithoutFFprobe(t *testing.T) {
	config := editTestHome(t)
	artifacts := compositionConversationFixture(t, config.Storage)
	app := NewApp()

	ref, err := app.ResolveCompositionMedia("conv_comp", "vid_a")
	if err != nil {
		t.Fatalf("ResolveCompositionMedia(vid_a) returned error: %v", err)
	}
	if ref.Kind != "video" || ref.DurationSeconds != 8 || ref.Width != 1280 || ref.Height != 720 || !ref.HasAudio || ref.FPS != joinTransitionsDefaultFPS {
		t.Fatalf("vid_a ref = %+v, want the sniffed 8s 1280x720 clip with audio at the default fps", ref)
	}
	if !filepath.IsAbs(ref.Path) || ref.ConversationID != "conv_comp" || ref.MimeType != "video/mp4" {
		t.Fatalf("vid_a identity = %+v, want an absolute path on conv_comp", ref)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatalf("ref path %q does not exist: %v", ref.Path, err)
	}
	silent, err := app.ResolveCompositionMedia("conv_comp", "vid_b")
	if err != nil || silent.HasAudio || silent.DurationSeconds != 4 || silent.Width != 640 {
		t.Fatalf("vid_b = %+v (err %v), want a silent 4s 640-wide clip", silent, err)
	}
	audio, err := app.ResolveCompositionMedia("conv_comp", "aud_a")
	if err != nil || audio.Kind != "audio" || audio.DurationSeconds != 2 || !audio.HasAudio {
		t.Fatalf("aud_a = %+v (err %v), want a 2s audio ref", audio, err)
	}

	if _, err := app.ResolveCompositionMedia("conv_comp", "img_x"); err == nil || !strings.Contains(err.Error(), "image media") {
		t.Fatalf("an image must be refused naming its kind, got %v", err)
	}
	if _, err := app.ResolveCompositionMedia("conv_comp", "vid_nope"); err == nil || !strings.Contains(err.Error(), "no video or audio asset") {
		t.Fatalf("an unknown id must be refused, got %v", err)
	}
	if _, err := app.ResolveCompositionMedia("", "vid_a"); err == nil {
		t.Fatal("an empty conversation id must be refused")
	}
	if err := os.Remove(filepath.Join(artifacts, "vid_b.mp4")); err != nil {
		t.Fatalf("remove vid_b: %v", err)
	}
	if _, err := app.ResolveCompositionMedia("conv_comp", "vid_b"); err == nil || !strings.Contains(err.Error(), "missing on disk") {
		t.Fatalf("a missing file must be refused at add time, got %v", err)
	}
}

func TestResolveCompositionMediaWidensToLibrary(t *testing.T) {
	config := editTestHome(t)
	if err := ensureStorageDirs(config.Storage); err != nil {
		t.Fatalf("ensureStorageDirs: %v", err)
	}
	library, err := createLibrary(config.Storage, "Films")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	project, err := createProject(config.Storage, library.ID, "Trailer")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	// conv_a owns the composition and has no media; conv_b, a project
	// sibling, holds the clip.
	writeSearchConversation(t, config.Storage, "2026/10/conv_a",
		libraryConversationFixture("conv_a", "Composer chat", "2026-10-02T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-10-02T10:00:00Z", HistoryContent{Type: "text", Text: "hello"}),
	)
	siblingArtifacts := filepath.Join(config.Storage.History, "conversations", "2026/10/conv_b", "artifacts")
	if err := os.MkdirAll(siblingArtifacts, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siblingArtifacts, "vid_lib.mp4"), compositionMP4Fixture(6, 1920, 1080, true), 0644); err != nil {
		t.Fatalf("write vid_lib: %v", err)
	}
	writeSearchConversation(t, config.Storage, "2026/10/conv_b",
		libraryConversationFixture("conv_b", "Footage", "2026-10-01T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "video", ArtifactID: "vid_lib", Path: "artifacts/vid_lib.mp4", MimeType: "video/mp4"}),
	)
	app := NewApp()
	ref, err := app.ResolveCompositionMedia("conv_a", "vid_lib")
	if err != nil {
		t.Fatalf("ResolveCompositionMedia across the library returned error: %v", err)
	}
	if ref.ConversationID != "conv_b" || ref.DurationSeconds != 6 || ref.Width != 1920 || !filepath.IsAbs(ref.Path) {
		t.Fatalf("library ref = %+v, want conv_b's 6s 1920-wide clip by absolute path", ref)
	}
	if !strings.Contains(ref.Path, "conv_b") {
		t.Fatalf("library ref path %q should point into conv_b's artifacts", ref.Path)
	}
}

func TestSeedCompositionSkipsUnresolvable(t *testing.T) {
	config := editTestHome(t)
	compositionConversationFixture(t, config.Storage)
	app := NewApp()

	info, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"aud_a", "vid_bogus", " ", "vid_a", "img_x"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	if info.ArtifactID != "" || info.URL != "" || info.ConversationTitle != "Clips" || info.ConversationID != "conv_comp" {
		t.Fatalf("seed identity = %+v, want an unsaved draft on conv_comp", info)
	}
	if len(info.Doc.Video) != 1 || info.Doc.Video[0].Ref.ArtifactID != "vid_a" || info.Doc.Video[0].OutSeconds != 8 {
		t.Fatalf("seeded video = %+v", info.Doc.Video)
	}
	if len(info.Doc.AudioLanes) != 1 || len(info.Doc.AudioLanes[0].Segments) != 1 || info.Doc.AudioLanes[0].Segments[0].Ref.ArtifactID != "aud_a" {
		t.Fatalf("seeded lanes = %+v", info.Doc.AudioLanes)
	}
	if len(info.Warnings) != 2 || !strings.Contains(info.Warnings[0], "vid_bogus") || !strings.Contains(info.Warnings[1], "img_x") {
		t.Fatalf("warnings = %v, want one per skipped id in order", info.Warnings)
	}
	if info.MissingRefs == nil || len(info.MissingRefs) != 0 {
		t.Fatalf("missing refs = %v, want an empty array", info.MissingRefs)
	}
	empty, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", Name: "Blank"})
	if err != nil || empty.Doc.Name != "Blank" || len(empty.Doc.Video) != 0 || len(empty.Warnings) != 0 {
		t.Fatalf("empty seed = %+v (err %v)", empty, err)
	}
	if _, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_missing"}); err == nil {
		t.Fatal("an unknown conversation must fail the seed")
	}
}

func TestSaveCompositionEditAndResolve(t *testing.T) {
	config := editTestHome(t)
	artifacts := compositionConversationFixture(t, config.Storage)
	app := NewApp()

	seed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"vid_a", "vid_b", "aud_a"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	doc := seed.Doc
	doc.Name = "Trailer"
	doc.Video[0].Transition = &CompositionTransition{Kind: "dissolve", Seconds: 1, AudioCrossfade: true}
	result, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: doc})
	if err != nil {
		t.Fatalf("SaveCompositionEdit returned error: %v", err)
	}
	if !strings.HasPrefix(result.ArtifactID, "cmp_") || !strings.HasSuffix(result.URL, ".json") || !strings.HasPrefix(result.URL, artifactPrefix) {
		t.Fatalf("result identity = %+v", result)
	}
	if result.Doc.Name != "Trailer" || !strings.Contains(result.Summary, "Trailer") || !strings.Contains(result.Summary, "2 clips") || !strings.Contains(result.Summary, "1 audio lane") {
		t.Fatalf("result = name %q summary %q", result.Doc.Name, result.Summary)
	}

	detail, err := getConversation(config.Storage, "conv_comp")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Stats.TurnCount != 1 || detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("stats = %+v, want the save's increments", detail.Conversation.Stats)
	}
	saveTurn := detail.Turns[len(detail.Turns)-1]
	if saveTurn.Role != "assistant" || len(saveTurn.Content) != 2 || saveTurn.Content[1].Type != "composition" || saveTurn.Content[1].ArtifactID != result.ArtifactID {
		t.Fatalf("save turn = %+v, want an assistant turn with text + composition", saveTurn)
	}
	if !strings.HasPrefix(saveTurn.Content[1].Text, artifactPrefix) {
		t.Fatalf("composition entry was not hydrated to an artifact URL: %q", saveTurn.Content[1].Text)
	}
	assets := conversationAssetsFromDetail(detail)
	var compositionAsset *ConversationAsset
	for i := range assets {
		if assets[i].Kind == "composition" {
			compositionAsset = &assets[i]
		}
	}
	if compositionAsset == nil || compositionAsset.ID != result.ArtifactID || compositionAsset.MimeType != "application/json" || compositionAsset.URL == "" {
		t.Fatalf("composition asset = %+v, want the saved revision listed with its URL", compositionAsset)
	}

	// Reopening the revision round-trips the normalized document.
	source, err := app.ResolveCompositionEditSource("conv_comp", result.ArtifactID)
	if err != nil {
		t.Fatalf("ResolveCompositionEditSource returned error: %v", err)
	}
	if source.ArtifactID != result.ArtifactID || source.URL != result.URL || source.ConversationTitle != "Clips" || source.MimeType != "application/json" {
		t.Fatalf("source identity = %+v", source)
	}
	if source.Doc.Name != "Trailer" || len(source.Doc.Video) != 2 || source.Doc.Video[0].Transition == nil || source.Doc.Video[0].Transition.Seconds != 1 || !source.Doc.Video[0].Transition.AudioCrossfade {
		t.Fatalf("reopened doc = %+v", source.Doc)
	}
	if source.Doc.Video[1].Transition != nil || len(source.Doc.AudioLanes) != 1 || source.Doc.AudioLanes[0].Segments[0].Ducks == nil {
		t.Fatalf("reopened doc lanes/transitions = %+v", source.Doc)
	}
	if len(source.MissingRefs) != 0 || source.Warnings == nil {
		t.Fatalf("missing/warnings = %v / %v", source.MissingRefs, source.Warnings)
	}
	// A source whose file vanished is identified, not dropped.
	if err := os.Remove(filepath.Join(artifacts, "vid_b.mp4")); err != nil {
		t.Fatalf("remove vid_b: %v", err)
	}
	source, err = app.ResolveCompositionEditSource("conv_comp", result.ArtifactID)
	if err != nil {
		t.Fatalf("ResolveCompositionEditSource after removal returned error: %v", err)
	}
	if len(source.MissingRefs) != 1 || source.MissingRefs[0] != "vid_b" || len(source.Doc.Video) != 2 {
		t.Fatalf("missing refs = %v with %d clips, want vid_b flagged and the timeline intact", source.MissingRefs, len(source.Doc.Video))
	}

	// Refusals: unknown conversation, unknown artifact, an invalid document,
	// and a conversation that is streaming.
	if _, err := app.ResolveCompositionEditSource("conv_comp", "cmp_nope"); err == nil {
		t.Fatal("an unknown composition artifact must fail")
	}
	if _, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_missing", Doc: doc}); err == nil {
		t.Fatal("an unknown conversation must fail")
	}
	bad := doc
	bad.Video = []CompositionClip{{ID: "x", Ref: testAudioRef("aud_a", 2), OutSeconds: 2}}
	if _, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: bad}); err == nil || !strings.Contains(err.Error(), "video lane holds video clips") {
		t.Fatalf("an invalid document must fail with the rule named, got %v", err)
	}
	app.streamsMu.Lock()
	app.streamConversations["req"] = "conv_comp"
	app.streamsMu.Unlock()
	if _, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: doc}); err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("a streaming conversation must refuse the save, got %v", err)
	}
}
