package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	skillIndexReadLimit = 32 * 1024
	skillBodyReadLimit  = 32 * 1024
)

// Skill-selection output budgeting. The selection JSON itself is tiny
// (skillName + reason), but reasoning-style harness models spend a large
// preamble inside the schema's string fields before the object closes; at the
// old 160-token cap every gemma4:e4b-mlx selection was cut mid-JSON and failed
// (conv_f70468e4 and siblings: done_reason "length" at exactly 160 tokens,
// "no valid skill selection JSON found", on every turn). 512 leaves room for
// the preamble; a selection still truncated beyond salvage gets one retry at
// 1024 before the turn proceeds without a skill.
const (
	skillSelectionNumPredict      = 512
	skillSelectionRetryNumPredict = 1024
	skillSelectionMaxAttempts     = 2
)

type SkillIndexEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

type LoadedSkill struct {
	SkillIndexEntry
	Body string `json:"body"`
}

type HarnessSkillDecision struct {
	Selected       bool   `json:"selected"`
	Name           string `json:"name,omitempty"`
	Description    string `json:"description,omitempty"`
	Path           string `json:"path,omitempty"`
	Reason         string `json:"reason,omitempty"`
	AvailableCount int    `json:"availableCount"`
	Error          string `json:"error,omitempty"`
}

type skillSelectionPlan struct {
	SkillName string `json:"skillName"`
	Reason    string `json:"reason"`
}

func defaultSkillRoots() []string {
	// Ordered highest-priority-first: when two roots define a skill of the
	// same name, loadSkillIndex keeps the first one it sees, so this order
	// decides shadowing precedence.
	return []string{
		normalizeStoragePath("~/.atelier/skills"),
		normalizeStoragePath("~/.agents/skills"),
	}
}

// skillRootsFor returns the skill search roots for a turn. The conversation
// workspace's .agents/skills directory is searched first and shadows
// same-named global skills; empty workspace falls back to global roots only.
func skillRootsFor(workspace string) []string {
	roots := defaultSkillRoots()
	ws := normalizeStoragePath(workspace)
	if ws == "" {
		return roots
	}
	return append([]string{filepath.Join(ws, ".agents", "skills")}, roots...)
}

func loadSkillIndex(roots []string) ([]SkillIndexEntry, error) {
	seen := map[string]bool{}
	var index []SkillIndexEntry
	var scanErrors []string
	for _, root := range roots {
		root = normalizeStoragePath(root)
		if strings.TrimSpace(root) == "" {
			continue
		}
		entries, err := skillMarkdownPaths(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			scanErrors = append(scanErrors, err.Error())
			continue
		}
		for _, path := range entries {
			item, err := readSkillIndexEntry(path)
			if err != nil {
				scanErrors = append(scanErrors, err.Error())
				continue
			}
			// Name-only dedup: roots are scanned highest-priority-first, so the
			// first root to define a name wins and shadows any later same-named
			// skill (workspace > ~/.atelier/skills > ~/.agents/skills).
			key := strings.ToLower(item.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			index = append(index, item)
		}
	}
	sort.Slice(index, func(i, j int) bool {
		return strings.ToLower(index[i].Name) < strings.ToLower(index[j].Name)
	})
	if len(scanErrors) > 0 && len(index) == 0 {
		return nil, errors.New(strings.Join(scanErrors, "; "))
	}
	return index, nil
}

func skillMarkdownPaths(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a skill directory", root)
	}
	var paths []string
	if path := filepath.Join(root, "SKILL.md"); fileExists(path) {
		paths = append(paths, path)
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		path := filepath.Join(root, child.Name(), "SKILL.md")
		if fileExists(path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func readSkillIndexEntry(path string) (SkillIndexEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return SkillIndexEntry{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, skillIndexReadLimit))
	if err != nil {
		return SkillIndexEntry{}, err
	}
	meta := parseSkillFrontmatter(string(data))
	name := strings.TrimSpace(meta["name"])
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	description := strings.TrimSpace(meta["description"])
	if name == "" || description == "" {
		return SkillIndexEntry{}, fmt.Errorf("%q is missing required SKILL.md name or description frontmatter", path)
	}
	return SkillIndexEntry{Name: name, Description: description, Path: path}, nil
}

func loadFullSkill(entry SkillIndexEntry) (LoadedSkill, error) {
	file, err := os.Open(entry.Path)
	if err != nil {
		return LoadedSkill{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, skillBodyReadLimit+1))
	if err != nil {
		return LoadedSkill{}, err
	}
	body := string(data)
	if len(data) > skillBodyReadLimit {
		body = string(data[:skillBodyReadLimit]) + "\n\n[SKILL.md truncated: the file exceeds Atelier's skill size limit.]"
	}
	return LoadedSkill{SkillIndexEntry: entry, Body: body}, nil
}

func parseSkillFrontmatter(content string) map[string]string {
	result := map[string]string{}
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return result
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for index := 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			result[key] = value
		}
	}
	return result
}

