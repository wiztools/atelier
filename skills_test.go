package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeSkillMD creates <dir>/<name>/SKILL.md with minimal frontmatter and
// returns the path to the SKILL.md file.
func writeSkillMD(t *testing.T, dir, name, body string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", skillDir, err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: " + name + " skill.\n---\n\n" + body
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", skillPath, err)
	}
	return skillPath
}

func TestSkillRootsForEmptyWorkspaceReturnsGlobalRoots(t *testing.T) {
	roots := skillRootsFor("")
	if len(roots) != 2 {
		t.Fatalf("skillRootsFor(\"\") = %v, want 2 roots", roots)
	}
	want := defaultSkillRoots()
	if roots[0] != want[0] || roots[1] != want[1] {
		t.Fatalf("skillRootsFor(\"\") = %v, want %v", roots, want)
	}
}

func TestSkillRootsForPrependsWorkspaceRootFirst(t *testing.T) {
	tmp := t.TempDir()
	roots := skillRootsFor(tmp)
	if len(roots) != 3 {
		t.Fatalf("skillRootsFor(workspace) = %v, want 3 roots", roots)
	}
	wantFirst := filepath.Join(tmp, ".agents", "skills")
	if roots[0] != wantFirst {
		t.Fatalf("roots[0] = %q, want workspace .agents/skills prepended first", roots[0])
	}
	// Workspace must take precedence (position 0); global roots follow.
	if roots[1] != defaultSkillRoots()[0] || roots[2] != defaultSkillRoots()[1] {
		t.Fatalf("global roots not preserved after workspace root: %v", roots)
	}
}

func TestSkillRootsForIgnoresBlankWorkspace(t *testing.T) {
	for _, ws := range []string{"   ", "\t"} {
		roots := skillRootsFor(ws)
		if len(roots) != 2 {
			t.Fatalf("skillRootsFor(%q) = %v, want global roots only", ws, roots)
		}
	}
}

