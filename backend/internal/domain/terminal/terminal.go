// Package terminal defines the safe, transport-neutral Terminal projection.
// Raw PTY handles, input, output and environment values intentionally do not
// belong to this package: they remain inside the runtime and bounded in-memory
// application buffer.
package terminal

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidCommand = errors.New("terminal command is invalid")
	ErrUnavailable    = errors.New("terminal service is unavailable")
	ErrForbidden      = errors.New("terminal caller is not authorized")
	ErrNotFound       = errors.New("terminal session not found")
	ErrClosed         = errors.New("terminal session is closed")
	ErrReplayGap      = errors.New("terminal replay gap")
	ErrCursorTooOld   = errors.New("terminal replay cursor is invalid")
	ErrSlowConsumer   = errors.New("terminal subscriber is too slow")
)

type ErrorCode string

const (
	CodeUnavailable  ErrorCode = "terminal_pty_unavailable"
	CodeInvalid      ErrorCode = "terminal_invalid_payload"
	CodeForbidden    ErrorCode = "terminal_forbidden"
	CodeNotFound     ErrorCode = "terminal_session_not_found"
	CodeClosed       ErrorCode = "terminal_session_not_found"
	CodeReplayGap    ErrorCode = "terminal_replay_gap"
	CodeCursorTooOld ErrorCode = "terminal_cursor_too_old"
	CodeSlowConsumer ErrorCode = "terminal_slow_consumer"
)

func CodeOf(err error) ErrorCode {
	switch {
	case errors.Is(err, ErrInvalidCommand):
		return CodeInvalid
	case errors.Is(err, ErrForbidden):
		return CodeForbidden
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrClosed):
		return CodeClosed
	case errors.Is(err, ErrReplayGap):
		return CodeReplayGap
	case errors.Is(err, ErrCursorTooOld):
		return CodeCursorTooOld
	case errors.Is(err, ErrSlowConsumer):
		return CodeSlowConsumer
	default:
		return CodeUnavailable
	}
}

const (
	RuntimeGo = "go"

	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusExited   = "exited"
	StatusKilled   = "killed"
	StatusError    = "error"

	EventOutput = "output"
	EventExit   = "exit"
	EventStatus = "status"
	EventClosed = "closed"
)

// Caller comes from an authenticated inbound adapter. Its ID is deliberately
// opaque: an ID, localhost origin or terminal session ID never grants access.
type Caller struct {
	ID string
}

func (caller Caller) Valid() bool {
	return strings.TrimSpace(caller.ID) != "" && caller.ID == strings.TrimSpace(caller.ID) && len(caller.ID) <= 256
}

// Profile is the safe read projection of a configured profile. Environment is
// intentionally absent; wire adapters may render the frozen empty env object.
type Profile struct {
	ID        string
	Name      string
	Kind      string
	ShellPath string
	Args      []string
}

func (profile Profile) Copy() Profile {
	profile.Args = append([]string(nil), profile.Args...)
	return profile
}

func (profile Profile) Valid() bool {
	if profile.ID == "" || len(profile.ID) > 80 || profile.Name == "" || len(profile.Name) > 80 ||
		(profile.Kind != "default" && profile.Kind != "custom") || profile.ShellPath == "" || len(profile.ShellPath) > 2048 || len(profile.Args) > 32 {
		return false
	}
	for _, arg := range profile.Args {
		if !utf8.ValidString(arg) || len(arg) > 512 || strings.ContainsAny(arg, "\x00\r\n") {
			return false
		}
	}
	return true
}

// Session is the only read model that may leave the application service after
// project/caller authorization. It contains neither PID, PTY handle, raw I/O
// nor environment values.
type Session struct {
	ID        string
	ProjectID int64
	Title     string
	CWD       string
	Shell     string
	Status    string
	CreatedAt time.Time
	EndedAt   *time.Time
	ExitCode  *int
	Cols      int
	Rows      int
	Profile   Profile
	Closed    bool
	Runtime   string
}

func (session Session) Copy() Session {
	session.Profile.Args = append([]string(nil), session.Profile.Args...)
	if session.EndedAt != nil {
		value := *session.EndedAt
		session.EndedAt = &value
	}
	if session.ExitCode != nil {
		value := *session.ExitCode
		session.ExitCode = &value
	}
	return session
}

type Output struct {
	Seq  uint64
	Data string
}

type Replay struct {
	Session  Session
	FirstSeq uint64
	LastSeq  uint64
	Entries  []Output
}

func (replay Replay) Copy() Replay {
	replay.Session = replay.Session.Copy()
	replay.Entries = append([]Output(nil), replay.Entries...)
	return replay
}

// Event is delivered only through a caller-authorized terminal subscription.
// Output contains raw data and must never be published into generic events,
// persistence, logs, audits, snapshots or project SSE.
type Event struct {
	Type    string
	Output  *Output
	Session *Session
	Exit    *Exit
}

func (event Event) Copy() Event {
	if event.Output != nil {
		value := *event.Output
		event.Output = &value
	}
	if event.Session != nil {
		value := event.Session.Copy()
		event.Session = &value
	}
	if event.Exit != nil {
		value := event.Exit.Copy()
		event.Exit = &value
	}
	return event
}

type Exit struct {
	Code     *int
	Signal   string
	TimedOut bool
}

func (exit Exit) Copy() Exit {
	if exit.Code != nil {
		value := *exit.Code
		exit.Code = &value
	}
	return exit
}
