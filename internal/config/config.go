package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxToolTimeoutSeconds = 3600

// Config contains the runtime configuration used by the dora CLI.
type Config struct {
	Model  Model  `yaml:"model"`
	Agent  Agent  `yaml:"agent,omitempty"`
	Tools  Tools  `yaml:"tools,omitempty"`
	Skills Skills `yaml:"skills,omitempty"`
}

// Agent configures model-tool loop safeguards.
type Agent struct {
	MaxRounds int `yaml:"max_rounds,omitempty"`
}

// Model describes one configured model endpoint.
type Model struct {
	Provider  string  `yaml:"provider"`
	API       string  `yaml:"api,omitempty"`
	Name      string  `yaml:"name"`
	BaseURL   string  `yaml:"base_url"`
	APIKey    string  `yaml:"api_key,omitempty"`
	APIKeyEnv *string `yaml:"api_key_env,omitempty"`
}

// Tools configures optional capabilities exposed to the model.
type Tools struct {
	Bash       Bash       `yaml:"bash,omitempty"`
	PowerShell PowerShell `yaml:"powershell,omitempty"`
}

// Bash configures the Bash command tool. Nil Enabled uses the platform default.
type Bash struct {
	Enabled        *bool `yaml:"enabled,omitempty"`
	TimeoutSeconds int   `yaml:"timeout_seconds,omitempty"`
}

// PowerShell configures the PowerShell command tool. Nil Enabled uses the
// platform default.
type PowerShell struct {
	Enabled        *bool `yaml:"enabled,omitempty"`
	TimeoutSeconds int   `yaml:"timeout_seconds,omitempty"`
}

// Skills configures local directories containing SKILL.md packages. Skills
// are disabled when no directories are configured.
type Skills struct {
	Directories []string `yaml:"directories,omitempty"`
}

// Default returns the validated built-in configuration. Environment variable
// references are resolved before the configuration is returned.
func Default() (Config, error) {
	cfg := defaultConfig()
	if err := cfg.resolveAndValidate(); err != nil {
		return Config{}, fmt.Errorf("validate default config: %w", err)
	}
	return cfg, nil
}

// Load strictly decodes and validates a YAML configuration file over the
// built-in defaults. Environment variable references are resolved before the
// configuration is returned.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	cfg := defaultConfig()
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

func defaultConfig() Config {
	return Config{}
}

func (cfg *Config) resolveAndValidate() error {
	model := &cfg.Model
	if err := model.selectProvider(); err != nil {
		return err
	}
	preset, ok := modelPresets[model.Provider]
	if !ok {
		return errors.New(`model.provider must be "openai", "deepseek", or "trust"`)
	}
	if model.API == "" {
		model.API = "chat_completions"
	}
	switch model.API {
	case "chat_completions", "responses":
	default:
		return errors.New(`model.api must be "chat_completions" or "responses"`)
	}
	if model.Name == "" {
		model.Name = preset.name
	}
	if model.BaseURL == "" {
		model.BaseURL = preset.baseURL
	}
	if model.APIKeyEnv == nil {
		model.APIKeyEnv = &preset.apiKeyEnv
	}
	if model.APIKey == "" && *model.APIKeyEnv != "" {
		value, ok := os.LookupEnv(*model.APIKeyEnv)
		if !ok || value == "" {
			return fmt.Errorf("environment variable %q is empty or unset", *model.APIKeyEnv)
		}
		model.APIKey = value
	}
	if cfg.Tools.Bash.TimeoutSeconds < 0 {
		return errors.New("tools.bash.timeout_seconds cannot be negative")
	}
	if cfg.Tools.Bash.TimeoutSeconds > maxToolTimeoutSeconds {
		return fmt.Errorf("tools.bash.timeout_seconds cannot exceed %d", maxToolTimeoutSeconds)
	}
	if cfg.Tools.PowerShell.TimeoutSeconds < 0 {
		return errors.New("tools.powershell.timeout_seconds cannot be negative")
	}
	if cfg.Tools.PowerShell.TimeoutSeconds > maxToolTimeoutSeconds {
		return fmt.Errorf("tools.powershell.timeout_seconds cannot exceed %d", maxToolTimeoutSeconds)
	}
	if cfg.Agent.MaxRounds < 0 {
		return errors.New("agent.max_rounds cannot be negative")
	}
	for index, directory := range cfg.Skills.Directories {
		if directory == "" {
			return fmt.Errorf("skills.directories[%d] cannot be empty", index)
		}
	}
	return nil
}

func (model *Model) selectProvider() error {
	if model.Provider != "" {
		return nil
	}

	var detected []string
	for provider, preset := range modelPresets {
		if value, ok := os.LookupEnv(preset.apiKeyEnv); ok && value != "" {
			detected = append(detected, provider)
		}
	}
	sort.Strings(detected)

	switch len(detected) {
	case 0:
		model.Provider = "deepseek"
	case 1:
		model.Provider = detected[0]
	default:
		return fmt.Errorf(
			"model.provider is ambiguous: API key environment variables for %s are set; configure model.provider explicitly",
			strings.Join(detected, ", "),
		)
	}
	return nil
}

type modelPreset struct {
	name      string
	baseURL   string
	apiKeyEnv string
}

var modelPresets = map[string]modelPreset{
	"openai": {
		name:      "gpt-5",
		baseURL:   "https://api.openai.com/v1",
		apiKeyEnv: "OPENAI_API_KEY",
	},
	"deepseek": {
		name:      "deepseek-v4-flash",
		baseURL:   "https://api.deepseek.com",
		apiKeyEnv: "DEEPSEEK_API_KEY",
	},
	"trust": {
		name:      "auto",
		baseURL:   "https://api.trustoken.cn/v1",
		apiKeyEnv: "TRUST_API_KEY",
	},
}
