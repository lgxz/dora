package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewBuildsSortedCatalogAndLoadsSkill(t *testing.T) {
	root := t.TempDir()
	weather := writeSkill(t, root, "weather", `---
name: weather
description: Get a detailed weather forecast.
---

# Weather

Use a reliable forecast source.
`)
	writeSkill(t, root, "system-status", `---
name: system-status
description: Analyze CPU, memory, and disk usage.
---

# System status

Inspect the machine carefully.
`)

	tool, err := New(Config{Directories: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	spec := tool.Spec()
	if spec.Name != "skill" {
		t.Fatalf("name = %q", spec.Name)
	}
	statusIndex := strings.Index(spec.Description, "system-status")
	weatherIndex := strings.Index(spec.Description, "weather")
	if statusIndex < 0 || weatherIndex < 0 || statusIndex > weatherIndex {
		t.Fatalf("description is not sorted: %q", spec.Description)
	}

	content, err := tool.Execute(context.Background(), []byte(`{"name":"weather"}`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(weather)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `Directory: "`+filepath.Dir(weather)+`"`) ||
		!strings.HasSuffix(content, string(want)) {
		t.Fatalf("content = %q", content)
	}
}

func TestNewRejectsDuplicateNames(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	contents := `---
name: duplicate
description: Duplicate skill.
---
Instructions.
`
	writeSkill(t, first, "duplicate", contents)
	writeSkill(t, second, "duplicate", contents)

	_, err := New(Config{Directories: []string{first, second}})
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewStrictlyValidatesFrontMatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "invalid", `---
name: invalid
description: Invalid metadata.
surprise: true
---
Instructions.
`)

	_, err := New(Config{Directories: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsDirectoryNameMismatch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "directory-name", `---
name: another-name
description: Mismatched name.
---
Instructions.
`)

	_, err := New(Config{Directories: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "must match directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteRejectsUnknownSkillAndUnknownInput(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "known", `---
name: known
description: Known skill.
---
Instructions.
`)
	tool, err := New(Config{Directories: []string{root}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tool.Execute(context.Background(), []byte(`{"name":"missing"}`)); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("unknown skill error = %v", err)
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"name":"known","extra":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestNewEnforcesLimits(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one", `---
name: one
description: First skill.
---
Instructions.
`)
	writeSkill(t, root, "two", `---
name: two
description: Second skill.
---
Instructions.
`)
	if _, err := New(Config{Directories: []string{root}, MaxSkills: 1}); err == nil || !strings.Contains(err.Error(), "more than 1") {
		t.Fatalf("skill limit error = %v", err)
	}
	if _, err := New(Config{Directories: []string{root}, MaxFileSize: 8}); err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("file limit error = %v", err)
	}
}

func TestNewReturnsErrNoSkillsForEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "not-a-skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{Directories: []string{root}})
	if !errors.Is(err, ErrNoSkills) {
		t.Fatalf("error = %v", err)
	}
}

func writeSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
