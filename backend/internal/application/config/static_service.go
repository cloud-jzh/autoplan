package config

import (
	"context"
	"errors"
	"time"

	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

var (
	ErrStaticUnavailable     = errors.New("static config application service unavailable")
	ErrStaticInvalidCommand  = errors.New("static config command is invalid")
	ErrStaticConflict        = errors.New("static config state conflicts")
	ErrStaticRuntimeDisabled = errors.New("mcp runtime capability is disabled")
)

type StaticDependencies struct {
	Writer repository.ChatTransactional
	Clock  Clock
}

type StaticService struct {
	writer repository.ChatTransactional
	clock  Clock
}

func NewStaticService(dependencies StaticDependencies) *StaticService {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &StaticService{writer: dependencies.Writer, clock: clock}
}

func (service *StaticService) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.writer == nil || service.clock == nil {
		return ErrStaticUnavailable
	}
	return service.writer.Check(ctx)
}

func (service *StaticService) timestamp(after ...string) string {
	next := service.clock.Now().UTC().Truncate(time.Millisecond)
	for _, value := range after {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && !next.After(parsed) {
			next = parsed.Add(time.Millisecond)
		}
	}
	return next.UTC().Format("2006-01-02T15:04:05.000Z")
}

type AIConfigDTO struct {
	ID                        int64   `json:"id"`
	ProjectID                 *int64  `json:"project_id"`
	ProjectId                 *int64  `json:"projectId"`
	Name                      string  `json:"name"`
	Provider                  string  `json:"provider"`
	BaseURL                   string  `json:"base_url"`
	BaseUrl                   string  `json:"baseUrl"`
	HasAPIKey                 bool    `json:"has_api_key"`
	HasApiKey                 bool    `json:"hasApiKey"`
	MaskedKey                 string  `json:"masked_key"`
	MaskedKeyCamel            string  `json:"maskedKey"`
	Model                     string  `json:"model"`
	Temperature               string  `json:"temperature"`
	ThinkingDepth             *string `json:"thinking_depth"`
	ThinkingDepthCamel        *string `json:"thinkingDepth"`
	ThinkingBudgetTokens      *int64  `json:"thinking_budget_tokens"`
	ThinkingBudgetTokensCamel *int64  `json:"thinkingBudgetTokens"`
	CreatedAt                 string  `json:"created_at"`
	CreatedAtCamel            string  `json:"createdAt"`
	UpdatedAt                 string  `json:"updated_at"`
	UpdatedAtCamel            string  `json:"updatedAt"`
	Version                   int64   `json:"version"`
}

type ClaudeCLIConfigDTO struct {
	ID                int64  `json:"id"`
	ProjectID         *int64 `json:"project_id"`
	ProjectId         *int64 `json:"projectId"`
	Name              string `json:"name"`
	BaseURL           string `json:"base_url"`
	BaseUrl           string `json:"baseUrl"`
	HasAuthToken      bool   `json:"has_auth_token"`
	HasAuthTokenCamel bool   `json:"hasAuthToken"`
	MaskedKey         string `json:"masked_key"`
	MaskedKeyCamel    string `json:"maskedKey"`
	Model             string `json:"model"`
	IsDefault         bool   `json:"is_default"`
	IsDefaultCamel    bool   `json:"isDefault"`
	CreatedAt         string `json:"created_at"`
	CreatedAtCamel    string `json:"createdAt"`
	UpdatedAt         string `json:"updated_at"`
	UpdatedAtCamel    string `json:"updatedAt"`
	Version           int64  `json:"version"`
}

type MCPConfigDTO struct {
	Enabled              bool   `json:"enabled"`
	Transport            string `json:"transport"`
	Host                 string `json:"host"`
	Port                 int64  `json:"port"`
	Path                 string `json:"path"`
	PortExplicit         bool   `json:"port_explicit"`
	PortExplicitCamel    bool   `json:"portExplicit"`
	HasAuthToken         bool   `json:"has_auth_token"`
	HasAuthTokenCamel    bool   `json:"hasAuthToken"`
	AuthTokenMasked      string `json:"auth_token_masked"`
	AuthTokenMaskedCamel string `json:"authTokenMasked"`
}