func TestLoadSkillIndexWorkspaceShadowsGlobal(t *testing.T) {
	workspace := t.TempDir()
	globalA := t.TempDir() // stands in for ~/.atelier/skills
	globalB := t.TempDir() // stands in for ~/.agents/skills

	wsPath := writeSkillMD(t, filepath.Join(workspace, ".agents", "skills"), "shared", "workspace body")
	writeSkillMD(t, globalA, "shared", "atelier body")
	writeSkillMD(t, globalB, "shared", "agents body")

	// Mirror the precedence skillRootsFor produces: workspace first, then
	// ~/.atelier/skills, then ~/.agents/skills.
	roots := []string{
		filepath.Join(workspace, ".agents", "skills"),
		globalA,
		globalB,
	}
	index, err := loadSkillIndex(roots)
	if err != nil {
		t.Fatalf("loadSkillIndex: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("index = %+v, want exactly one shared skill after name dedup", index)
	}
	if index[0].Path != wsPath {
		t.Fatalf("shadowed entry Path = %q, want workspace %q", index[0].Path, wsPath)
	}
	loaded, err := loadFullSkill(index[0])
	if err != nil {
		t.Fatalf("loadFullSkill: %v", err)
	}
	if !strings.Contains(loaded.Body, "workspace body") {
		t.Fatalf("loaded body = %q, want the workspace skill's body", loaded.Body)
	}
}

func TestLoadSkillIndexSkipsMissingWorkspaceSkillDir(t *testing.T) {
	// Workspace set but no .agents/skills directory: loadSkillIndex must treat
	// the missing root as a no-op (os.ErrNotExist) and still return global skills.
	workspace := t.TempDir()
	global := t.TempDir()
	writeSkillMD(t, global, "lonely", "global only")

	roots := []string{
		filepath.Join(workspace, ".agents", "skills"), // does not exist
		global,
	}
	index, err := loadSkillIndex(roots)
	if err != nil {
		t.Fatalf("loadSkillIndex: %v", err)
	}
	if entry, ok := findSkillByName(index, "lonely"); !ok {
		t.Fatalf("index = %+v, want global lonely skill to survive missing workspace root", index)
	} else if entry.Path != filepath.Join(global, "lonely", "SKILL.md") {
		t.Fatalf("entry.Path = %q, want global skill path", entry.Path)
	}
}

// TestSkillGuidesPlannerSuppressesBodyForGenerationModes is the core guard for
// conv_473c1357: the skill selector picked `mediabunny` (a browser audio/video
// metadata library) for an image-generation turn, its body was injected into the
// planner, and the planner then decided no tool call was needed — producing
// zero images. Generation is a built-in tool, not a workflow a SKILL.md guides,
// so a model-selected skill body must not steer the planner in those modes.
func TestSkillGuidesPlannerSuppressesBodyForGenerationModes(t *testing.T) {
	skill := &LoadedSkill{SkillIndexEntry: SkillIndexEntry{Name: "mediabunny"}, Body: "workflow guidance"}
	for _, mode := range []string{"image", "video", "audio"} {
		if skillGuidesPlanner(skill, mode, false) {
			t.Errorf("responseMode=%q: model-selected skill body must be suppressed for generation modes", mode)
		}
	}
	// A skill explicitly named by the user overrides the routing — it always
	// guides the planner, even in a generation mode, because the user asked.
	if !skillGuidesPlanner(skill, "image", true) {
		t.Errorf("user-requested skill must guide the planner even in image mode")
	}
}

// TestSkillGuidesPlannerAllowsBodyForTextAndVision covers the non-generation
// modes, where skills remain valuable: they guide run_command/write_file
// workflows the planner executes.
func TestSkillGuidesPlannerAllowsBodyForTextAndVision(t *testing.T) {
	skill := &LoadedSkill{SkillIndexEntry: SkillIndexEntry{Name: "remotion-render"}, Body: "render steps"}
	for _, mode := range []string{"text", "vision", ""} {
		if !skillGuidesPlanner(skill, mode, false) {
			t.Errorf("responseMode=%q: skill body must guide the planner in non-generation modes", mode)
		}
	}
	if skillGuidesPlanner(nil, "text", false) {
		t.Errorf("nil skill must not guide the planner")
	}
}

// TestPlannerPromptOmitsSkillBodyForImageMode is the end-to-end guard: when
// triage routed to image mode, a selected skill's body must not appear in the
// planner prompt (both the JSON-schema and native paths), so it cannot derail
// the planner into needsTools:false.
func TestPlannerPromptOmitsSkillBodyForImageMode(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	skill := &LoadedSkill{
		SkillIndexEntry: SkillIndexEntry{Name: "mediabunny"},
		Body:            "UNIQUE_SKILL_BODY_MARKER_42",
	}
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "generate an image"}}}

	for name, prompt := range map[string]string{
		"json":   engine.plannerSystemPrompt(registry, req, skill, "", "image", false),
		"native": engine.plannerSystemPromptNative(registry, req, skill, "", "image", false),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(prompt, "UNIQUE_SKILL_BODY_MARKER_42") {
				t.Fatalf("%s planner prompt injected the skill body in image mode — it can only confuse a generation-tool turn", name)
			}
			if strings.Contains(prompt, "Active SKILL.md selected for this turn") {
				t.Fatalf("%s planner prompt still carries the skill-injection preamble in image mode", name)
			}
		})
	}
}

// TestPlannerPromptKeepsUserRequestedSkillBodyInImageMode covers the override:
// a skill the user explicitly named is injected even in a generation mode,
// because the user asked for it.
func TestPlannerPromptKeepsUserRequestedSkillBodyInImageMode(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	skill := &LoadedSkill{
		SkillIndexEntry: SkillIndexEntry{Name: "remotion-create"},
		Body:            "USER_REQUESTED_MARKER_7",
	}
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "/remotion-create a video"}}}
	prompt := engine.plannerSystemPrompt(registry, req, skill, "", "image", true)
	if !strings.Contains(prompt, "USER_REQUESTED_MARKER_7") {
		t.Fatalf("user-requested skill body must be injected even in image mode")
	}
}

