// Package paths resolves Dora's filesystem layout.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigFile returns Dora's default YAML configuration path. It lives at
// <doraHome>/config.yaml.
func ConfigFile() (string, error) {
	home, err := doraHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "config.yaml"), nil
}

// SessionsDir returns Dora's default directory for named sessions. It lives at
// <doraHome>/sessions.
func SessionsDir() (string, error) {
	home, err := doraHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "sessions"), nil
}

// SkillsDir returns Dora's default directory for skills. It lives at
// <doraHome>/skills.
func SkillsDir() (string, error) {
	home, err := doraHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "skills"), nil
}

// doraHome returns Dora's home directory. It uses the DORA_HOME environment
// variable when set, otherwise it falls back to $HOME/.dora.
func doraHome() (string, error) {
	if value := os.Getenv("DORA_HOME"); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("DORA_HOME must be an absolute path")
		}
		return filepath.Clean(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("home directory must be an absolute path")
	}
	return filepath.Join(home, ".dora"), nil
}
