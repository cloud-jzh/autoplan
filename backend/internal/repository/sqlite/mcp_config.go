package sqlite

import (
	"context"
	"strconv"
	"strings"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func (transaction *writeTransaction) GetMCPConfig(ctx context.Context) (domainconfig.MCPConfig, error) {
	settings, err := transaction.ListSettings(ctx, "mcp.")
	if err != nil {
		return domainconfig.MCPConfig{}, err
	}
	config, _, err := mcpConfigFromSettings(settings)
	return config, err
}

func (transaction *writeTransaction) SaveMCPConfig(ctx context.Context, input domainconfig.MCPInput) (domainconfig.MCPConfig, error) {
	settings, err := transaction.ListSettings(ctx, "mcp.")
	if err != nil {
		return domainconfig.MCPConfig{}, err
	}
	current, values, err := mcpConfigFromSettings(settings)
	if err != nil {
		return domainconfig.MCPConfig{}, err
	}
	next, err := domainconfig.NormalizeMCPConfig(input, current)
	if err != nil {
		return domainconfig.MCPConfig{}, repository.ErrInvalidAutomation
	}
	updates := map[string]string{}
	if input.Enabled != nil {
		updates["mcp.enabled"] = strconv.FormatBool(next.Enabled)
	}
	if input.Transport != nil {
		updates["mcp.transport"] = next.Transport
	}
	if input.Host != nil {
		updates["mcp.host"] = next.Host
	}
	if input.Port != nil {
		updates["mcp.port"] = strconv.FormatInt(next.Port, 10)
		updates["mcp.portExplicit"] = "true"
	}
	if input.Path != nil {
		updates["mcp.path"] = next.Path
	}
	if input.AuthToken != nil {
		updates["mcp.authToken"] = strings.TrimSpace(*input.AuthToken)
	}
	for _, key := range []string{"mcp.enabled", "mcp.transport", "mcp.host", "mcp.port", "mcp.path", "mcp.authToken", "mcp.portExplicit"} {
		value, changed := updates[key]
		if !changed {
			continue
		}
		setting, exists := values[key]
		expected := int64(1)
		if exists {
			expected = setting.Version
		}
		if _, _, err := transaction.PutSetting(ctx, repository.SettingMutation{Key: key, Value: value, ExpectedVersion: expected}); err != nil {
			return domainconfig.MCPConfig{}, err
		}
	}
	return transaction.GetMCPConfig(ctx)
}

func mcpConfigFromSettings(settings []repository.Setting) (domainconfig.MCPConfig, map[string]repository.Setting, error) {
	values := make(map[string]repository.Setting, len(settings))
	for _, setting := range settings {
		values[setting.Key] = setting
	}
	current := domainconfig.DefaultMCPConfig()
	input := domainconfig.MCPInput{}
	if setting, exists := values["mcp.enabled"]; exists {
		value := mcpBool(setting.Value, true)
		input.Enabled = &value
	}
	if setting, exists := values["mcp.transport"]; exists {
		value := setting.Value
		input.Transport = &value
	}
	if setting, exists := values["mcp.host"]; exists {
		value := setting.Value
		input.Host = &value
	}
	if setting, exists := values["mcp.port"]; exists {
		port, err := strconv.ParseInt(strings.TrimSpace(setting.Value), 10, 64)
		if err == nil {
			input.Port = &port
		}
	}
	if setting, exists := values["mcp.path"]; exists {
		value := setting.Value
		input.Path = &value
	}
	config, err := domainconfig.NormalizeMCPConfig(input, current)
	if err != nil {
		return domainconfig.MCPConfig{}, nil, repository.ErrInvalidStore
	}
	config.PortExplicit = false
	if setting, exists := values["mcp.portExplicit"]; exists {
		config.PortExplicit = mcpBool(setting.Value, false)
	}
	if setting, exists := values["mcp.authToken"]; exists {
		config.HasAuthToken = strings.TrimSpace(setting.Value) != ""
		config.MaskedAuthToken = domainconfig.MaskSecret(setting.Value)
	}
	return config, values, nil
}

func mcpBool(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "enabled":
		return true
	case "0", "false", "off", "disabled":
		return false
	default:
		return fallback
	}
}
