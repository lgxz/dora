package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileUsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "dora", "config.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestConfigFileFallsBackToLinuxLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "dora", "config.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSessionsDirUsesXDGStateHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)

	path, err := SessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "dora", "sessions")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSessionsDirFallsBackToLinuxLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")

	path, err := SessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "dora", "sessions")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestRejectsRelativeXDGHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")

	_, err := ConfigFile()
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v", err)
	}
}

func TestSkillsDirIsBesideActiveConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "profile", "dora.yaml")

	dir, err := SkillsDir(config)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(config), "skills")
	if dir != want {
		t.Fatalf("directory = %q, want %q", dir, want)
	}
}
