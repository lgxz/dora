package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lgxz/dora"
	"gopkg.in/yaml.v3"
)

const (
	maxToolTimeoutSeconds     = 3600
	defaultModelContextWindow = 1 << 20
)

// Config contains the runtime configuration used by the dora CLI.
type Config struct {
	Policy    PolicySettings `yaml:"policy,omitempty"`
	// Client is a deprecated compatibility field. It is retained only so
	// internal/cli keeps compiling until the final wiring commit; use Policy.
	//
	// Deprecated: use Config.Policy.
	Client    ClientSelector `yaml:"client,omitempty"`
	Providers []Provider     `yaml:"providers,omitempty"`
	// Env supplies config-local fallbacks for provider API key environment
	// variables. It never mutates the process environment.
	Env    map[string]string `yaml:"env,omitempty"`
	Agent  Agent             `yaml:"agent,omitempty"`
	Tools  Tools             `yaml:"tools,omitempty"`
	Skills Skills            `yaml:"skills,omitempty"`

	providerDefaults map[string]providerDefault
}

// Policy selects a provider and profile for one capability. Empty fields leave
// selection to the router (auto).
type Policy struct {
	Provider string `yaml:"provider,omitempty"`
	Profile  string `yaml:"profile,omitempty"`
}

// PolicySettings holds the per-capability policy. Absence of a key means auto.
type PolicySettings struct {
	Text  Policy `yaml:"text,omitempty"`
	Image Policy `yaml:"image,omitempty"`
}

// Agent configures model-tool loop safeguards.
type Agent struct {
	MaxRounds int `yaml:"max_rounds,omitempty"`
	// SystemPrompt overrides the built-in default agent system prompt. Empty
	// uses the built-in default.
	SystemPrompt string `yaml:"system_prompt,omitempty"`
}

// ClientSelector is a deprecated compatibility type. It is retained only so
// internal/cli keeps compiling until the final wiring commit removes the
// legacy client selector; use Policy instead.
//
// Deprecated: use Config.Policy.
type ClientSelector struct {
	Provider string `yaml:"provider,omitempty"`
	Profile  string `yaml:"profile,omitempty"`
}

// Provider describes one provider endpoint with multiple models.
type Provider struct {
	Name                     string      `yaml:"name"`
	BaseURL                  string      `yaml:"base_url"`
	APIKey                   string      `yaml:"-"`
	API                      string      `yaml:"api,omitempty"`
	TimeoutSeconds           int         `yaml:"timeout_seconds,omitempty"`
	ConnectTimeoutSeconds    int         `yaml:"connect_timeout_seconds,omitempty"`
	StreamIdleTimeoutSeconds int         `yaml:"stream_idle_timeout_seconds,omitempty"`
	Models                   []ModelSpec `yaml:"models"`
}

// ModelSpec describes one named model profile under a Provider. Model defaults
// to Name and may be shared by profiles with different generation parameters.
type ModelSpec struct {
	Name      string  `yaml:"name"`
	Model     string  `yaml:"model,omitempty"`
	API       string  `yaml:"api,omitempty"`
	Thinking  *string `yaml:"thinking,omitempty"`
	MaxTokens *int    `yaml:"max_tokens,omitempty"`
	// ContextWindow is an approximate model context capacity measured in
	// message-content bytes. Nil uses the default; configured values must be
	// positive.
	ContextWindow *int     `yaml:"context_window,omitempty"`
	Temperature   *float64 `yaml:"temperature,omitempty"`
	// Capabilities advertises the provider-neutral capabilities this profile
	// supports (for example text, image_input).
	Capabilities []dora.Capability `yaml:"capabilities,omitempty"`
	// Vision is a deprecated compatibility field derived from Capabilities. It
	// is retained only so internal/cli keeps compiling until the final wiring
	// commit; use Capabilities.
	//
	// Deprecated: use Capabilities.
	Vision bool `yaml:"vision,omitempty"`
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
	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, err
	}
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

	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, err
	}
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

