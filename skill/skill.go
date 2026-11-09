// Package skill exposes local SKILL.md instruction packages as one dora.Tool.
package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lgxz/dora"
	"gopkg.in/yaml.v3"
)

const (
	defaultMaxSkills   = 64
	defaultMaxFileSize = 256 << 10
	maxDescriptionSize = 1024
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ErrNoSkills indicates that the configured directories contain no SKILL.md
// packages. Callers may use this to leave skill support disabled.
var ErrNoSkills = errors.New("skill: no skills found")

// Config controls local skill discovery and resource limits. Zero limits use
// conservative defaults.
type Config struct {
	Directories []string
	MaxSkills   int
	MaxFileSize int64
}

// tool loads one discovered skill's instructions by name.
type tool struct {
	spec    dora.ToolSpec
	catalog string
	skills  map[string]loadedSkill
}

// toolDescription is fixed on purpose: the tool definitions are part of the
// request prefix that providers and local servers cache, so the discovered
// skills are listed separately by Catalog instead of in this description.
const toolDescription = "Load the full instructions of one skill by name. The available skills and what they are for are listed in the context at the start of the conversation."

// Catalog returns the model-visible list of the skills a tool made by New can
// load, one per line in name order. It is empty for any other tool. Callers
// must place it in the model's context, for example Agent context, because the
// tool description no longer carries it.
func Catalog(t dora.Tool) string {
	if loaded, ok := t.(*tool); ok && loaded != nil {
		return loaded.catalog
	}
	return ""
}

type metadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type entry struct {
	name        string
	description string
	directory   string
	content     string
}

type loadedSkill struct {
	directory string
	content   string
}

// New discovers and validates every immediate child directory containing a
// SKILL.md. Skill directory names must match their front matter names.
func New(cfg Config) (dora.Tool, error) {
	if len(cfg.Directories) == 0 {
		return nil, ErrNoSkills
	}
	if cfg.MaxSkills < 0 {
		return nil, errors.New("skill: MaxSkills cannot be negative")
	}
	if cfg.MaxFileSize < 0 {
		return nil, errors.New("skill: MaxFileSize cannot be negative")
	}
	maxSkills := cfg.MaxSkills
	if maxSkills == 0 {
		maxSkills = defaultMaxSkills
	}
	maxFileSize := cfg.MaxFileSize
	if maxFileSize == 0 {
		maxFileSize = defaultMaxFileSize
	}

	entries := make([]entry, 0)
	seen := make(map[string]string)
	for _, directory := range cfg.Directories {
		if directory == "" {
			return nil, errors.New("skill: directory is empty")
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, fmt.Errorf("skill: resolve directory %q: %w", directory, err)
		}
		children, err := os.ReadDir(absolute)
		if err != nil {
			return nil, fmt.Errorf("skill: read directory %q: %w", absolute, err)
		}
		for _, child := range children {
			if !child.IsDir() {
				continue
			}
			path := filepath.Join(absolute, child.Name(), "SKILL.md")
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("skill: inspect %q: %w", path, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("skill: %q must be a regular file", path)
			}
			if info.Size() > maxFileSize {
				return nil, fmt.Errorf("skill: %q exceeds %d bytes", path, maxFileSize)
			}
			loaded, err := load(path, maxFileSize)
			if err != nil {
				return nil, err
			}
			if previous, exists := seen[loaded.name]; exists {
				return nil, fmt.Errorf("skill: duplicate name %q in %q and %q", loaded.name, previous, path)
			}
			seen[loaded.name] = path
			entries = append(entries, loaded)
			if len(entries) > maxSkills {
				return nil, fmt.Errorf("skill: discovered more than %d skills", maxSkills)
			}
		}
	}
	if len(entries) == 0 {
		return nil, ErrNoSkills
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	result := &tool{skills: make(map[string]loadedSkill, len(entries))}
	var catalog strings.Builder
	catalog.WriteString("Available skills (load one with the skill tool when it is relevant):")
	for _, loaded := range entries {
		result.skills[loaded.name] = loadedSkill{directory: loaded.directory, content: loaded.content}
		fmt.Fprintf(&catalog, "\n- %s: %s", loaded.name, loaded.description)
	}
	result.catalog = catalog.String()
	result.spec = dora.ToolSpec{
		Name:        "skill",
		Description: toolDescription,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name to load"}},"required":["name"],"additionalProperties":false}`),
	}
	return result, nil
}

// Spec implements dora.Tool.
func (t *tool) Spec() dora.ToolSpec {
	if t == nil {
		return dora.ToolSpec{}
	}
	spec := t.spec
	spec.InputSchema = append(json.RawMessage(nil), spec.InputSchema...)
	return spec
}

// Execute implements dora.Tool.
func (t *tool) Execute(ctx context.Context, input json.RawMessage) (dora.ToolResult, error) {
	if t == nil || t.skills == nil {
		return dora.ToolResult{}, errors.New("skill: tool is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return dora.ToolResult{}, err
	}
	var request struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return dora.ToolResult{}, fmt.Errorf("skill: decode input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return dora.ToolResult{}, errors.New("skill: input contains multiple JSON values")
		}
		return dora.ToolResult{}, fmt.Errorf("skill: decode input: %w", err)
	}
	if request.Name == "" {
		return dora.ToolResult{}, errors.New("skill: name is required")
	}
	loaded, exists := t.skills[request.Name]
	if !exists {
		return dora.ToolResult{}, fmt.Errorf("skill: unknown skill %q", request.Name)
	}
	return dora.ToolResult{Content: fmt.Sprintf(
		"Skill: %s\nDirectory: %s\n\n--- SKILL.md ---\n%s",
		request.Name,
		strconv.Quote(loaded.directory),
		loaded.content,
	)}, nil
}

func load(path string, maxFileSize int64) (entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return entry{}, fmt.Errorf("skill: open %q: %w", path, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return entry{}, fmt.Errorf("skill: read %q: %w", path, err)
	}
	if int64(len(content)) > maxFileSize {
		return entry{}, fmt.Errorf("skill: %q exceeds %d bytes", path, maxFileSize)
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return entry{}, fmt.Errorf("skill: %q must start with YAML front matter", path)
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return entry{}, fmt.Errorf("skill: %q has unterminated YAML front matter", path)
	}

	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:closing], "\n")))
	decoder.KnownFields(true)
	var meta metadata
	if err := decoder.Decode(&meta); err != nil {
		return entry{}, fmt.Errorf("skill: decode %q front matter: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return entry{}, fmt.Errorf("skill: %q front matter contains multiple YAML documents", path)
		}
		return entry{}, fmt.Errorf("skill: decode %q front matter: %w", path, err)
	}
	if !namePattern.MatchString(meta.Name) {
		return entry{}, fmt.Errorf("skill: %q has invalid name %q", path, meta.Name)
	}
	description := strings.Join(strings.Fields(meta.Description), " ")
	if description == "" {
		return entry{}, fmt.Errorf("skill: %q description is required", path)
	}
	if len(description) > maxDescriptionSize {
		return entry{}, fmt.Errorf("skill: %q description exceeds %d bytes", path, maxDescriptionSize)
	}
	if strings.TrimSpace(strings.Join(lines[closing+1:], "\n")) == "" {
		return entry{}, fmt.Errorf("skill: %q instructions are empty", path)
	}
	return entry{
		name:        meta.Name,
		description: description,
		directory:   filepath.Dir(path),
		content:     string(content),
	}, nil
}

var _ dora.Tool = (*tool)(nil)
