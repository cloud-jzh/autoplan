package config

import (
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultTerminalSessionsGlobal     = 32
	DefaultTerminalSessionsPerProject = 8
	DefaultTerminalSessionRuntime     = 4 * time.Hour
	DefaultTerminalGracePeriod        = 5 * time.Second
	DefaultTerminalInputBytes         = 64 << 10
	DefaultTerminalInputRateBytes     = 256 << 10
	DefaultTerminalInputRateWindow    = 10 * time.Second
	DefaultTerminalResizeRate         = 20
	DefaultTerminalResizeRateWindow   = 10 * time.Second
	DefaultTerminalReadChunkBytes     = 64 << 10
	DefaultTerminalEnvironmentEntries = 64
	DefaultTerminalEnvironmentBytes   = 128 << 10
	DefaultTerminalEnvironmentValue   = 8 << 10
	DefaultTerminalArguments          = 32
	DefaultTerminalArgumentBytes      = 512
	DefaultTerminalColumns            = 80
	DefaultTerminalRows               = 24
	DefaultTerminalConnectionsGlobal  = 128
	DefaultTerminalConnectionsSession = 4
	DefaultTerminalSendQueueFrames    = 64
	DefaultTerminalSendQueueBytes     = 1 << 20
	DefaultTerminalPingInterval       = 15 * time.Second
	DefaultTerminalPongGrace          = 10 * time.Second
	DefaultTerminalReadDeadline       = 30 * time.Second
	DefaultTerminalWriteDeadline      = 10 * time.Second
	DefaultTerminalWebSocketBytes     = 64 << 10
	TerminalMinimumColumns            = 2
	TerminalMaximumColumns            = 500
	TerminalMinimumRows               = 1
	TerminalMaximumRows               = 200

	MaximumTerminalSessionsGlobal     = 256
	MaximumTerminalSessionsPerProject = 64
	MaximumTerminalSessionRuntime     = 8 * time.Hour
	MaximumTerminalGracePeriod        = time.Minute
	MaximumTerminalInputBytes         = 1 << 20
	MaximumTerminalInputRateBytes     = 2 << 20
	MaximumTerminalInputRateWindow    = time.Minute
	MaximumTerminalResizeRate         = 120
	MaximumTerminalResizeRateWindow   = time.Minute
	MaximumTerminalReadChunkBytes     = 1 << 20
	MaximumTerminalEnvironmentEntries = 128
	MaximumTerminalEnvironmentBytes   = 1 << 20
	MaximumTerminalEnvironmentValue   = 64 << 10
	MaximumTerminalProfiles           = 16
	MaximumTerminalConnectionsGlobal  = 1024
	MaximumTerminalConnectionsSession = 16
	MaximumTerminalSendQueueFrames    = 256
	MaximumTerminalSendQueueBytes     = 4 << 20
	MaximumTerminalPingInterval       = time.Minute
	MaximumTerminalPongGrace          = 30 * time.Second
	MaximumTerminalReadDeadline       = 2 * time.Minute
	MaximumTerminalWriteDeadline      = time.Minute
	MaximumTerminalWebSocketBytes     = 1 << 20
)

// TerminalProfile is static launch configuration, not a renderer DTO. Values
// are copied only into the spawned PTY and are never eligible for persistence,
// audit bodies, snapshots or logs.
type TerminalProfile struct {
	ID          string
	Executable  string
	Args        []string
	Environment map[string]string
}

// TerminalRuntime defines finite, fail-closed limits for the P14 PTY layer.
// It intentionally does not appear in Config: P14 remains default-off until a
// later transport gate chooses to supply this isolated runtime dependency.
type TerminalRuntime struct {
	Profiles                 []TerminalProfile
	DefaultProfile           string
	AllowedEnvironment       []string
	MaxSessionsGlobal        int
	MaxSessionsPerProject    int
	MaxSessionRuntime        time.Duration
	GracePeriod              time.Duration
	MaxInputBytes            int
	MaxInputRateBytes        int
	InputRateWindow          time.Duration
	MaxResizeRate            int
	ResizeRateWindow         time.Duration
	ReadChunkBytes           int
	MaxEnvironmentEntries    int
	MaxEnvironmentBytes      int
	MaxEnvironmentValueBytes int
	MaxArguments             int
	MaxArgumentBytes         int
	DefaultCols              int
	DefaultRows              int
	MaxConnectionsGlobal     int
	MaxConnectionsPerSession int
	SendQueueFrames          int
	SendQueueBytes           int
	PingInterval             time.Duration
	PongGrace                time.Duration
	ReadDeadline             time.Duration
	WriteDeadline            time.Duration
	MaxWebSocketMessageBytes int
}

