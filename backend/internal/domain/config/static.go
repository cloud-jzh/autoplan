package config

import (
	"errors"
	"strconv"
	"strings"
)

var (
	ErrInvalidAIConfig     = errors.New("ai config is invalid")
	ErrInvalidClaudeConfig = errors.New("claude cli config is invalid")
	ErrInvalidMCPConfig    = errors.New("mcp config is invalid")
)

const (
	DefaultAIProvider        = "openai"
	DefaultAIModel           = "gpt-5.5"
	DefaultTemperature       = "0.3"
	DefaultMCPHost           = "127.0.0.1"
	DefaultMCPPort     int64 = 43847
	DefaultMCPPath           = "/mcp"
)

type AIConfig struct {
	ID                   int64
	ProjectID            *int64
	Name                 string
	Provider             string
	BaseURL              string
	HasAPIKey            bool
	MaskedAPIKey         string
	Model                string
	Temperature          string
	ThinkingDepth        *string
	ThinkingBudgetTokens *int64
	CreatedAt            string
	UpdatedAt            string
	Version              int64
}

type AIConfigInput struct {
	Name                 *string
	Provider             *string
	BaseURL              *string
	APIKey               *string
	Model                *string
	Temperature          *string
	ThinkingDepth        *string
	ThinkingBudgetTokens *int64
}

type ClaudeCLIConfig struct {
	ID              int64
	ProjectID       *int64
	Name            string
	BaseURL         string
	HasAuthToken    bool
	MaskedAuthToken string
	Model           string
	IsDefault       bool
	CreatedAt       string
	UpdatedAt       string
	Version         int64
}

type ClaudeCLIConfigInput struct {
	Name      *string
	BaseURL   *string
	AuthToken *string
	Model     *string
}

type MCPConfig struct {
	Enabled         bool
	Transport       string
	Host            string
	Port            int64
	Path            string
	PortExplicit    bool
	HasAuthToken    bool
	MaskedAuthToken string
}

type MCPInput struct {
	Enabled   *bool
	Transport *string
	Host      *string
	Port      *int64
	Path      *string
	AuthToken *string
}