func findSkillByName(index []SkillIndexEntry, name string) (SkillIndexEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return SkillIndexEntry{}, false
	}
	for _, entry := range index {
		if strings.ToLower(entry.Name) == name {
			return entry, true
		}
	}
	return SkillIndexEntry{}, false
}

func skillSelectionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"skillName", "reason"},
		"properties": map[string]any{
			"skillName": map[string]any{"type": "string"},
			"reason":    map[string]any{"type": "string"},
		},
	}
}

// decodeSkillSelectionPlan parses the harness model's skill-selection JSON. It
// is lenient the way triage is (decodeTriageDecision): mis-typed scalar fields
// are coerced to their JSON text (coerceJSONString), and a response truncated
// by the output token limit is salvaged by regex when a complete "skillName"
// value closed before the cut. Chosen salvage semantics:
//   - a truncation after a complete skillName value is a successful selection
//     (salvaged=true so the caller can note the degradation on the step);
//   - a truncation inside the skillName value is a hard error — the selection
//     is knowably incomplete, and guessing a prefix match would pick the wrong
//     skill (the caller's length-retry gets one more chance at it);
//   - prose or an empty response is a hard error, as before: the model ignored
//     the schema entirely, which deserves a visible failed step.
func decodeSkillSelectionPlan(content string) (skillSelectionPlan, bool, error) {
	candidate := stripJSONFence(content)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
		return salvageSkillSelectionFromTruncation(candidate)
	}
	return skillSelectionPlan{
		SkillName: coerceJSONString(raw["skillName"]),
		Reason:    coerceJSONString(raw["reason"]),
	}, false, nil
}

// salvageSkillSelectionFromTruncation recovers a skill selection from a JSON
// object that was truncated before it closed (the model hit its output token
// limit), mirroring salvageTriageFromTruncation. Only complete, well-formed
// values are kept — a skillName cut off mid-string is an error, never guessed.
// Unquoted keys are tolerated the way the triage salvage regexes tolerate them.
func salvageSkillSelectionFromTruncation(candidate string) (skillSelectionPlan, bool, error) {
	if m := regexp.MustCompile(`"?skillName"?\s*:\s*"([^"]*)"`).FindStringSubmatch(candidate); m != nil {
		plan := skillSelectionPlan{SkillName: m[1]}
		if m := regexp.MustCompile(`"?reason"?\s*:\s*"([^"]*)"`).FindStringSubmatch(candidate); m != nil {
			plan.Reason = m[1]
		}
		return plan, true, nil
	}
	// A skillName that opened but never closed means the truncation fell inside
	// the value itself: the selection is incomplete and unrecoverable.
	if regexp.MustCompile(`"?skillName"?\s*:\s*"[^"]*$`).MatchString(candidate) {
		return skillSelectionPlan{}, false, errors.New("skill selection was truncated before the skill name completed")
	}
	return skillSelectionPlan{}, false, errors.New("no valid skill selection JSON found")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func containsSkillName(text, name string) bool {
	text = strings.ToLower(text)
	name = strings.ToLower(strings.TrimSpace(name))
	if text == "" || name == "" {
		return false
	}
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		if token == name {
			return true
		}
	}
	return false
}