// --- Skill selection decode: truncation salvage ---
//
// gemma4:e4b-mlx as the harness model burned its whole 160-token output cap on
// the reasoning preamble, and every "skill" step failed identically with
// done_reason "length" and "no valid skill selection JSON found"
// (conv_f70468e4 and siblings). decodeSkillSelectionPlan now mirrors triage's
// tolerance: mis-typed fields coerce, and a truncation after a complete
// skillName value still selects.

// TestDecodeSkillSelectionPlanParsesValidResponses covers the strict path,
// including the mis-typed-field coercion borrowed from triage.
func TestDecodeSkillSelectionPlanParsesValidResponses(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantName     string
		wantReason   string
		wantSalvaged bool
	}{
		{"valid json", `{"skillName":"memorybank","reason":"store the note"}`, "memorybank", "store the note", false},
		{"fenced json", "```json\n{\"skillName\":\"memorybank\",\"reason\":\"store the note\"}\n```", "memorybank", "store the note", false},
		{"empty skillName means no skill", `{"skillName":"","reason":"nothing applies"}`, "", "nothing applies", false},
		{"missing reason", `{"skillName":"memorybank"}`, "memorybank", "", false},
		{"object reason coerced", `{"skillName":"memorybank","reason":{"why":"note storage"}}`, "memorybank", `{"why":"note storage"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, salvaged, err := decodeSkillSelectionPlan(c.content)
			if err != nil {
				t.Fatalf("decodeSkillSelectionPlan(%q) error: %v", c.content, err)
			}
			if plan.SkillName != c.wantName || plan.Reason != c.wantReason {
				t.Fatalf("plan = %+v, want skillName %q reason %q", plan, c.wantName, c.wantReason)
			}
			if salvaged != c.wantSalvaged {
				t.Fatalf("salvaged = %v, want %v", salvaged, c.wantSalvaged)
			}
		})
	}
}

// TestDecodeSkillSelectionPlanSalvagesTruncationAfterCompleteName is the gemma
// regression: the output budget ran out after the skillName value closed (mid-
// reason is the usual victim), so the selection is recovered rather than
// failing the step.
func TestDecodeSkillSelectionPlanSalvagesTruncationAfterCompleteName(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
	}{
		{"cut mid-reason", `{"skillName":"memorybank","reason":"The user wants to store a note and `, "memorybank"},
		{"cut right after the object's only complete field", `{"skillName":"memorybank"`, "memorybank"},
		{"empty skillName salvaged as no selection", `{"skillName":"","reason":"nothing `, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, salvaged, err := decodeSkillSelectionPlan(c.content)
			if err != nil {
				t.Fatalf("truncation after a complete skillName must be salvaged, got error: %v", err)
			}
			if !salvaged {
				t.Fatalf("salvaged = false, want true for %q", c.content)
			}
			if plan.SkillName != c.wantName {
				t.Fatalf("skillName = %q, want %q", plan.SkillName, c.wantName)
			}
		})
	}
}

// TestDecodeSkillSelectionPlanStillErrorsOnUnrecoverableResponses keeps the
// hard errors: a cut inside the skillName value is knowably incomplete (the
// caller's length-retry gets one more chance), and prose or an empty response
// means the model ignored the schema entirely.
func TestDecodeSkillSelectionPlanStillErrorsOnUnrecoverableResponses(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantErrSub string
	}{
		{"truncated mid-skillName", `{"reason":"long reasoning preamble","skillName":"memo`, "truncated before the skill name"},
		{"pure prose", "I would pick a skill for this turn.", "no valid skill selection JSON"},
		{"empty", "", "no valid skill selection JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := decodeSkillSelectionPlan(c.content)
			if err == nil {
				t.Fatalf("decodeSkillSelectionPlan(%q) must error", c.content)
			}
			if !strings.Contains(err.Error(), c.wantErrSub) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), c.wantErrSub)
			}
		})
	}
}

// --- Skill selection vs length truncation, end to end ---
//
// A mocked Ollama whose skill-selection responses are gemma-style
// length-truncated bodies (done_reason "length", 160-token-era cuts) must not
// produce a failed "skill" step on every turn: a cut after a complete
// skillName salvages into a selection, a cut inside the name triggers the
// single larger-cap retry, and an exhausted retry still fails soft — the turn
// always completes.