func NormalizeAIConfig(input AIConfigInput, current *AIConfig) (AIConfig, error) {
	result := AIConfig{Provider: DefaultAIProvider, Model: DefaultAIModel, Temperature: DefaultTemperature}
	if current != nil {
		result = *current
		result.ProjectID = cloneStaticID(current.ProjectID)
		result.ThinkingDepth = cloneStaticText(current.ThinkingDepth)
		result.ThinkingBudgetTokens = cloneStaticID(current.ThinkingBudgetTokens)
	}
	if input.Name != nil {
		result.Name = strings.TrimSpace(*input.Name)
	}
	if current == nil && input.Name == nil {
		return AIConfig{}, ErrInvalidAIConfig
	}
	if result.Name == "" || len(result.Name) > 200 || strings.ContainsRune(result.Name, 0) {
		return AIConfig{}, ErrInvalidAIConfig
	}
	if input.Provider != nil {
		result.Provider = normalizeAIProvider(*input.Provider)
	} else {
		result.Provider = normalizeAIProvider(result.Provider)
	}
	if input.BaseURL != nil {
		result.BaseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.Model != nil {
		result.Model = strings.TrimSpace(*input.Model)
	}
	if result.Model == "" {
		result.Model = DefaultAIModel
	}
	if input.Temperature != nil {
		result.Temperature = strings.TrimSpace(*input.Temperature)
	}
	if result.Temperature == "" {
		result.Temperature = DefaultTemperature
	}
	if input.ThinkingDepth != nil {
		result.ThinkingDepth = normalizeThinkingDepth(*input.ThinkingDepth, result.Provider)
	} else {
		result.ThinkingDepth = normalizeThinkingDepthValue(result.ThinkingDepth, result.Provider)
	}
	if result.Provider == "anthropic" {
		if input.ThinkingBudgetTokens != nil {
			if *input.ThinkingBudgetTokens <= 0 {
				result.ThinkingBudgetTokens = nil
			} else {
				result.ThinkingBudgetTokens = cloneStaticID(input.ThinkingBudgetTokens)
			}
		}
	} else {
		result.ThinkingBudgetTokens = nil
	}
	if !validConfigText(result.BaseURL, 4096) || !validConfigText(result.Model, 500) ||
		!validConfigText(result.Temperature, 32) || (result.ThinkingBudgetTokens != nil && *result.ThinkingBudgetTokens <= 0) {
		return AIConfig{}, ErrInvalidAIConfig
	}
	return result, nil
}

func NormalizeClaudeCLIConfig(input ClaudeCLIConfigInput, current *ClaudeCLIConfig) (ClaudeCLIConfig, error) {
	result := ClaudeCLIConfig{}
	if current != nil {
		result = *current
		result.ProjectID = cloneStaticID(current.ProjectID)
	}
	if input.Name != nil {
		result.Name = strings.TrimSpace(*input.Name)
	}
	if current == nil && input.Name == nil {
		return ClaudeCLIConfig{}, ErrInvalidClaudeConfig
	}
	if result.Name == "" || !validConfigText(result.Name, 200) {
		return ClaudeCLIConfig{}, ErrInvalidClaudeConfig
	}
	if input.BaseURL != nil {
		result.BaseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.Model != nil {
		result.Model = strings.TrimSpace(*input.Model)
	}
	if !validConfigText(result.BaseURL, 4096) || !validConfigText(result.Model, 500) {
		return ClaudeCLIConfig{}, ErrInvalidClaudeConfig
	}
	return result, nil
}

func DefaultMCPConfig() MCPConfig {
	return MCPConfig{Enabled: true, Transport: "http", Host: DefaultMCPHost, Port: DefaultMCPPort, Path: DefaultMCPPath}
}

func NormalizeMCPConfig(input MCPInput, current MCPConfig) (MCPConfig, error) {
	result := current
	if result.Transport == "" {
		result = DefaultMCPConfig()
	}
	if input.Enabled != nil {
		result.Enabled = *input.Enabled
	}
	if input.Transport != nil {
		transport := strings.ToLower(strings.TrimSpace(*input.Transport))
		if transport == "" {
			transport = "http"
		}
		if transport != "http" && transport != "stdio" {
			return MCPConfig{}, ErrInvalidMCPConfig
		}
		result.Transport = transport
	}
	if input.Host != nil {
		result.Host = strings.TrimSpace(*input.Host)
	}
	if result.Host == "" {
		result.Host = DefaultMCPHost
	}
	if input.Port != nil {
		result.Port = *input.Port
		result.PortExplicit = true
	}
	if result.Port <= 0 || result.Port > 65535 {
		return MCPConfig{}, ErrInvalidMCPConfig
	}
	if input.Path != nil {
		result.Path = strings.TrimSpace(*input.Path)
	}
	if result.Path == "" || result.Path == "/" {
		result.Path = DefaultMCPPath
	}
	if !strings.HasPrefix(result.Path, "/") {
		result.Path = "/" + result.Path
	}
	if strings.ContainsRune(result.Host, 0) || strings.ContainsRune(result.Path, 0) || len(result.Host) > 512 || len(result.Path) > 2048 {
		return MCPConfig{}, ErrInvalidMCPConfig
	}
	return result, nil
}

func ResolveMCPEnvironment(current MCPConfig, environment map[string]string) MCPConfig {
	value := current
	if environment == nil {
		return value
	}
	if raw, exists := environment["AUTOPLAN_MCP_ENABLED"]; exists {
		value.Enabled = normalizedMCPBoolean(raw, value.Enabled)
	}
	if raw, exists := environment["AUTOPLAN_MCP_TRANSPORT"]; exists {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "http", "stdio":
			value.Transport = strings.ToLower(strings.TrimSpace(raw))
		}
	}
	if raw, exists := environment["AUTOPLAN_MCP_HOST"]; exists && strings.TrimSpace(raw) != "" {
		value.Host = strings.TrimSpace(raw)
	}
	if raw, exists := environment["AUTOPLAN_MCP_PORT"]; exists {
		if port, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && port > 0 && port <= 65535 {
			value.Port = port
		}
	}
	if raw, exists := environment["AUTOPLAN_MCP_PATH"]; exists && strings.TrimSpace(raw) != "" {
		value.Path = strings.TrimSpace(raw)
		if !strings.HasPrefix(value.Path, "/") {
			value.Path = "/" + value.Path
		}
	}
	if raw, exists := environment["AUTOPLAN_MCP_AUTH_TOKEN"]; exists {
		value.HasAuthToken = strings.TrimSpace(raw) != ""
		value.MaskedAuthToken = MaskSecret(raw)
	}
	return value
}

func MaskSecret(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return ""
	}
	const prefix = "\u00b7\u00b7\u00b7\u00b7"
	if len(runes) <= 4 {
		return prefix
	}
	return prefix + string(runes[len(runes)-4:])
}

func normalizeAIProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai", "deepseek", "anthropic", "codex":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return DefaultAIProvider
	}
}

func normalizeThinkingDepth(value string, provider string) *string {
	depth := strings.ToLower(strings.TrimSpace(value))
	allowed := map[string][]string{
		"openai":   {"low", "medium", "high", "xhigh"},
		"deepseek": {"low", "medium", "high"},
		"codex":    {"low", "medium", "high", "xhigh"},
	}[provider]
	for _, item := range allowed {
		if depth == item {
			return &depth
		}
	}
	return nil
}

func normalizeThinkingDepthValue(value *string, provider string) *string {
	if value == nil {
		return nil
	}
	return normalizeThinkingDepth(*value, provider)
}

func cloneStaticText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStaticID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validConfigText(value string, maximum int) bool {
	return len(value) <= maximum && !strings.ContainsRune(value, 0)
}

func normalizedMCPBoolean(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "enabled":
		return true
	case "0", "false", "off", "disabled":
		return false
	default:
		return fallback
	}
}
