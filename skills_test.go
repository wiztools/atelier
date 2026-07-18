package main

import (
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
