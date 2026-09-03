package main

import (
	"os"
	"path/filepath"
)

// UI state persistence: which sections of the left navigation were collapsed
// and which library/project tree nodes were expanded. The frontend saves on
// every change, so the file is current at any shutdown — clean or otherwise —
// and the next launch restores exactly what was left. It lives in
// ~/.atelier/ui-state.json, deliberately NOT in config.json: the frontend's
// debounced settings auto-save rewrites the whole config and would race or
// wipe this slice (the same reasoning that keeps the updater's state in its
// own file under ~/.atelier/updates).

// UIState is the persisted slice of interface state restored on launch.
type UIState struct {
	Sidebar SidebarState `json:"sidebar"`
}

// SidebarState records the left navigation's collapse state. The two
// top-level sections are inverted "collapsed" flags so the zero value — first
// launch, or a state file written before a section existed — means expanded,
// matching the sidebar's defaults. The tree stores the IDs of expanded
// libraries/projects; an ID whose record no longer exists simply misses on
// lookup and renders collapsed.
type SidebarState struct {
	ChatsCollapsed     bool     `json:"chatsCollapsed,omitempty"`
	LibrariesCollapsed bool     `json:"librariesCollapsed,omitempty"`
	ExpandedLibraries  []string `json:"expandedLibraries,omitempty"`
	ExpandedProjects   []string `json:"expandedProjects,omitempty"`
}

func uiStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier", "ui-state.json"), nil
}

// LoadUIState returns the persisted UI state. A missing or unparsable file is
// not an error — the app opens with fresh defaults rather than failing launch.
func (a *App) LoadUIState() UIState {
	path, err := uiStatePath()
	if err != nil {
		return UIState{}
	}
	var state UIState
	if err := readJSONFile(path, &state); err != nil {
		return UIState{}
	}
	return state
}

// SaveUIState persists the given state with the same atomic write every other
// store uses (temp file + rename).
func (a *App) SaveUIState(state UIState) error {
	path, err := uiStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeJSONFile(path, state)
}
