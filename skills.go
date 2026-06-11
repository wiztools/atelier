package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const skillIndexReadLimit = 32 * 1024

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
	return []string{
		normalizeStoragePath("~/.agents/skills"),
		normalizeStoragePath("~/.atelier/skills"),
	}
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
			key := strings.ToLower(item.Name) + "\x00" + item.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			index = append(index, item)
		}
	}
	sort.Slice(index, func(i, j int) bool {
		if strings.EqualFold(index[i].Name, index[j].Name) {
			return index[i].Path < index[j].Path
		}
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
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return LoadedSkill{}, err
	}
	return LoadedSkill{SkillIndexEntry: entry, Body: string(data)}, nil
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

func decodeSkillSelectionPlan(content string) (skillSelectionPlan, error) {
	for _, candidate := range harnessJSONCandidates(content) {
		var plan skillSelectionPlan
		if err := json.Unmarshal([]byte(candidate), &plan); err == nil {
			return plan, nil
		}
	}
	return skillSelectionPlan{}, errors.New("no valid skill selection JSON found")
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
