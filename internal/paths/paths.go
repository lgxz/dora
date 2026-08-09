// Package paths resolves Dora's filesystem layout.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigFile returns Dora's default YAML configuration path. It follows the
// XDG layout on every operating system.
func ConfigFile() (string, error) {
	home, err := xdgHome("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "dora", "config.yaml"), nil
}

// SessionsDir returns Dora's default directory for named sessions. It follows
// the XDG layout on every operating system.
func SessionsDir() (string, error) {
	home, err := xdgHome("XDG_STATE_HOME", ".local", "state")
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "dora", "sessions"), nil
}

// SkillsDir returns the default skills directory beside the active
// configuration file.
func SkillsDir(configFile string) (string, error) {
	absolute, err := filepath.Abs(configFile)
	if err != nil {
		return "", fmt.Errorf("resolve config path for skills: %w", err)
	}
	return filepath.Join(filepath.Dir(absolute), "skills"), nil
}

func xdgHome(environment string, fallback ...string) (string, error) {
	if value := os.Getenv(environment); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be an absolute path", environment)
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
	parts := append([]string{home}, fallback...)
	return filepath.Join(parts...), nil
}