// DefaultTerminalRuntime contains only a fixed shell profile and a small
// environment-name allowlist. It does not inherit arbitrary host environment
// values or enable the Go Terminal feature by itself.
func DefaultTerminalRuntime() TerminalRuntime {
	profile := TerminalProfile{
		ID: "default", Executable: "/bin/sh", Args: []string{},
		Environment: map[string]string{"TERM": "xterm-256color"},
	}
	allowed := []string{"TERM"}
	if runtime.GOOS == "windows" {
		profile = TerminalProfile{ID: "default", Executable: "cmd.exe", Args: []string{}, Environment: map[string]string{}}
		allowed = []string{"ComSpec", "PATHEXT", "SystemRoot", "TEMP", "TMP"}
	}
	return TerminalRuntime{
		Profiles: profileSlice(profile), DefaultProfile: "default", AllowedEnvironment: allowed,
		MaxSessionsGlobal: DefaultTerminalSessionsGlobal, MaxSessionsPerProject: DefaultTerminalSessionsPerProject,
		MaxSessionRuntime: DefaultTerminalSessionRuntime, GracePeriod: DefaultTerminalGracePeriod,
		MaxInputBytes: DefaultTerminalInputBytes, MaxInputRateBytes: DefaultTerminalInputRateBytes,
		InputRateWindow: DefaultTerminalInputRateWindow, MaxResizeRate: DefaultTerminalResizeRate,
		ResizeRateWindow: DefaultTerminalResizeRateWindow, ReadChunkBytes: DefaultTerminalReadChunkBytes,
		MaxEnvironmentEntries: DefaultTerminalEnvironmentEntries, MaxEnvironmentBytes: DefaultTerminalEnvironmentBytes,
		MaxEnvironmentValueBytes: DefaultTerminalEnvironmentValue, MaxArguments: DefaultTerminalArguments,
		MaxArgumentBytes: DefaultTerminalArgumentBytes, DefaultCols: DefaultTerminalColumns, DefaultRows: DefaultTerminalRows,
		MaxConnectionsGlobal: DefaultTerminalConnectionsGlobal, MaxConnectionsPerSession: DefaultTerminalConnectionsSession,
		SendQueueFrames: DefaultTerminalSendQueueFrames, SendQueueBytes: DefaultTerminalSendQueueBytes,
		PingInterval: DefaultTerminalPingInterval, PongGrace: DefaultTerminalPongGrace,
		ReadDeadline: DefaultTerminalReadDeadline, WriteDeadline: DefaultTerminalWriteDeadline,
		MaxWebSocketMessageBytes: DefaultTerminalWebSocketBytes,
	}
}

func profileSlice(profile TerminalProfile) []TerminalProfile { return []TerminalProfile{profile} }