// skillTurnTransport serves one chat turn against a mocked Ollama, answering
// the skill-selection calls from scripted bodies and routing triage/planning/
// final responses to fixed replies. It records the num_predict of every
// skill-selection request so tests can pin the caps.
type skillTurnTransport struct {
	skillResponses []struct {
		content    string
		doneReason string
	}
	skillNumPredicts []float64
	nonStreamCalls   int
	t                *testing.T
}

func (tr *skillTurnTransport) roundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/api/chat" {
		return notFoundResponse(), nil
	}
	var payload map[string]any
	data, _ := io.ReadAll(req.Body)
	if err := json.Unmarshal(data, &payload); err != nil {
		tr.t.Fatalf("provider request body is not JSON: %v", err)
	}
	if payload["stream"] == false {
		tr.nonStreamCalls++
		messages, _ := payload["messages"].([]any)
		first, _ := messages[0].(map[string]any)
		system, _ := first["content"].(string)
		body, doneReason := "```json\n{\"brief\":\"Follow the skill.\",\"needsTools\":false,\"reason\":\"Skill guidance is enough.\",\"toolCalls\":[]}\n```", "stop"
		switch {
		case strings.Contains(system, "private skill selector"):
			if len(tr.skillResponses) == 0 {
				tr.t.Fatalf("unexpected skill-selection call #%d", tr.nonStreamCalls)
			}
			scripted := tr.skillResponses[0]
			tr.skillResponses = tr.skillResponses[1:]
			body, doneReason = scripted.content, scripted.doneReason
			options, _ := payload["options"].(map[string]any)
			tr.skillNumPredicts = append(tr.skillNumPredicts, options["num_predict"].(float64))
		case strings.Contains(system, "You are Atelier's harness model"):
			body = `{"needsTools":true,"responseMode":"text","toolTask":"Store the note.","reason":"workspace action"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"` + doneReason + `","eval_count":3,"prompt_eval_count":946}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	stream := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Note stored."},"done":false}`) +
		fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":4}`)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
	}, nil
}

