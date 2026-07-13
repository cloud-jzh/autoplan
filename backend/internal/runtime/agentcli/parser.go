package agentcli

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/runtime/process"
)

type ParserKind string

const (
	ParserCodex    ParserKind = "codex"
	ParserClaude   ParserKind = "claude-stream-json"
	ParserOpenCode ParserKind = "opencode"
	ParserOhMyPi   ParserKind = "oh-my-pi"
)

type parsedOutput struct {
	SessionID      string
	SessionMissing bool
	ParseFailed    bool
}

var codexSessionPattern = regexp.MustCompile(`(?im)(?:session\s+id:\s*|"(?:session_id|sessionId)"\s*:\s*"|(?:session_id|sessionId)\s*[:=]\s*)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
var codexResumeFailure = regexp.MustCompile(`(?i)(?:thread/resume|resume failed|no rollout found|session\s+(?:not\s+found|missing)|conversation\s+not\s+found|unknown\s+session|invalid\s+session)`)
var claudeSessionMissing = regexp.MustCompile(`(?i)(?:session\s+not\s+found|unknown\s+session|invalid\s+session|conversation\s+not\s+found|no\s+conversation)`)
var openCodeSessionMissing = regexp.MustCompile(`(?i)(?:session\s+not\s+found|unknown\s+session|invalid\s+session)`)

func parseOutput(_ Provider, kind ParserKind, result process.Result) parsedOutput {
	text := safeProcessText(result)
	parsed := parsedOutput{}
	switch kind {
	case ParserCodex:
		if match := codexSessionPattern.FindStringSubmatch(text); len(match) == 2 {
			parsed.SessionID = normalizeSessionID(ProviderCodex, match[1])
		}
		parsed.SessionMissing = codexResumeFailure.MatchString(text)
	case ParserClaude:
		parsed = parseClaudeStream(text)
		parsed.SessionMissing = parsed.SessionMissing || claudeSessionMissing.MatchString(text)
	case ParserOpenCode:
		parsed.SessionMissing = openCodeSessionMissing.MatchString(text)
	case ParserOhMyPi:
		// oh-my-pi deliberately has no persistent session contract.
	}
	return parsed
}

func parseClaudeStream(text string) parsedOutput {
	result := parsedOutput{}
	seenJSON := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		seenJSON = true
		if event.Type == "result" {
			result.SessionID = normalizeSessionID(ProviderClaude, event.SessionID)
		}
	}
	if strings.TrimSpace(text) != "" && !seenJSON {
		result.ParseFailed = true
	}
	return result
}

type openCodeSessionRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
}

func parseOpenCodeSessions(result process.Result, title string) (string, bool) {
	text := strings.TrimSpace(safeProcessText(result))
	if text == "" || result.ExitCode != 0 || result.TimedOut || result.Cancelled || result.Stdout.Truncated || result.Stderr.Truncated {
		return "", false
	}
	var entries []openCodeSessionRecord
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.Title == title {
			if id := normalizeSessionID(ProviderOpenCode, entry.ID); id != "" {
				return id, true
			}
		}
	}
	return "", true
}

func safeProcessText(result process.Result) string {
	// Process.Result has already bounded and redacted both streams. Parser input
	// remains internal and is never copied into an Agent CLI Result or event.
	return result.Stdout.Tail + "\n" + result.Stderr.Tail
}
