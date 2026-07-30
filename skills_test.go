package main

import (
	"context"
	"os"
	"path/filepath"
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
