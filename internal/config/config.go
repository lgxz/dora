package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Config contains the runtime configuration used by the dora CLI.
type Config struct {
	Model  Model  `yaml:"model"`
	Agent  Agent  `yaml:"agent,omitempty"`
	Tools  Tools  `yaml:"tools,omitempty"`
	Skills Skills `yaml:"skills,omitempty"`
}

// Agent configures model-tool loop safeguards.
type Agent struct {
	MaxModelCalls int `yaml:"max_model_calls,omitempty"`
}

// Model describes one configured model endpoint.
type Model struct {
	Provider  string `yaml:"provider"`
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	APIKey    string `yaml:"api_key,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

// Tools configures optional capabilities exposed to the model.
type Tools struct {
	Bash Bash `yaml:"bash,omitempty"`
}

// Bash configures the Bash command tool. It is disabled by default.
type Bash struct {
	Enabled        bool   `yaml:"enabled"`
	WorkingDir     string `yaml:"working_dir,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds,omitempty"`
}

// Skills configures local directories containing SKILL.md packages. Skills
// are disabled when no directories are configured.
type Skills struct {
	Directories []string `yaml:"directories,omitempty"`
}

// Load strictly decodes and validates a YAML configuration file. Environment
// variable references are resolved before the configuration is returned.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode config %q: multiple YAML documents are not allowed", path)
		}
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}

	if err := cfg.resolveAndValidate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (cfg *Config) resolveAndValidate() error {
	model := &cfg.Model
	switch model.Provider {
	case "openai-compatible", "openai-responses":
	default:
		return errors.New(`model.provider must be "openai-compatible" or "openai-responses"`)
	}
	if model.Name == "" {
		return errors.New("model.name is required")
	}
	if model.BaseURL == "" {
		return errors.New("model.base_url is required")
	}
	if model.APIKey != "" && model.APIKeyEnv != "" {
		return errors.New("model.api_key and model.api_key_env are mutually exclusive")
	}
	if model.APIKeyEnv != "" {
		value, ok := os.LookupEnv(model.APIKeyEnv)
		if !ok || value == "" {
			return fmt.Errorf("environment variable %q is empty or unset", model.APIKeyEnv)
		}
		model.APIKey = value
	}
	if cfg.Tools.Bash.TimeoutSeconds < 0 {
		return errors.New("tools.bash.timeout_seconds cannot be negative")
	}
	if cfg.Agent.MaxModelCalls < 0 {
		return errors.New("agent.max_model_calls cannot be negative")
	}
	for index, directory := range cfg.Skills.Directories {
		if directory == "" {
			return fmt.Errorf("skills.directories[%d] cannot be empty", index)
		}
	}
	return nil
}