func TestHarnessSkillSelectionSurvivesLengthTruncation(t *testing.T) {
	cases := []struct {
		name           string
		skillResponses []struct {
			content    string
			doneReason string
		}
		wantSkillNumPredicts []float64
		wantSkillSteps       int
		wantLastSkillStatus  string
		wantLastDecision     string
		wantSelected         bool
		wantSkillError       string
		wantFirstSummary     string
	}{
		{
			name: "cut after complete skillName salvages without a retry",
			skillResponses: []struct {
				content    string
				doneReason string
			}{{`{"skillName":"memorybank","reason":"The user wants to store a note and `, "length"}},
			wantSkillNumPredicts: []float64{512},
			wantSkillSteps:       1,
			wantLastSkillStatus:  "completed",
			wantLastDecision:     "selected: memorybank",
			wantSelected:         true,
		},
		{
			name: "cut inside skillName retries once at a larger cap",
			skillResponses: []struct {
				content    string
				doneReason string
			}{
				{`{"reason":"The user wants to store a note and so the right choice is","skillName":"memo`, "length"},
				{`{"skillName":"memorybank","reason":"store the note"}`, "stop"},
			},
			wantSkillNumPredicts: []float64{512, 1024},
			wantSkillSteps:       2,
			wantLastSkillStatus:  "completed",
			wantLastDecision:     "selected: memorybank",
			wantSelected:         true,
			wantFirstSummary:     "retrying with a larger cap",
		},
		{
			name: "exhausted retry fails soft and the turn still completes",
			skillResponses: []struct {
				content    string
				doneReason string
			}{
				{`{"reason":"preamble one","skillName":"memo`, "length"},
				{`{"reason":"preamble two","skillName":"memo`, "length"},
			},
			wantSkillNumPredicts: []float64{512, 1024},
			wantSkillSteps:       2,
			wantLastSkillStatus:  "failed",
			wantSkillError:       "truncated before the skill name",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeSkillMD(t, filepath.Join(home, ".agents", "skills"), "memorybank", "Store notes via notesctl.")

			config := defaultAppConfig()
			config.Storage = ConfigStorage{
				Root:      filepath.Join(home, ".atelier"),
				History:   filepath.Join(home, ".atelier", "history"),
				Artifacts: filepath.Join(home, ".atelier", "history"),
			}
			config.Providers.Ollama.BaseURL = "http://ollama.test"
			config.Providers.Ollama.Models.Primary = "chat-box-model"
			config.Providers.Ollama.Models.Harness = "harness-model"
			if err := writeAppConfig(config); err != nil {
				t.Fatalf("writeAppConfig returned error: %v", err)
			}

			app := NewApp()
			transport := &skillTurnTransport{t: t, skillResponses: c.skillResponses}
			app.client.Transport = roundTripFunc(transport.roundTrip)

			app.runChatStream(context.Background(), "request-skill-trunc", ChatRequest{
				BaseURL: "http://ollama.test",
				Model:   "chat-box-model",
				Messages: []ChatMessage{
					{Role: "user", Content: "Please remember this note for later"},
				},
			})

			if got := persistedAssistantTurn(t, config, 1).Content[0].Text; got != "Note stored." {
				t.Fatalf("assistant content = %q, want the turn to complete without a skill selection failure", got)
			}
			if len(transport.skillNumPredicts) != len(c.wantSkillNumPredicts) {
				t.Fatalf("skill-selection calls = %d (num_predict %v), want %d", len(transport.skillNumPredicts), transport.skillNumPredicts, len(c.wantSkillNumPredicts))
			}
			for i, want := range c.wantSkillNumPredicts {
				if transport.skillNumPredicts[i] != want {
					t.Fatalf("skill call #%d num_predict = %v, want %v", i+1, transport.skillNumPredicts[i], want)
				}
			}
			// One provider call per recorded "skill" step: the retry adds a call
			// AND a step, never one without the other.
			wantNonStream := 2 + c.wantSkillSteps // triage + planning + skill attempts
			if transport.nonStreamCalls != wantNonStream {
				t.Fatalf("non-stream provider calls = %d, want %d (no hidden calls beyond the recorded steps)", transport.nonStreamCalls, wantNonStream)
			}

			run := persistedHarnessRun(t, config, 1)
			steps, _ := run["steps"].([]any)
			skillSteps := harnessStepsByKind(t, steps, "skill")
			if len(skillSteps) != c.wantSkillSteps {
				t.Fatalf("skill steps = %d, want %d", len(skillSteps), c.wantSkillSteps)
			}
			last := skillSteps[len(skillSteps)-1]
			if last["status"] != c.wantLastSkillStatus {
				t.Fatalf("last skill step status = %v, want %q", last["status"], c.wantLastSkillStatus)
			}
			if decision, _ := last["decision"].(string); decision != c.wantLastDecision {
				t.Fatalf("last skill step decision = %q, want %q", decision, c.wantLastDecision)
			}
			if c.wantFirstSummary != "" {
				if summary, _ := skillSteps[0]["summary"].(string); !strings.Contains(summary, c.wantFirstSummary) {
					t.Fatalf("first skill step summary = %q, want it to mention %q", summary, c.wantFirstSummary)
				}
			}
			if c.wantSkillSteps > 1 {
				if iter := skillSteps[0]["iteration"]; iter != float64(1) {
					t.Fatalf("first skill step iteration = %v, want 1", iter)
				}
				if iter := last["iteration"]; iter != float64(2) {
					t.Fatalf("retry skill step iteration = %v, want 2", iter)
				}
			}
			decision, _ := run["skill"].(map[string]any)
			if decision == nil {
				t.Fatalf("harness run missing skill decision: %+v", run["skill"])
			}
			if decision["selected"] != c.wantSelected {
				t.Fatalf("skill decision selected = %v, want %v", decision["selected"], c.wantSelected)
			}
			if c.wantSelected && decision["name"] != "memorybank" {
				t.Fatalf("skill decision name = %v, want memorybank", decision["name"])
			}
			if errText, _ := decision["error"].(string); c.wantSkillError != "" && !strings.Contains(errText, c.wantSkillError) {
				t.Fatalf("skill decision error = %q, want it to mention %q", errText, c.wantSkillError)
			}
		})
	}
}
