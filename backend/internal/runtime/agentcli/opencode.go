package agentcli

import (
	"context"
	"fmt"
	"strings"

	filesapp "github.com/lyming99/autoplan/backend/internal/application/files"
)

type openCodeAdapter struct{}

func (openCodeAdapter) Provider() Provider { return ProviderOpenCode }

func (openCodeAdapter) Prepare(ctx context.Context, request Request, artifacts ArtifactWriter) (Prepared, error) {
	command, err := resolvedCommand(ProviderOpenCode, request.Command)
	if err != nil {
		return Prepared{}, err
	}
	session, err := normalizeSession(ProviderOpenCode, request.Session)
	if err != nil {
		return Prepared{}, err
	}
	arguments := []string{"run", "--format", "default", "--auto"}
	if session.Mode == SessionResume && session.ID != "" {
		arguments = append(arguments, "--session", session.ID)
	}
	title := session.Title
	if title == "" {
		title = normalizeSessionTitle(request.OpenCodeTitle)
	}
	if title == "" && request.PlanID > 0 {
		title = fmt.Sprintf("AutoPlan project %d plan %d", request.ProjectID, request.PlanID)
	}
	if title != "" {
		arguments = append(arguments, "--title", title)
		session.Title = title
	}
	agent := normalizeOpenCodeAgent(request.OpenCodeAgent)
	if request.PlanGeneration {
		if artifacts == nil {
			return Prepared{}, ErrControlledArtifact
		}
		agent, err = artifacts.EnsureOpenCodePlanAgent(ctx, request.Workspace)
		if err != nil || agent != filesapp.OpenCodePlanAgentName {
			return Prepared{}, ErrControlledArtifact
		}
	}
	if agent != "" {
		arguments = append(arguments, "--agent", agent)
	}
	prepared := Prepared{
		Executable: command, Arguments: arguments, PromptMode: PromptArgument, Parser: ParserOpenCode, Session: session,
	}
	if shouldAttachOpenCodePrompt(request.Prompt) {
		if artifacts == nil {
			return Prepared{}, ErrControlledArtifact
		}
		attachment, err := artifacts.WriteOpenCodePrompt(ctx, request.Workspace, request.Prompt)
		if err != nil {
			return Prepared{}, ErrControlledArtifact
		}
		prepared.Arguments = append(prepared.Arguments,
			"The complete instruction is attached. Read the attachment in full and follow it exactly.",
			"-f", attachment.Path,
		)
		prepared.Cleanup = func(cleanup context.Context) error { return artifacts.RemoveOpenCodePrompt(cleanup, attachment) }
		return prepared, nil
	}
	prepared.Arguments = append(prepared.Arguments, request.Prompt)
	return prepared, nil
}

func normalizeOpenCodeAgent(value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	if len(text) == 0 || len(text) > 128 {
		return ""
	}
	for index, character := range text {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' && index > 0) {
			return ""
		}
	}
	return text
}

func shouldAttachOpenCodePrompt(prompt string) bool {
	return len(prompt) > maximumInlinePrompt || strings.ContainsAny(prompt, "\r\n")
}
