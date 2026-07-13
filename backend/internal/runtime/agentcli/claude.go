package agentcli

import "context"

type claudeAdapter struct{}

func (claudeAdapter) Provider() Provider { return ProviderClaude }

func (claudeAdapter) Prepare(_ context.Context, request Request, _ ArtifactWriter) (Prepared, error) {
	command, err := resolvedCommand(ProviderClaude, request.Command)
	if err != nil {
		return Prepared{}, err
	}
	session, err := normalizeSession(ProviderClaude, request.Session)
	if err != nil {
		return Prepared{}, err
	}
	arguments := []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	switch session.Mode {
	case SessionContinue:
		arguments = append(arguments, "--continue")
	case SessionResume:
		arguments = append(arguments, "--resume", session.ID)
	case SessionSpecified:
		arguments = append(arguments, "--session-id", session.ID)
	}
	environment := make(map[string]string, 2)
	if request.ClaudeBaseURL != "" {
		environment["ANTHROPIC_BASE_URL"] = request.ClaudeBaseURL
	}
	if request.ClaudeModel != "" {
		environment["ANTHROPIC_MODEL"] = request.ClaudeModel
	}
	return Prepared{
		Executable: command, Arguments: arguments, PromptMode: PromptStdin, Prompt: request.Prompt,
		Environment: environment, Parser: ParserClaude, Session: session,
	}, nil
}
