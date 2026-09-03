package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}

	chatsCollapsed, librariesCollapsed := true, true
	state := UIState{Sidebar: SidebarState{
		ChatsCollapsed:     chatsCollapsed,
		LibrariesCollapsed: librariesCollapsed,
		ExpandedLibraries:  []string{"lib_alpha", "lib_beta"},
		ExpandedProjects:   []string{"proj_one"},
	}}
	if err := app.SaveUIState(state); err != nil {
		t.Fatalf("SaveUIState: %v", err)
	}

	loaded := app.LoadUIState()
	if loaded.Sidebar.ChatsCollapsed != chatsCollapsed {
		t.Errorf("ChatsCollapsed = %v, want %v", loaded.Sidebar.ChatsCollapsed, chatsCollapsed)
	}
	if loaded.Sidebar.LibrariesCollapsed != librariesCollapsed {
		t.Errorf("LibrariesCollapsed = %v, want %v", loaded.Sidebar.LibrariesCollapsed, librariesCollapsed)
	}
	if strings.Join(loaded.Sidebar.ExpandedLibraries, ",") != "lib_alpha,lib_beta" {
		t.Errorf("ExpandedLibraries = %v, want [lib_alpha lib_beta]", loaded.Sidebar.ExpandedLibraries)
	}
	if strings.Join(loaded.Sidebar.ExpandedProjects, ",") != "proj_one" {
		t.Errorf("ExpandedProjects = %v, want [proj_one]", loaded.Sidebar.ExpandedProjects)
	}
}

func TestUIStateZeroWhenFileMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}

	if state := app.LoadUIState(); state.Sidebar.ChatsCollapsed || state.Sidebar.LibrariesCollapsed {
		t.Errorf("LoadUIState with no file = %+v, want zero state", state)
	}
}

func TestUIStateZeroWhenFileCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".atelier", "ui-state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app := &App{}
	if state := app.LoadUIState(); state.Sidebar.ChatsCollapsed {
		t.Errorf("LoadUIState with corrupt file = %+v, want zero state", state)
	}
}

func TestUIStateSaveWritesAtomicJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app := &App{}

	if err := app.SaveUIState(UIState{}); err != nil {
		t.Fatalf("SaveUIState: %v", err)
	}
	path := filepath.Join(home, ".atelier", "ui-state.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing after save: %v", err)
	}
	// The empty state omits every field (omitempty) but still writes valid JSON.
	if state := app.LoadUIState(); state.Sidebar.ChatsCollapsed {
		t.Errorf("empty round-trip = %+v, want zero state", state)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind after save: %v", err)
	}
}
