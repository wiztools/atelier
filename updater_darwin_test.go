//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "/Applications/Atelier.app", want: `'/Applications/Atelier.app'`},
		{input: "/tmp/My Staging Dir/Atelier.app", want: `'/tmp/My Staging Dir/Atelier.app'`},
		{input: "it's", want: `'it'\''s'`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.input); got != tt.want {
			t.Fatalf("shellQuote(%q) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestParseTeamIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "developer id output",
			input: `Executable=/Applications/Atelier.app/Contents/MacOS/Atelier
Identifier=com.wails.Atelier
Format=app bundle with Mach-O universal (x86_64 arm64)
CodeDirectory v=20400 size=1234 flags=0x2(runtime) hashes=1234+5
TeamIdentifier=WIZTOOLS123
Timestamp=1 Jan 2026`,
			want: "WIZTOOLS123",
		},
		{name: "ad hoc", input: "TeamIdentifier=not set\n", want: ""},
		{name: "missing", input: "Identifier=com.wails.Atelier\n", want: ""},
		{name: "empty output", input: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTeamIdentifier(tt.input); got != tt.want {
				t.Fatalf("parseTeamIdentifier = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildInstallHelperScript(t *testing.T) {
	staging := "/tmp/staging dir 'quoted'"
	staged := staging + "/Atelier.app"
	target := "/Applications/Atelier.app"
	script := buildInstallHelperScript(staging, staged, target, 4242)

	for _, want := range []string{
		"PID=4242",
		shellQuote(target),
		shellQuote(staged),
		shellQuote(staging),
		"previous.app",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}

	// The generated script must be syntactically valid /bin/sh even with
	// hostile paths (spaces, single quotes).
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("generated script fails sh -n: %v: %s", err, out)
	}
}

func TestCurrentAppBundlePathRefusesRawBinary(t *testing.T) {
	// The test binary runs from the go-build cache, never inside a .app, so
	// this pins the development-mode refusal (the wails dev case).
	if _, err := currentAppBundlePath(); err == nil {
		t.Fatal("currentAppBundlePath should refuse a raw (non-bundle) executable")
	}
}

func TestCleanupStaleUpdateStaging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := updateStateDir()
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "staged-123")
	freshFile := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(filepath.Join(stale, "Atelier.app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	cleanupStaleUpdateStaging()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale staging dir should be removed at launch")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatalf("state.json must survive staging cleanup: %v", err)
	}
}

func TestFindStagedAppBundle(t *testing.T) {
	t.Run("single bundle", func(t *testing.T) {
		staging := t.TempDir()
		if err := os.MkdirAll(filepath.Join(staging, "Atelier.app"), 0755); err != nil {
			t.Fatal(err)
		}
		got, err := findStagedAppBundle(staging)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != "Atelier.app" {
			t.Fatalf("found %q, want Atelier.app", got)
		}
	})
	t.Run("no bundle", func(t *testing.T) {
		if _, err := findStagedAppBundle(t.TempDir()); err == nil {
			t.Fatal("want an error when the archive has no .app")
		}
	})
	t.Run("ambiguous bundles", func(t *testing.T) {
		staging := t.TempDir()
		os.MkdirAll(filepath.Join(staging, "A.app"), 0755)
		os.MkdirAll(filepath.Join(staging, "B.app"), 0755)
		if _, err := findStagedAppBundle(staging); err == nil {
			t.Fatal("want an error when the archive has multiple .app bundles")
		}
	})
}