func (service *StaticService) ListAIConfigs(ctx context.Context) ([]AIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	var result []AIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		records, err := transaction.ListAIConfigs(ctx)
		if err != nil {
			return err
		}
		result = make([]AIConfigDTO, 0, len(records))
		for _, record := range records {
			result = append(result, aiConfigDTO(record))
		}
		return nil
	})
	return result, mapStaticError(err)
}

func (service *StaticService) CreateAIConfig(ctx context.Context, input domainconfig.AIConfigInput) (AIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return AIConfigDTO{}, err
	}
	var result AIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.CreateAIConfig(ctx, input, service.timestamp())
		if err == nil {
			result = aiConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) UpdateAIConfig(ctx context.Context, id, expectedVersion int64, input domainconfig.AIConfigInput) (AIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return AIConfigDTO{}, err
	}
	if id <= 0 || expectedVersion <= 0 {
		return AIConfigDTO{}, ErrStaticInvalidCommand
	}
	var result AIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetAIConfig(ctx, id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.UpdateAIConfig(ctx, id, expectedVersion, input, service.timestamp(current.UpdatedAt))
		if err == nil {
			result = aiConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) DeleteAIConfig(ctx context.Context, id, expectedVersion int64) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if id <= 0 || expectedVersion <= 0 {
		return ErrStaticInvalidCommand
	}
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetAIConfig(ctx, id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		return transaction.DeleteAIConfig(ctx, id, expectedVersion, service.timestamp(current.UpdatedAt))
	})
	return mapStaticError(err)
}

func (service *StaticService) ListClaudeCLIConfigs(ctx context.Context) ([]ClaudeCLIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	var result []ClaudeCLIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		records, err := transaction.ListClaudeCLIConfigs(ctx)
		if err != nil {
			return err
		}
		result = make([]ClaudeCLIConfigDTO, 0, len(records))
		for _, record := range records {
			result = append(result, claudeConfigDTO(record))
		}
		return nil
	})
	return result, mapStaticError(err)
}

func (service *StaticService) CreateClaudeCLIConfig(ctx context.Context, input domainconfig.ClaudeCLIConfigInput) (ClaudeCLIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ClaudeCLIConfigDTO{}, err
	}
	var result ClaudeCLIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.CreateClaudeCLIConfig(ctx, input, service.timestamp())
		if err == nil {
			result = claudeConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) UpdateClaudeCLIConfig(ctx context.Context, id, expectedVersion int64, input domainconfig.ClaudeCLIConfigInput) (ClaudeCLIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ClaudeCLIConfigDTO{}, err
	}
	if id <= 0 || expectedVersion <= 0 {
		return ClaudeCLIConfigDTO{}, ErrStaticInvalidCommand
	}
	var result ClaudeCLIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.UpdateClaudeCLIConfig(ctx, id, expectedVersion, input, service.timestamp(current.UpdatedAt))
		if err == nil {
			result = claudeConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) DeleteClaudeCLIConfig(ctx context.Context, id, expectedVersion int64) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if id <= 0 || expectedVersion <= 0 {
		return ErrStaticInvalidCommand
	}
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		return transaction.DeleteClaudeCLIConfig(ctx, id, expectedVersion, service.timestamp(current.UpdatedAt))
	})
	return mapStaticError(err)
}

