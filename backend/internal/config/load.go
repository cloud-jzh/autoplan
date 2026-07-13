package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const configFileEnvironment = EnvironmentPrefix + "CONFIG_FILE"

var knownEnvironment = map[string]struct{}{
	configFileEnvironment:                     {},
	EnvironmentPrefix + "LISTEN_HOST":         {},
	EnvironmentPrefix + "LISTEN_PORT":         {},
	EnvironmentPrefix + "ALLOWED_ORIGINS":     {},
	EnvironmentPrefix + "BODY_LIMIT_BYTES":    {},
	EnvironmentPrefix + "READ_HEADER_TIMEOUT": {},
	EnvironmentPrefix + "READ_TIMEOUT":        {},
	EnvironmentPrefix + "WRITE_TIMEOUT":       {},
	EnvironmentPrefix + "IDLE_TIMEOUT":        {},
	EnvironmentPrefix + "SHUTDOWN_TIMEOUT":    {},
	EnvironmentPrefix + "RUNTIME_DIR":         {},
	EnvironmentPrefix + "RUNTIME_TARGET_KIND": {},
	EnvironmentPrefix + "LOG_LEVEL":           {},
}

// LoadOptions makes configuration sources replaceable in tests without
// changing process globals or reading real application directories.
type LoadOptions struct {
	RepositoryRoot string
	TemporaryRoot  string
	Environ        []string
	ReadFile       func(string) ([]byte, error)
}

type fileConfig struct {
	ListenHost        *string   `json:"listen_host"`
	ListenPort        *int      `json:"listen_port"`
	AllowedOrigins    *[]string `json:"allowed_origins"`
	BodyLimitBytes    *int64    `json:"body_limit_bytes"`
	ReadHeaderTimeout *string   `json:"read_header_timeout"`
	ReadTimeout       *string   `json:"read_timeout"`
	WriteTimeout      *string   `json:"write_timeout"`
	IdleTimeout       *string   `json:"idle_timeout"`
	ShutdownTimeout   *string   `json:"shutdown_timeout"`
	RuntimeDirectory  *string   `json:"runtime_dir"`
	RuntimeTargetKind *string   `json:"runtime_target_kind"`
	LogLevel          *string   `json:"log_level"`
}

// Load applies exactly: secure defaults, optional strict JSON file, then known
// environment overrides. Unknown or duplicate sidecar variables are rejected.
func Load(options LoadOptions) (Config, error) {
	values, err := parseEnvironment(options.Environ)
	if err != nil {
		return Config{}, err
	}
	result := Defaults()
	if fileName, exists := values[configFileEnvironment]; exists {
		if strings.TrimSpace(fileName) == "" {
			return Config{}, newError("config_file_empty")
		}
		if !filepath.IsAbs(fileName) {
			return Config{}, newError("config_file_path_invalid")
		}
		reader := options.ReadFile
		if reader == nil {
			reader = os.ReadFile
		}
		content, readErr := reader(fileName)
		if readErr != nil {
			return Config{}, newError("config_file_unavailable")
		}
		fileValue, decodeErr := decodeFile(content)
		if decodeErr != nil {
			return Config{}, decodeErr
		}
		if err := applyFile(&result, fileValue); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnvironment(&result, values); err != nil {
		return Config{}, err
	}
	if err := result.Validate(options.RepositoryRoot, options.TemporaryRoot); err != nil {
		return Config{}, err
	}
	return result, nil
}

func parseEnvironment(environ []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		upperName := strings.ToUpper(name)
		if !strings.HasPrefix(upperName, EnvironmentPrefix) {
			continue
		}
		if name != upperName {
			return nil, newError("environment_name_invalid")
		}
		if _, known := knownEnvironment[name]; !known {
			return nil, newError("environment_unknown")
		}
		if _, duplicate := values[name]; duplicate {
			return nil, newError("environment_duplicate")
		}
		values[name] = value
	}
	return values, nil
}

