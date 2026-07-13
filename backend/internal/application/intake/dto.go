package intake

import (
	"net/url"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
)

type LinkedPlanDTO struct {
	LinkID     *int64 `json:"link_id"`
	PlanID     int64  `json:"plan_id"`
	PhaseIndex int64  `json:"phase_index"`
	PhaseTitle string `json:"phase_title"`
}

type IntakeDTO struct {
	ID                                 int64               `json:"id"`
	ProjectID                          int64               `json:"project_id"`
	Type                               domainintake.Type   `json:"intake_type"`
	RequirementID                      *int64              `json:"requirement_id"`
	Title                              string              `json:"title"`
	Body                               string              `json:"body"`
	Status                             domainintake.Status `json:"status"`
	AcceptedAt                         *string             `json:"accepted_at"`
	LinkedPlanID                       *int64              `json:"linked_plan_id"`
	LinkedPlans                        []LinkedPlanDTO     `json:"linked_plans"`
	CreatedAt                          string              `json:"created_at"`
	UpdatedAt                          string              `json:"updated_at"`
	AgentCLIProvider                   *string             `json:"agent_cli_provider"`
	AgentCLICommand                    string              `json:"agent_cli_command"`
	CodexReasoningEffort               *string             `json:"codex_reasoning_effort"`
	PlanGenerationStrategy             *string             `json:"plan_generation_strategy"`
	PlanGenerationProvider             *string             `json:"plan_generation_provider"`
	PlanGenerationCommand              string              `json:"plan_generation_command"`
	PlanGenerationModel                string              `json:"plan_generation_model"`
	PlanGenerationCodexReasoningEffort *string             `json:"plan_generation_codex_reasoning_effort"`
	PlanGenerationClaudeBaseURL        string              `json:"plan_generation_claude_base_url"`
	PlanGenerationClaudeModel          string              `json:"plan_generation_claude_model"`
	PlanGenerationClaudeConfigID       int64               `json:"plan_generation_claude_config_id"`
	PlanGenerationHasClaudeAuthToken   bool                `json:"plan_generation_has_claude_auth_token"`
	GenerateFailCount                  int64               `json:"generate_fail_count"`
	LastGenerateFailAt                 *string             `json:"last_generate_fail_at"`
	LastGenerateError                  *string             `json:"last_generate_error"`
	LastGenerateAgentCLIProvider       *string             `json:"last_generate_agent_cli_provider"`
	LastGenerateCodexReasoningEffort   *string             `json:"last_generate_codex_reasoning_effort"`
}

type AttachmentDTO struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Size        int64  `json:"size"`
	MIMEType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}

type CleanupDTO struct {
	Status  string `json:"status"`
	Total   int64  `json:"total"`
	Deleted int64  `json:"deleted"`
	Missing int64  `json:"missing"`
	Pending int64  `json:"pending"`
	Code    string `json:"code,omitempty"`
}

type MutationResult struct {
	Snapshot contracts.AppSnapshot `json:"snapshot"`
	Cleanup  *CleanupDTO           `json:"cleanup,omitempty"`
}

func intakeDTO(value domainintake.Intake, links []domainintake.PlanLink) IntakeDTO {
	linked := make([]LinkedPlanDTO, 0, len(links))
	for _, link := range links {
		var linkID *int64
		if link.ID > 0 {
			id := link.ID
			linkID = &id
		}
		linked = append(linked, LinkedPlanDTO{
			LinkID: linkID, PlanID: link.PlanID, PhaseIndex: link.PhaseIndex, PhaseTitle: link.PhaseTitle,
		})
	}
	return IntakeDTO{
		ID: value.ID, ProjectID: value.ProjectID, Type: value.Type,
		RequirementID: copyInt64(value.RequirementID), Title: value.Title, Body: value.Body,
		Status: value.Status, AcceptedAt: copyString(value.AcceptedAt),
		LinkedPlanID: copyInt64(value.LinkedPlanID), LinkedPlans: linked,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		AgentCLIProvider: copyString(value.AgentCLI.Provider), AgentCLICommand: value.AgentCLI.Command,
		CodexReasoningEffort:   copyString(value.AgentCLI.CodexReasoningEffort),
		PlanGenerationStrategy: copyString(value.PlanGeneration.Strategy),
		PlanGenerationProvider: copyString(value.PlanGeneration.Provider),
		PlanGenerationCommand:  value.PlanGeneration.Command, PlanGenerationModel: value.PlanGeneration.Model,
		PlanGenerationCodexReasoningEffort: copyString(value.PlanGeneration.CodexReasoningEffort),
		PlanGenerationClaudeBaseURL:        safeDTOBaseURL(value.PlanGeneration.ClaudeBaseURL),
		PlanGenerationClaudeModel:          value.PlanGeneration.ClaudeModel,
		PlanGenerationClaudeConfigID:       value.PlanGeneration.ClaudeConfigID,
		PlanGenerationHasClaudeAuthToken:   value.PlanGeneration.ClaudeAuthToken != "",
		GenerateFailCount:                  value.Failure.Count, LastGenerateFailAt: copyString(value.Failure.LastFailedAt),
		LastGenerateError:                safeFailure(value.Failure.LastError),
		LastGenerateAgentCLIProvider:     copyString(value.Failure.LastAgentCLIProvider),
		LastGenerateCodexReasoningEffort: copyString(value.Failure.LastCodexEffort),
	}
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func safeFailure(value *string) *string {
	if value == nil {
		return nil
	}
	redacted := "<redacted_error>"
	return &redacted
}

func safeDTOBaseURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.User, parsed.RawQuery, parsed.Fragment, parsed.RawFragment = nil, "", "", ""
	parsed.ForceQuery = false
	return parsed.String()
}
