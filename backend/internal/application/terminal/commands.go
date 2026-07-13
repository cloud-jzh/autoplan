package terminal

import (
	"strings"
	"unicode/utf8"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
)

const (
	defaultTitle   = "Terminal"
	maximumTitle   = 80
	maximumPath    = 2048
	maximumInput   = 64 << 10
	minimumColumns = 2
	maximumColumns = 500
	minimumRows    = 1
	maximumRows    = 200
)

// CreateCommand intentionally carries only write-only environment input. The
// service forwards it directly to the constrained PTY factory and never keeps
// it in a record, replay buffer, event, audit record or read DTO.
type CreateCommand struct {
	Caller      domainterminal.Caller
	ProjectID   int64
	CWD         string
	ProfileID   string
	Environment map[string]string
	Title       string
	Cols        int
	Rows        int
}

func (command CreateCommand) valid() bool {
	return command.Caller.Valid() && command.ProjectID > 0 && validPath(command.CWD) &&
		validProfileID(command.ProfileID) && validTitle(command.Title, true) && validSize(command.Cols, command.Rows) &&
		validEnvironment(command.Environment)
}

type SessionCommand struct {
	Caller    domainterminal.Caller
	ProjectID int64
	SessionID string
}

func (command SessionCommand) valid() bool {
	return command.Caller.Valid() && command.ProjectID > 0 && validSessionID(command.SessionID)
}

type WriteCommand struct {
	SessionCommand
	Data string
}

func (command WriteCommand) valid() bool {
	return command.SessionCommand.valid() && command.Data != "" && utf8.ValidString(command.Data) && len(command.Data) <= maximumInput
}

type ResizeCommand struct {
	SessionCommand
	Cols int
	Rows int
}

func (command ResizeCommand) valid() bool {
	return command.SessionCommand.valid() && validSize(command.Cols, command.Rows)
}

type RenameCommand struct {
	SessionCommand
	Title string
}

func (command RenameCommand) valid() bool {
	return command.SessionCommand.valid() && validTitle(command.Title, false)
}

type ReplayCommand struct {
	SessionCommand
	LastSeq uint64
}

func (command ReplayCommand) valid() bool { return command.SessionCommand.valid() }

type AttachCommand struct {
	ReplayCommand
}

func (command AttachCommand) valid() bool { return command.ReplayCommand.valid() }

func validPath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximumPath && !strings.ContainsRune(value, 0)
}

func validProfileID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validTitle(value string, permitEmpty bool) bool {
	if permitEmpty && value == "" {
		return true
	}
	return value == strings.TrimSpace(value) && value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximumTitle && !strings.ContainsRune(value, 0)
}

func validSize(columns, rows int) bool {
	return columns >= minimumColumns && columns <= maximumColumns && rows >= minimumRows && rows <= maximumRows
}

func validSessionID(value string) bool {
	if !strings.HasPrefix(value, "term_") || len(value) > 160 || len(value) < 11 {
		return false
	}
	for index, character := range value[len("term_"):] {
		if index == 0 && !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func validEnvironment(values map[string]string) bool {
	if len(values) > 64 {
		return false
	}
	for name, value := range values {
		if name == "" || len(name) > 128 || !utf8.ValidString(name) || !utf8.ValidString(value) || len(value) > 8<<10 ||
			strings.ContainsAny(name, "\x00\r\n=") || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}