func (service *StaticService) SetDefaultClaudeCLIConfig(ctx context.Context, id, expectedVersion int64) (ClaudeCLIConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return ClaudeCLIConfigDTO{}, err
	}
	if id <= 0 || expectedVersion <= 0 {
		return ClaudeCLIConfigDTO{}, ErrStaticInvalidCommand
	}
	var result ClaudeCLIConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		current, found, err := transaction.GetClaudeCLIConfig(ctx, id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return repository.ErrNotFound
		}
		record, err := transaction.SetDefaultClaudeCLIConfig(ctx, id, expectedVersion, service.timestamp(current.UpdatedAt))
		if err == nil {
			result = claudeConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) GetMCPConfig(ctx context.Context, environment map[string]string) (MCPConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return MCPConfigDTO{}, err
	}
	var result MCPConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.GetMCPConfig(ctx)
		if err == nil {
			result = mcpConfigDTO(domainconfig.ResolveMCPEnvironment(record, environment))
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) SaveMCPConfig(ctx context.Context, input domainconfig.MCPInput) (MCPConfigDTO, error) {
	if err := service.ready(ctx); err != nil {
		return MCPConfigDTO{}, err
	}
	var result MCPConfigDTO
	err := service.writer.TransactChat(ctx, func(transaction repository.ChatWriteTransaction) error {
		record, err := transaction.SaveMCPConfig(ctx, input)
		if err == nil {
			result = mcpConfigDTO(record)
		}
		return err
	})
	return result, mapStaticError(err)
}

func (service *StaticService) StartMCP(context.Context) error { return ErrStaticRuntimeDisabled }
func (service *StaticService) StopMCP(context.Context) error  { return ErrStaticRuntimeDisabled }

func aiConfigDTO(value domainconfig.AIConfig) AIConfigDTO {
	return AIConfigDTO{ID: value.ID, ProjectID: cloneID(value.ProjectID), ProjectId: cloneID(value.ProjectID), Name: value.Name,
		Provider: value.Provider, BaseURL: value.BaseURL, BaseUrl: value.BaseURL, HasAPIKey: value.HasAPIKey,
		HasApiKey: value.HasAPIKey, MaskedKey: value.MaskedAPIKey, MaskedKeyCamel: value.MaskedAPIKey,
		Model: value.Model, Temperature: value.Temperature, ThinkingDepth: cloneText(value.ThinkingDepth),
		ThinkingDepthCamel: cloneText(value.ThinkingDepth), ThinkingBudgetTokens: cloneID(value.ThinkingBudgetTokens),
		ThinkingBudgetTokensCamel: cloneID(value.ThinkingBudgetTokens), CreatedAt: value.CreatedAt, CreatedAtCamel: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, UpdatedAtCamel: value.UpdatedAt, Version: value.Version}
}

func claudeConfigDTO(value domainconfig.ClaudeCLIConfig) ClaudeCLIConfigDTO {
	return ClaudeCLIConfigDTO{ID: value.ID, ProjectID: cloneID(value.ProjectID), ProjectId: cloneID(value.ProjectID), Name: value.Name,
		BaseURL: value.BaseURL, BaseUrl: value.BaseURL, HasAuthToken: value.HasAuthToken, HasAuthTokenCamel: value.HasAuthToken,
		MaskedKey: value.MaskedAuthToken, MaskedKeyCamel: value.MaskedAuthToken, Model: value.Model,
		IsDefault: value.IsDefault, IsDefaultCamel: value.IsDefault, CreatedAt: value.CreatedAt, CreatedAtCamel: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, UpdatedAtCamel: value.UpdatedAt, Version: value.Version}
}

func mcpConfigDTO(value domainconfig.MCPConfig) MCPConfigDTO {
	return MCPConfigDTO{Enabled: value.Enabled, Transport: value.Transport, Host: value.Host, Port: value.Port,
		Path: value.Path, PortExplicit: value.PortExplicit, PortExplicitCamel: value.PortExplicit,
		HasAuthToken: value.HasAuthToken, HasAuthTokenCamel: value.HasAuthToken,
		AuthTokenMasked: value.MaskedAuthToken, AuthTokenMaskedCamel: value.MaskedAuthToken}
}

func cloneText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func mapStaticError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrVersionConflict) || errors.Is(err, repository.ErrDuplicate) {
		return ErrStaticConflict
	}
	if errors.Is(err, repository.ErrInvalidAutomation) || errors.Is(err, domainconfig.ErrInvalidAIConfig) ||
		errors.Is(err, domainconfig.ErrInvalidClaudeConfig) || errors.Is(err, domainconfig.ErrInvalidMCPConfig) {
		return ErrStaticInvalidCommand
	}
	return err
}
