package argp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type configKey struct {
	name         string
	defaultValue string
	comment      string
}

// The config file only carries these keys. The storage directory and the
// config file path itself are command-line only; resolving them from the file
// they live in would be circular.
var configKeys = []configKey{
	{
		name:         "listen",
		defaultValue: "",
		comment:      "# TCP address to listen on, e.g. \":8080\". Empty selects \":443\" when\n# TLS certificates exist in the storage directory, otherwise \":80\".",
	},
	{
		name:         "loglevel",
		defaultValue: "info",
		comment:      "# Log level: \"debug\" or \"info\".",
	},
	{
		name:         "trusted-proxies",
		defaultValue: "",
		comment:      "# Comma-separated trusted proxy IPs for X-Forwarded-For.",
	},
	{
		name:         "secure-cookies",
		defaultValue: "false",
		comment:      "# \"true\" marks session cookies Secure (use behind HTTPS or TLS termination).",
	},
	{
		name:         "setup-token",
		defaultValue: "",
		comment:      "# Token for the first (admin) registration, at least 32 characters.\n# Empty falls back to the CACAO_SETUP_TOKEN environment variable.",
	},
}

var (
	configValues map[string]string
	configErr    error
	configPath   string
)

func init() {
	LoadConfig(resolveConfigPath())
}

func cliGet(name string) (string, bool) {
	for _, arg := range os.Args {
		option := strings.SplitN(arg, "=", 2)
		if len(option) == 2 {
			if "--"+name == option[0] {
				return option[1], true
			}
		}
	}
	return "", false
}

// Get resolves a setting as command-line flag, then config file, then the
// caller's default value.
func Get(name string, value string) string {
	if option, ok := cliGet(name); ok {
		return option
	}
	if option := Config(name); option != "" {
		return option
	}
	return value
}

// Config returns the config file value of a supported key, or "" when the key
// is unset, empty, or not configurable through the file.
func Config(name string) string {
	for _, key := range configKeys {
		if key.name == name {
			return configValues[name]
		}
	}
	return ""
}

func resolveConfigPath() string {
	if option, ok := cliGet("config"); ok && option != "" {
		return option
	}
	storage := "."
	if option, ok := cliGet("storage"); ok && option != "" {
		storage = option
	}
	return filepath.Join(storage, "config.toml")
}

// LoadConfig reads the config file at path. A missing file leaves the config
// layer empty. Parsing problems are returned and remembered so startup can
// fail closed. An empty path clears the config layer.
func LoadConfig(path string) error {
	configValues, configErr, configPath = nil, nil, ""
	if path == "" {
		return nil
	}
	configPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		configErr = err
		return err
	}
	values := map[string]any{}
	if err := toml.Unmarshal(data, &values); err != nil {
		configErr = fmt.Errorf("parse %s: %w", path, err)
		return configErr
	}
	// Tables and unknown keys are allowed; only the supported keys must be
	// quoted strings.
	parsed := map[string]string{}
	for _, key := range configKeys {
		value, ok := values[key.name]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			configErr = fmt.Errorf("parse %s: %s must be a quoted string", path, key.name)
			return configErr
		}
		parsed[key.name] = text
	}
	configValues = parsed
	return nil
}

// MaintainConfig creates the config file when it is missing and appends known
// keys that are absent, preserving the existing content. It never modifies a
// file that failed to parse.
func MaintainConfig() error {
	if configErr != nil {
		return configErr
	}
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return createConfigFile()
		}
		return err
	}
	values := map[string]any{}
	if err := toml.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	var missing []string
	for _, key := range configKeys {
		if _, ok := values[key.name]; !ok {
			missing = append(missing, key.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return appendConfigKeys(configPath, string(data), missing)
}

func createConfigFile() error {
	var out strings.Builder
	out.WriteString("# Cacao configuration file. Command-line flags override these values,\n")
	out.WriteString("# and missing keys are appended at startup. The storage directory and this\n")
	out.WriteString("# file's location are set with --storage and --config only.\n")
	for i, key := range configKeys {
		if i != 0 {
			out.WriteString("\n")
		}
		out.WriteString(key.comment + "\n")
		out.WriteString(key.name + ` = "` + key.defaultValue + `"` + "\n")
	}
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(out.String()), 0o600); err != nil {
		return fmt.Errorf("create %s: %w", configPath, err)
	}
	return LoadConfig(configPath)
}

// appendConfigKeys adds missing keys before the first table header, or at the
// end of a table-free file, so keys never land inside a [table] section where
// they would be parsed under a different namespace and re-appended forever.
func appendConfigKeys(path, text string, missing []string) error {
	var block strings.Builder
	for _, name := range missing {
		for _, key := range configKeys {
			if key.name == name {
				block.WriteString(key.comment + "\n")
				block.WriteString(name + ` = "` + key.defaultValue + `"` + "\n")
			}
		}
	}
	lines := strings.Split(text, "\n")
	insert := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			insert = i
			break
		}
	}
	var out strings.Builder
	out.WriteString(strings.Join(lines[:insert], "\n"))
	if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\n")
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	out.WriteString(block.String())
	out.WriteString(strings.Join(lines[insert:], "\n"))
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return LoadConfig(path)
}
