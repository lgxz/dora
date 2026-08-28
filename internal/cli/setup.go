package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"gopkg.in/yaml.v3"
)

func runSetup(opts options, streams IO) error {
	cfg, err := config.Default()
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		return errors.New("no built-in providers are available")
	}

	reader := bufio.NewReader(streams.Stdin)
	fmt.Fprintln(streams.Stdout, "Dora setup")
	fmt.Fprintln(streams.Stdout, "")
	fmt.Fprintln(streams.Stdout, "Select a model provider:")
	for i, provider := range cfg.Providers {
		fmt.Fprintf(streams.Stdout, "  %d. %s\n", i+1, provider.Name)
	}
	providerInput, err := readSetupLine(reader, streams.Stdout, "Provider [1]: ")
	if err != nil {
		return fmt.Errorf("read provider: %w", err)
	}
	provider, err := selectProvider(cfg.Providers, providerInput)
	if err != nil {
		return err
	}

	fmt.Fprintln(streams.Stdout, "")
	fmt.Fprintln(streams.Stdout, "Available model profiles:")
	fmt.Fprintln(streams.Stdout, "  0. auto")
	for i, profile := range provider.Profiles {
		fmt.Fprintf(streams.Stdout, "  %d. %s\n", i+1, profile.Name)
	}
	profileInput, err := readSetupLine(reader, streams.Stdout, "Profile [auto]: ")
	if err != nil {
		return fmt.Errorf("read model profile: %w", err)
	}
	profile, err := selectProfile(provider.Profiles, profileInput)
	if err != nil {
		return err
	}

	fmt.Fprintf(streams.Stdout, "%s: ", config.APIKeyEnvironment(provider.Name))
	var apiKey string
	if streams.StdinIsTerminal && streams.ReadSecret != nil {
		apiKey, err = streams.ReadSecret()
		fmt.Fprintln(streams.Stdout)
	} else {
		apiKey, err = readSetupLine(reader, io.Discard, "")
	}
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return errors.New("API key cannot be empty")
	}

	configPath := opts.configPath
	if configPath == "" {
		configPath, err = paths.ConfigFile()
		if err != nil {
			return err
		}
	}
	if err := updateSetupConfig(configPath, provider.Name, profile, apiKey); err != nil {
		return err
	}

	fmt.Fprintf(streams.Stdout, "\nConfigured %s", provider.Name)
	if profile != "" {
		fmt.Fprintf(streams.Stdout, "/%s", profile)
	} else {
		fmt.Fprint(streams.Stdout, " with automatic model selection")
	}
	fmt.Fprintf(streams.Stdout, " in %s\n", configPath)
	return nil
}

func readSetupLine(reader *bufio.Reader, output io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(output, prompt); err != nil {
		return "", err
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	return strings.TrimSpace(value), nil
}

func selectProvider(providers []config.Provider, input string) (config.Provider, error) {
	if input == "" {
		return providers[0], nil
	}
	if index, err := strconv.Atoi(input); err == nil {
		if index >= 1 && index <= len(providers) {
			return providers[index-1], nil
		}
		return config.Provider{}, fmt.Errorf("provider selection must be between 1 and %d", len(providers))
	}
	for _, provider := range providers {
		if input == provider.Name {
			return provider, nil
		}
	}
	return config.Provider{}, fmt.Errorf("unknown provider %q", input)
}

func selectProfile(profiles []config.ProfileSpec, input string) (string, error) {
	if input == "" || input == "0" || input == "auto" {
		return "", nil
	}
	if index, err := strconv.Atoi(input); err == nil {
		if index >= 1 && index <= len(profiles) {
			return profiles[index-1].Name, nil
		}
		return "", fmt.Errorf("profile selection must be between 0 and %d", len(profiles))
	}
	for _, profile := range profiles {
		if input == profile.Name {
			return profile.Name, nil
		}
	}
	return "", fmt.Errorf("unknown model profile %q", input)
}

func updateSetupConfig(path, provider, profile, apiKey string) error {
	document, err := readSetupConfig(path)
	if err != nil {
		return err
	}
	root := document.Content[0]
	env := ensureMapping(root, "env")
	setScalar(env, config.APIKeyEnvironment(provider), apiKey)
	policy := ensureMapping(root, "policy")
	textPolicy := ensureMapping(policy, "text")
	setScalar(textPolicy, "provider", provider)
	if profile == "" {
		removeMappingValue(textPolicy, "profile")
	} else {
		setScalar(textPolicy, "profile", profile)
	}

	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode config %q: %w", path, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("encode config %q: %w", path, err)
	}
	return writeValidatedSetupConfig(path, data.Bytes())
}

func readSetupConfig(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	if err == nil && len(bytes.TrimSpace(data)) != 0 {
		if _, err := config.Load(path); err != nil {
			return nil, err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode config %q: %w", path, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return nil, fmt.Errorf("decode config %q: root must be a mapping", path)
		}
		return &document, nil
	}
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}, nil
}

func ensureMapping(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			value := parent.Content[i+1]
			if value.Kind != yaml.MappingNode {
				value.Kind = yaml.MappingNode
				value.Tag = "!!map"
				value.Value = ""
				value.Content = nil
			}
			return value
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valueNode)
	return valueNode
}

func setScalar(parent *yaml.Node, key, value string) {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1].Kind = yaml.ScalarNode
			parent.Content[i+1].Tag = "!!str"
			parent.Content[i+1].Value = value
			parent.Content[i+1].Content = nil
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func removeMappingValue(parent *yaml.Node, key string) {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return
		}
	}
}

func writeValidatedSetupConfig(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".dora-config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if _, err := config.Load(temporaryPath); err != nil {
		return fmt.Errorf("validate updated config: %w", err)
	}
	if err := replaceSetupConfig(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}
	return nil
}

func replaceSetupConfig(temporaryPath, path string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(temporaryPath, path)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return os.Rename(temporaryPath, path)
	} else if err != nil {
		return err
	}
	backup, err := os.CreateTemp(filepath.Dir(path), ".dora-config-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(path, backupPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		return err
	}
	return os.Remove(backupPath)
}
