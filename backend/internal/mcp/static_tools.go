// Package mcp exposes authorization-ready static tools. Registration and
// transport startup remain separate; this adapter owns no listener, file,
// process, repository, or secret-provider capability.
package mcp

import (
	"context"
	"errors"

	applicationautomation "github.com/lyming99/autoplan/backend/internal/application/automation"
	applicationchat "github.com/lyming99/autoplan/backend/internal/application/chat"
	applicationconfig "github.com/lyming99/autoplan/backend/internal/application/config"
	domainautomation "github.com/lyming99/autoplan/backend/internal/domain/automation"
	domainchat "github.com/lyming99/autoplan/backend/internal/domain/chat"
	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type StaticAutomationApplication interface {
	ListScripts(context.Context, applicationautomation.ListQuery) ([]applicationautomation.ScriptDTO, error)
	GetScript(context.Context, int64, int64) (applicationautomation.ScriptDTO, error)
	CreateScript(context.Context, applicationautomation.CreateScriptCommand) (applicationautomation.ScriptDTO, error)
	UpdateScript(context.Context, applicationautomation.UpdateScriptCommand) (applicationautomation.ScriptDTO, error)
	DeleteScript(context.Context, int64, int64, int64) (applicationautomation.ScriptDTO, error)
	ToggleScript(context.Context, int64, int64, int64) (applicationautomation.ScriptDTO, error)
	ListExecutors(context.Context, applicationautomation.ListQuery) ([]applicationautomation.ExecutorDTO, error)
	GetExecutor(context.Context, int64, int64) (applicationautomation.ExecutorDTO, error)
	CreateExecutor(context.Context, applicationautomation.CreateExecutorCommand) (applicationautomation.ExecutorDTO, error)
	UpdateExecutor(context.Context, applicationautomation.UpdateExecutorCommand) (applicationautomation.ExecutorDTO, error)
	DeleteExecutor(context.Context, int64, int64, int64) (applicationautomation.ExecutorDTO, error)
	ToggleExecutor(context.Context, int64, int64, int64) (applicationautomation.ExecutorDTO, error)
}

type StaticChatApplication interface {
	ListConversations(context.Context, domainchat.ConversationListOptions) (applicationchat.ConversationPage, error)
	GetConversation(context.Context, int64, int64) (applicationchat.ConversationDTO, error)
	CreateConversation(context.Context, applicationchat.CreateConversationCommand) (applicationchat.ConversationDTO, error)
	UpdateConversation(context.Context, applicationchat.UpdateConversationCommand) (applicationchat.ConversationDTO, error)
	DeleteConversation(context.Context, int64, int64) (int64, error)
	ListHistory(context.Context, domainchat.MessageListOptions) (applicationchat.MessagePage, error)
}

type StaticConfigApplication interface {
	ListAIConfigs(context.Context) ([]applicationconfig.AIConfigDTO, error)
	CreateAIConfig(context.Context, domainconfig.AIConfigInput) (applicationconfig.AIConfigDTO, error)
	UpdateAIConfig(context.Context, int64, int64, domainconfig.AIConfigInput) (applicationconfig.AIConfigDTO, error)
	DeleteAIConfig(context.Context, int64, int64) error
	ListClaudeCLIConfigs(context.Context) ([]applicationconfig.ClaudeCLIConfigDTO, error)
	CreateClaudeCLIConfig(context.Context, domainconfig.ClaudeCLIConfigInput) (applicationconfig.ClaudeCLIConfigDTO, error)
	UpdateClaudeCLIConfig(context.Context, int64, int64, domainconfig.ClaudeCLIConfigInput) (applicationconfig.ClaudeCLIConfigDTO, error)
	DeleteClaudeCLIConfig(context.Context, int64, int64) error
	SetDefaultClaudeCLIConfig(context.Context, int64, int64) (applicationconfig.ClaudeCLIConfigDTO, error)
	GetMCPConfig(context.Context, map[string]string) (applicationconfig.MCPConfigDTO, error)
	SaveMCPConfig(context.Context, domainconfig.MCPInput) (applicationconfig.MCPConfigDTO, error)
}

var (
	_ StaticAutomationApplication = (*applicationautomation.Service)(nil)
	_ StaticChatApplication       = (*applicationchat.Service)(nil)
	_ StaticConfigApplication     = (*applicationconfig.StaticService)(nil)
)

type StaticDependencies struct {
	Automation StaticAutomationApplication
	Chat       StaticChatApplication
	Config     StaticConfigApplication
}

type StaticTools struct {
	automation StaticAutomationApplication
	chat       StaticChatApplication
	config     StaticConfigApplication
}

func NewStaticTools(dependencies StaticDependencies) *StaticTools {
	return &StaticTools{automation: dependencies.Automation, chat: dependencies.Chat, config: dependencies.Config}
}

// Names is safe capability metadata for a registration layer. It does not
// imply that the MCP listener is running.
func (tools *StaticTools) Names() []string {
	return []string{
		"automation.list_scripts", "automation.get_script", "automation.create_script", "automation.update_script", "automation.delete_script", "automation.toggle_script",
		"automation.list_executors", "automation.get_executor", "automation.create_executor", "automation.update_executor", "automation.delete_executor", "automation.toggle_executor",
		"chat.list_conversations", "chat.get_conversation", "chat.create_conversation", "chat.update_conversation", "chat.delete_conversation", "chat.list_messages",
		"config.list_ai", "config.create_ai", "config.update_ai", "config.delete_ai", "config.list_claude", "config.create_claude", "config.update_claude", "config.delete_claude", "config.set_claude_default", "config.get_mcp", "config.save_mcp",
	}
}

type StaticListRequest struct {
	ProjectID int64
	Limit     int
	Offset    int
}
type StaticItemRequest struct {
	ProjectID int64
	ID        int64
	Version   int64
}
type StaticConversationRequest struct {
	ProjectID      int64
	ConversationID int64
	Limit          int
	Cursor         string
}

func (tools *StaticTools) ListScripts(ctx context.Context, input StaticListRequest) ([]applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.ListScripts(ctx, applicationautomation.ListQuery{ProjectID: input.ProjectID, Limit: input.Limit, Offset: input.Offset})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) GetScript(ctx context.Context, input StaticItemRequest) (applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ScriptDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.GetScript(ctx, input.ProjectID, input.ID)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) CreateScript(ctx context.Context, input StaticListRequest, value domainautomation.ScriptInput) (applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ScriptDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.CreateScript(ctx, applicationautomation.CreateScriptCommand{ProjectID: input.ProjectID, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) UpdateScript(ctx context.Context, input StaticItemRequest, value domainautomation.ScriptInput) (applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ScriptDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.UpdateScript(ctx, applicationautomation.UpdateScriptCommand{ProjectID: input.ProjectID, ScriptID: input.ID, ExpectedVersion: input.Version, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) DeleteScript(ctx context.Context, input StaticItemRequest) (applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ScriptDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.DeleteScript(ctx, input.ProjectID, input.ID, input.Version)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) ToggleScript(ctx context.Context, input StaticItemRequest) (applicationautomation.ScriptDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ScriptDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.ToggleScript(ctx, input.ProjectID, input.ID, input.Version)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) ListExecutors(ctx context.Context, input StaticListRequest) ([]applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.ListExecutors(ctx, applicationautomation.ListQuery{ProjectID: input.ProjectID, Limit: input.Limit, Offset: input.Offset})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) GetExecutor(ctx context.Context, input StaticItemRequest) (applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ExecutorDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.GetExecutor(ctx, input.ProjectID, input.ID)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) CreateExecutor(ctx context.Context, input StaticListRequest, value domainautomation.ExecutorInput) (applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ExecutorDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.CreateExecutor(ctx, applicationautomation.CreateExecutorCommand{ProjectID: input.ProjectID, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) UpdateExecutor(ctx context.Context, input StaticItemRequest, value domainautomation.ExecutorInput) (applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ExecutorDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.UpdateExecutor(ctx, applicationautomation.UpdateExecutorCommand{ProjectID: input.ProjectID, ExecutorID: input.ID, ExpectedVersion: input.Version, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) DeleteExecutor(ctx context.Context, input StaticItemRequest) (applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ExecutorDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.DeleteExecutor(ctx, input.ProjectID, input.ID, input.Version)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) ToggleExecutor(ctx context.Context, input StaticItemRequest) (applicationautomation.ExecutorDTO, error) {
	if tools == nil || tools.automation == nil {
		return applicationautomation.ExecutorDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.automation.ToggleExecutor(ctx, input.ProjectID, input.ID, input.Version)
	return result, mapStaticToolError(err)
}

func (tools *StaticTools) ListConversations(ctx context.Context, input StaticConversationRequest) (applicationchat.ConversationPage, error) {
	if tools == nil || tools.chat == nil {
		return applicationchat.ConversationPage{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.ListConversations(ctx, domainchat.ConversationListOptions{ProjectID: input.ProjectID, Limit: input.Limit, Cursor: input.Cursor})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) GetConversation(ctx context.Context, input StaticConversationRequest) (applicationchat.ConversationDTO, error) {
	if tools == nil || tools.chat == nil {
		return applicationchat.ConversationDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.GetConversation(ctx, input.ProjectID, input.ConversationID)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) CreateConversation(ctx context.Context, input StaticConversationRequest, value domainchat.ConversationInput) (applicationchat.ConversationDTO, error) {
	if tools == nil || tools.chat == nil {
		return applicationchat.ConversationDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.CreateConversation(ctx, applicationchat.CreateConversationCommand{ProjectID: input.ProjectID, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) UpdateConversation(ctx context.Context, input StaticConversationRequest, value domainchat.ConversationInput) (applicationchat.ConversationDTO, error) {
	if tools == nil || tools.chat == nil {
		return applicationchat.ConversationDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.UpdateConversation(ctx, applicationchat.UpdateConversationCommand{ProjectID: input.ProjectID, ConversationID: input.ConversationID, Input: value})
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) DeleteConversation(ctx context.Context, input StaticConversationRequest) (int64, error) {
	if tools == nil || tools.chat == nil {
		return 0, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.DeleteConversation(ctx, input.ProjectID, input.ConversationID)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) ListMessages(ctx context.Context, input StaticConversationRequest) (applicationchat.MessagePage, error) {
	if tools == nil || tools.chat == nil {
		return applicationchat.MessagePage{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.chat.ListHistory(ctx, domainchat.MessageListOptions{ProjectID: input.ProjectID, ConversationID: input.ConversationID, Limit: input.Limit, Cursor: input.Cursor})
	return result, mapStaticToolError(err)
}

func (tools *StaticTools) ListAIConfigs(ctx context.Context) ([]applicationconfig.AIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.ListAIConfigs(ctx)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) CreateAIConfig(ctx context.Context, value domainconfig.AIConfigInput) (applicationconfig.AIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.AIConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.CreateAIConfig(ctx, value)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) UpdateAIConfig(ctx context.Context, input StaticItemRequest, value domainconfig.AIConfigInput) (applicationconfig.AIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.AIConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.UpdateAIConfig(ctx, input.ID, input.Version, value)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) DeleteAIConfig(ctx context.Context, input StaticItemRequest) error {
	if tools == nil || tools.config == nil {
		return ToolError{Code: "service_unavailable"}
	}
	return mapStaticToolError(tools.config.DeleteAIConfig(ctx, input.ID, input.Version))
}
func (tools *StaticTools) ListClaudeConfigs(ctx context.Context) ([]applicationconfig.ClaudeCLIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return nil, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.ListClaudeCLIConfigs(ctx)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) CreateClaudeConfig(ctx context.Context, value domainconfig.ClaudeCLIConfigInput) (applicationconfig.ClaudeCLIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.ClaudeCLIConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.CreateClaudeCLIConfig(ctx, value)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) UpdateClaudeConfig(ctx context.Context, input StaticItemRequest, value domainconfig.ClaudeCLIConfigInput) (applicationconfig.ClaudeCLIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.ClaudeCLIConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.UpdateClaudeCLIConfig(ctx, input.ID, input.Version, value)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) DeleteClaudeConfig(ctx context.Context, input StaticItemRequest) error {
	if tools == nil || tools.config == nil {
		return ToolError{Code: "service_unavailable"}
	}
	return mapStaticToolError(tools.config.DeleteClaudeCLIConfig(ctx, input.ID, input.Version))
}
func (tools *StaticTools) SetDefaultClaudeConfig(ctx context.Context, input StaticItemRequest) (applicationconfig.ClaudeCLIConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.ClaudeCLIConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.SetDefaultClaudeCLIConfig(ctx, input.ID, input.Version)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) GetMCPConfig(ctx context.Context) (applicationconfig.MCPConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.MCPConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.GetMCPConfig(ctx, nil)
	return result, mapStaticToolError(err)
}
func (tools *StaticTools) SaveMCPConfig(ctx context.Context, value domainconfig.MCPInput) (applicationconfig.MCPConfigDTO, error) {
	if tools == nil || tools.config == nil {
		return applicationconfig.MCPConfigDTO{}, ToolError{Code: "service_unavailable"}
	}
	result, err := tools.config.SaveMCPConfig(ctx, value)
	return result, mapStaticToolError(err)
}

func mapStaticToolError(err error) error {
	if err == nil {
		return nil
	}
	var tool ToolError
	if errors.As(err, &tool) {
		return tool
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ToolError{Code: "request_timeout"}
	case errors.Is(err, applicationautomation.ErrInvalidCommand), errors.Is(err, applicationchat.ErrInvalidCommand), errors.Is(err, applicationconfig.ErrStaticInvalidCommand), errors.Is(err, domainautomation.ErrInvalidScript), errors.Is(err, domainautomation.ErrInvalidExecutor), errors.Is(err, domainchat.ErrInvalidConversation), errors.Is(err, domainchat.ErrInvalidCursor), errors.Is(err, domainconfig.ErrInvalidAIConfig), errors.Is(err, domainconfig.ErrInvalidClaudeConfig), errors.Is(err, domainconfig.ErrInvalidMCPConfig):
		return ToolError{Code: "invalid_request"}
	case errors.Is(err, applicationautomation.ErrStateConflict), errors.Is(err, applicationchat.ErrStateConflict), errors.Is(err, applicationconfig.ErrStaticConflict), errors.Is(err, repository.ErrVersionConflict):
		return ToolError{Code: "precondition_failed"}
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrProjectMismatch):
		return ToolError{Code: "not_found"}
	case errors.Is(err, applicationautomation.ErrUnavailable), errors.Is(err, applicationchat.ErrUnavailable), errors.Is(err, applicationconfig.ErrStaticUnavailable), errors.Is(err, repository.ErrNotConfigured), errors.Is(err, repository.ErrClosed):
		return ToolError{Code: "service_unavailable"}
	default:
		return ToolError{Code: "internal_error"}
	}
}