//go:embed builtin_providers.yaml
var builtinProvidersYAML []byte

type providerDefault struct {
	baseURL string
}

func defaultConfig() (Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(builtinProvidersYAML))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode built-in provider catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode built-in provider catalog: multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("decode built-in provider catalog: %w", err)
	}
	cfg.providerDefaults = make(map[string]providerDefault, len(cfg.Providers))
	for i, provider := range cfg.Providers {
		if provider.Name == "" || provider.BaseURL == "" {
			return Config{}, fmt.Errorf("decode built-in provider catalog: providers[%d] must define name and base_url", i)
		}
		cfg.providerDefaults[provider.Name] = providerDefault{
			baseURL: provider.BaseURL,
		}
	}
	if err := cfg.resolveProviders(); err != nil {
		return Config{}, fmt.Errorf("validate built-in provider catalog: %w", err)
	}
	return cfg, nil
}

// intPtr returns a pointer to v. It is a convenience helper for setting
// pointer-typed config defaults.
func intPtr(v int) *int {
	return &v
}

func (cfg *Config) resolveAndValidate() error {
	if err := cfg.resolvePolicy(); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		return errors.New("providers cannot be empty")
	}
	if err := cfg.resolveProviders(); err != nil {
		return err
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

// resolvePolicy applies per-capability policy environment variables to the
// config policy. The v1 capability keys are text and image.
func (cfg *Config) resolvePolicy() error {
	for _, capKey := range []string{"text", "image"} {
		var policy *Policy
		switch capKey {
		case "text":
			policy = &cfg.Policy.Text
		case "image":
			policy = &cfg.Policy.Image
		}
		if err := resolvePolicyField(capKey, "provider", &policy.Provider); err != nil {
			return err
		}
		if err := resolvePolicyField(capKey, "profile", &policy.Profile); err != nil {
			return err
		}
	}
	return nil
}

func resolvePolicyField(capKey, field string, dst *string) error {
	envVar := "DORA_POLICY_" + strings.ToUpper(capKey) + "_" + strings.ToUpper(field)
	if value, ok := os.LookupEnv(envVar); ok && value != "" {
		*dst = value
	}
	return nil
}

// resolveProviders fills built-in connection defaults, resolves API keys, and
// validates the provider/model-profile catalog. Missing API keys remain valid
// so local endpoints can operate without authentication.
func (cfg *Config) resolveProviders() error {
	providerNames := make(map[string]struct{}, len(cfg.Providers))
	apiKeyEnvironments := make(map[string]string, len(cfg.Providers))
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name == "" {
			return fmt.Errorf("providers[%d].name cannot be empty", i)
		}
		if _, exists := providerNames[p.Name]; exists {
			return fmt.Errorf("providers[%d].name %q is duplicated", i, p.Name)
		}
		providerNames[p.Name] = struct{}{}
		apiKeyEnvironment := providerAPIKeyEnvironment(p.Name)
		if owner, exists := apiKeyEnvironments[apiKeyEnvironment]; exists {
			return fmt.Errorf(
				"providers[%d].name %q maps to API key environment variable %q already used by provider %q",
				i, p.Name, apiKeyEnvironment, owner,
			)
		}
		apiKeyEnvironments[apiKeyEnvironment] = p.Name
		if defaults, ok := cfg.providerDefaults[p.Name]; ok {
			if p.BaseURL == "" {
				p.BaseURL = defaults.baseURL
			}
		}
		if p.BaseURL == "" {
			return fmt.Errorf("providers[%d].base_url cannot be empty", i)
		}
		if p.API == "" {
			p.API = "chat_completions"
		}
		if err := validateAPI(p.API, fmt.Sprintf("providers[%d].api", i)); err != nil {
			return err
		}
		if p.TimeoutSeconds < 0 {
			return fmt.Errorf("providers[%d].timeout_seconds cannot be negative", i)
		}
		if p.ConnectTimeoutSeconds < 0 {
			return fmt.Errorf("providers[%d].connect_timeout_seconds cannot be negative", i)
		}
		if p.StreamIdleTimeoutSeconds < 0 {
			return fmt.Errorf("providers[%d].stream_idle_timeout_seconds cannot be negative", i)
		}
		p.APIKey = ""
		// The real process environment wins over the config-local fallback. The
		// fallback never mutates the process environment or child processes.
		if value, ok := os.LookupEnv(apiKeyEnvironment); ok && value != "" {
			p.APIKey = value
		} else if value := cfg.Env[apiKeyEnvironment]; value != "" {
			p.APIKey = value
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("providers[%d].models cannot be empty", i)
		}
		modelNames := make(map[string]struct{}, len(p.Models))
		for j := range p.Models {
			m := &p.Models[j]
			if m.Name == "" {
				return fmt.Errorf("providers[%d].models[%d].name cannot be empty", i, j)
			}
			if m.Model == "" {
				m.Model = m.Name
			}
			if _, exists := modelNames[m.Name]; exists {
				return fmt.Errorf("providers[%d].models[%d].name %q is duplicated", i, j, m.Name)
			}
			modelNames[m.Name] = struct{}{}
			if m.API != "" {
				if err := validateAPI(m.API, fmt.Sprintf("providers[%d].models[%d].api", i, j)); err != nil {
					return err
				}
			}
			if m.MaxTokens != nil && *m.MaxTokens < 0 {
				return fmt.Errorf("providers[%d].models[%d].max_tokens cannot be negative", i, j)
			}
			if m.MaxTokens == nil {
				m.MaxTokens = intPtr(32768)
			}
			if m.ContextWindow != nil && *m.ContextWindow <= 0 {
				return fmt.Errorf("providers[%d].models[%d].context_window must be positive", i, j)
			}
			if m.ContextWindow == nil {
				m.ContextWindow = intPtr(defaultModelContextWindow)
			}
			if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
				return fmt.Errorf("providers[%d].models[%d].temperature must be within [0, 2]", i, j)
			}
			if m.Thinking != nil {
				switch *m.Thinking {
				case "off", "minimal", "low", "medium", "high":
				default:
					return fmt.Errorf(`providers[%d].models[%d].thinking must be one of "off", "minimal", "low", "medium", "high"`, i, j)
				}
			}
			for _, capability := range m.Capabilities {
				if !validCapability(capability) {
					return fmt.Errorf("providers[%d].models[%d].capabilities contains unknown capability %q", i, j, capability)
				}
			}
		}
	}
	for name := range cfg.Env {
		if _, ok := apiKeyEnvironments[name]; !ok {
			return fmt.Errorf("env.%s does not match any configured provider API key", name)
		}
	}
	return nil
}

// providerAPIKeyEnvironment derives the conventional API key environment
// variable for a provider. ASCII letters are uppercased and non-ASCII-
// alphanumeric characters become underscores.
func providerAPIKeyEnvironment(name string) string {
	var result strings.Builder
	result.Grow(len(name) + len("_API_KEY"))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			result.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			result.WriteRune(r)
		default:
			result.WriteByte('_')
		}
	}
	result.WriteString("_API_KEY")
	return result.String()
}

func validateAPI(api, field string) error {
	switch api {
	case "chat_completions", "responses":
		return nil
	default:
		return fmt.Errorf(`%s must be "chat_completions" or "responses"`, field)
	}
}

// validCapability reports whether capability is a recognized dora.Capability.
func validCapability(capability dora.Capability) bool {
	switch capability {
	case dora.CapabilityText, dora.CapabilityImageInput, dora.CapabilityImageOutput,
		dora.CapabilityAudioInput, dora.CapabilityFileInput:
		return true
	default:
		return false
	}
}