func decodeFile(content []byte) (fileConfig, error) {
	if err := validateJSONObject(content); err != nil {
		return fileConfig{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value fileConfig
	if err := decoder.Decode(&value); err != nil {
		return fileConfig{}, newError("config_file_invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fileConfig{}, newError("config_file_trailing_data")
	}
	return value, nil
}

func validateJSONObject(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return newError("config_file_invalid")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return newError("config_file_invalid")
		}
		name, ok := token.(string)
		if !ok {
			return newError("config_file_invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return newError("config_file_duplicate_key")
		}
		seen[name] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return newError("config_file_invalid")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return newError("config_file_null_value")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return newError("config_file_invalid")
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return newError("config_file_trailing_data")
	}
	return nil
}

func applyFile(target *Config, source fileConfig) error {
	if source.ListenHost != nil {
		target.HTTP.ListenHost = *source.ListenHost
	}
	if source.ListenPort != nil {
		target.HTTP.ListenPort = *source.ListenPort
	}
	if source.AllowedOrigins != nil {
		target.HTTP.AllowedOrigins = append([]string(nil), (*source.AllowedOrigins)...)
	}
	if source.BodyLimitBytes != nil {
		target.HTTP.BodyLimitBytes = *source.BodyLimitBytes
	}
	for _, item := range []struct {
		value       *string
		destination *time.Duration
	}{
		{source.ReadHeaderTimeout, &target.HTTP.ReadHeaderTimeout},
		{source.ReadTimeout, &target.HTTP.ReadTimeout},
		{source.WriteTimeout, &target.HTTP.WriteTimeout},
		{source.IdleTimeout, &target.HTTP.IdleTimeout},
		{source.ShutdownTimeout, &target.HTTP.ShutdownTimeout},
	} {
		if item.value != nil {
			parsed, err := time.ParseDuration(*item.value)
			if err != nil {
				return newError("duration_invalid")
			}
			*item.destination = parsed
		}
	}
	if source.RuntimeDirectory != nil {
		if *source.RuntimeDirectory == "" {
			return newError("runtime_directory_empty")
		}
		target.Runtime.Directory = *source.RuntimeDirectory
	}
	if source.RuntimeTargetKind != nil {
		if *source.RuntimeTargetKind == "" {
			return newError("runtime_target_kind_empty")
		}
		target.Runtime.TargetKind = RuntimeTargetKind(*source.RuntimeTargetKind)
	}
	if source.LogLevel != nil {
		target.LogLevel = LogLevel(*source.LogLevel)
	}
	return nil
}

func applyEnvironment(target *Config, values map[string]string) error {
	if value, exists := values[EnvironmentPrefix+"LISTEN_HOST"]; exists {
		target.HTTP.ListenHost = value
	}
	if value, exists := values[EnvironmentPrefix+"LISTEN_PORT"]; exists {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return newError("listen_port_invalid")
		}
		target.HTTP.ListenPort = parsed
	}
	if value, exists := values[EnvironmentPrefix+"ALLOWED_ORIGINS"]; exists {
		if strings.TrimSpace(value) == "" {
			return newError("allowed_origins_empty")
		}
		target.HTTP.AllowedOrigins = strings.Split(value, ",")
	}
	if value, exists := values[EnvironmentPrefix+"BODY_LIMIT_BYTES"]; exists {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return newError("body_limit_invalid")
		}
		target.HTTP.BodyLimitBytes = parsed
	}
	durations := []struct {
		name        string
		destination *time.Duration
	}{
		{EnvironmentPrefix + "READ_HEADER_TIMEOUT", &target.HTTP.ReadHeaderTimeout},
		{EnvironmentPrefix + "READ_TIMEOUT", &target.HTTP.ReadTimeout},
		{EnvironmentPrefix + "WRITE_TIMEOUT", &target.HTTP.WriteTimeout},
		{EnvironmentPrefix + "IDLE_TIMEOUT", &target.HTTP.IdleTimeout},
		{EnvironmentPrefix + "SHUTDOWN_TIMEOUT", &target.HTTP.ShutdownTimeout},
	}
	for _, item := range durations {
		if value, exists := values[item.name]; exists {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return newError("duration_invalid")
			}
			*item.destination = parsed
		}
	}
	if value, exists := values[EnvironmentPrefix+"RUNTIME_DIR"]; exists {
		if value == "" {
			return newError("runtime_directory_empty")
		}
		target.Runtime.Directory = value
	}
	if value, exists := values[EnvironmentPrefix+"RUNTIME_TARGET_KIND"]; exists {
		if value == "" {
			return newError("runtime_target_kind_empty")
		}
		target.Runtime.TargetKind = RuntimeTargetKind(value)
	}
	if value, exists := values[EnvironmentPrefix+"LOG_LEVEL"]; exists {
		target.LogLevel = LogLevel(value)
	}
	return nil
}
