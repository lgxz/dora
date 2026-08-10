package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileUsesDoraHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)

	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "config.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestConfigFileFallsBackToHomeDotDora(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DORA_HOME", "")

	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".dora", "config.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSessionsDirUsesDoraHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)

	path, err := SessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "sessions")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSessionsDirFallsBackToHomeDotDora(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DORA_HOME", "")

	path, err := SessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".dora", "sessions")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestRejectsRelativeDoraHome(t *testing.T) {
	t.Setenv("DORA_HOME", "relative")

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