// Valid rejects unsafe launch profiles and any zero or unbounded resource
// setting. A caller must fix the configuration; no field is silently defaulted
// at PTY creation time.
func (value TerminalRuntime) Valid() bool {
	if len(value.Profiles) == 0 || len(value.Profiles) > MaximumTerminalProfiles ||
		value.DefaultProfile == "" || value.MaxSessionsGlobal <= 0 || value.MaxSessionsGlobal > MaximumTerminalSessionsGlobal ||
		value.MaxSessionsPerProject <= 0 || value.MaxSessionsPerProject > MaximumTerminalSessionsPerProject ||
		value.MaxSessionsPerProject > value.MaxSessionsGlobal ||
		value.MaxSessionRuntime <= 0 || value.MaxSessionRuntime > MaximumTerminalSessionRuntime ||
		value.GracePeriod <= 0 || value.GracePeriod > MaximumTerminalGracePeriod ||
		value.MaxInputBytes <= 0 || value.MaxInputBytes > MaximumTerminalInputBytes ||
		value.MaxInputRateBytes < value.MaxInputBytes || value.MaxInputRateBytes > MaximumTerminalInputRateBytes ||
		value.InputRateWindow <= 0 || value.InputRateWindow > MaximumTerminalInputRateWindow ||
		value.MaxResizeRate <= 0 || value.MaxResizeRate > MaximumTerminalResizeRate ||
		value.ResizeRateWindow <= 0 || value.ResizeRateWindow > MaximumTerminalResizeRateWindow ||
		value.ReadChunkBytes <= 0 || value.ReadChunkBytes > MaximumTerminalReadChunkBytes ||
		value.MaxEnvironmentEntries <= 0 || value.MaxEnvironmentEntries > MaximumTerminalEnvironmentEntries ||
		value.MaxEnvironmentBytes <= 0 || value.MaxEnvironmentBytes > MaximumTerminalEnvironmentBytes ||
		value.MaxEnvironmentValueBytes <= 0 || value.MaxEnvironmentValueBytes > MaximumTerminalEnvironmentValue ||
		value.MaxArguments <= 0 || value.MaxArguments > DefaultTerminalArguments ||
		value.MaxArgumentBytes <= 0 || value.MaxArgumentBytes > DefaultTerminalArgumentBytes ||
		value.DefaultCols < TerminalMinimumColumns || value.DefaultCols > TerminalMaximumColumns ||
		value.DefaultRows < TerminalMinimumRows || value.DefaultRows > TerminalMaximumRows ||
		value.MaxConnectionsGlobal <= 0 || value.MaxConnectionsGlobal > MaximumTerminalConnectionsGlobal ||
		value.MaxConnectionsPerSession <= 0 || value.MaxConnectionsPerSession > MaximumTerminalConnectionsSession ||
		value.MaxConnectionsPerSession > value.MaxConnectionsGlobal ||
		value.SendQueueFrames <= 0 || value.SendQueueFrames > MaximumTerminalSendQueueFrames ||
		value.SendQueueBytes <= 0 || value.SendQueueBytes > MaximumTerminalSendQueueBytes ||
		value.PingInterval <= 0 || value.PingInterval > MaximumTerminalPingInterval ||
		value.PongGrace <= 0 || value.PongGrace > MaximumTerminalPongGrace ||
		value.ReadDeadline <= 0 || value.ReadDeadline > MaximumTerminalReadDeadline ||
		value.WriteDeadline <= 0 || value.WriteDeadline > MaximumTerminalWriteDeadline ||
		value.MaxWebSocketMessageBytes <= 0 || value.MaxWebSocketMessageBytes > MaximumTerminalWebSocketBytes ||
		value.MaxWebSocketMessageBytes > value.MaxInputBytes ||
		len(value.AllowedEnvironment) > MaximumTerminalEnvironmentEntries {
		return false
	}
	allowed := make(map[string]struct{}, len(value.AllowedEnvironment))
	for _, name := range value.AllowedEnvironment {
		if !validTerminalEnvironmentName(name) || terminalSensitiveEnvironmentName(name) {
			return false
		}
		canonical := canonicalTerminalEnvironmentName(name)
		if _, duplicate := allowed[canonical]; duplicate {
			return false
		}
		allowed[canonical] = struct{}{}
	}
	profiles := make(map[string]struct{}, len(value.Profiles))
	defaultFound := false
	for _, profile := range value.Profiles {
		if !validTerminalProfileID(profile.ID) || !validTerminalExecutable(profile.Executable) || len(profile.Args) > value.MaxArguments {
			return false
		}
		if _, duplicate := profiles[profile.ID]; duplicate {
			return false
		}
		profiles[profile.ID] = struct{}{}
		if profile.ID == value.DefaultProfile {
			defaultFound = true
		}
		argumentBytes := 0
		for _, argument := range profile.Args {
			if !validTerminalArgument(argument) {
				return false
			}
			argumentBytes += len(argument)
			if argumentBytes > value.MaxArgumentBytes {
				return false
			}
		}
		if len(profile.Environment) > value.MaxEnvironmentEntries || terminalEnvironmentBytes(profile.Environment) > value.MaxEnvironmentBytes {
			return false
		}
		for name, envValue := range profile.Environment {
			if _, permitted := allowed[canonicalTerminalEnvironmentName(name)]; !permitted ||
				!validTerminalEnvironmentName(name) || terminalSensitiveEnvironmentName(name) ||
				!validTerminalEnvironmentValue(envValue, value.MaxEnvironmentValueBytes) {
				return false
			}
		}
	}
	return defaultFound
}

func (value TerminalRuntime) Profile(id string) (TerminalProfile, bool) {
	wanted := strings.TrimSpace(id)
	if wanted == "" {
		wanted = value.DefaultProfile
	}
	for _, profile := range value.Profiles {
		if profile.ID != wanted {
			continue
		}
		copy := TerminalProfile{ID: profile.ID, Executable: profile.Executable, Args: append([]string(nil), profile.Args...), Environment: make(map[string]string, len(profile.Environment))}
		for name, envValue := range profile.Environment {
			copy.Environment[name] = envValue
		}
		return copy, true
	}
	return TerminalProfile{}, false
}

func validTerminalProfileID(value string) bool {
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

func validTerminalExecutable(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 2048 && !strings.ContainsAny(value, "\x00\r\n|&;`$><")
}

func validTerminalArgument(value string) bool {
	return utf8.ValidString(value) && len(value) <= DefaultTerminalArgumentBytes && !strings.ContainsAny(value, "\x00\r\n")
}

func validTerminalEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return false
		}
		if index > 0 && !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func terminalSensitiveEnvironmentName(value string) bool {
	upper := strings.ToUpper(value)
	if upper == "TOKEN" || upper == "SECRET" || upper == "PASSWORD" || upper == "PASSPHRASE" || upper == "API_KEY" || upper == "AUTHORIZATION" || upper == "COOKIE" {
		return true
	}
	for _, suffix := range []string{"_TOKEN", "_SECRET", "_PASSWORD", "_PASSPHRASE", "_API_KEY", "_AUTH_TOKEN", "_AUTHORIZATION", "_COOKIE"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func validTerminalEnvironmentValue(value string, maximum int) bool {
	return utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func terminalEnvironmentBytes(values map[string]string) int {
	total := 0
	for name, value := range values {
		total += len(name) + len(value) + 1
	}
	return total
}

func canonicalTerminalEnvironmentName(value string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(value)
	}
	return value
}
