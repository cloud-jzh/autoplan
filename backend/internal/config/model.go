// Package config owns the sidecar's typed, security-sensitive configuration.
// It never discovers Electron userData, a production database, or credentials.
package config

import (
	"net"
	"strconv"
	"time"
)

const (
	DefaultListenHost    = "127.0.0.1"
	DefaultListenPort    = 0
	DefaultBodyLimit     = int64(1 << 20)
	MaximumBodyLimit     = int64(16 << 20)
	DefaultReadHeader    = 5 * time.Second
	DefaultRead          = 15 * time.Second
	DefaultWrite         = 30 * time.Second
	DefaultIdle          = 60 * time.Second
	DefaultShutdown      = 10 * time.Second
	MaximumHTTPTimeout   = 5 * time.Minute
	MaximumCloseTimeout  = 2 * time.Minute
	EnvironmentPrefix    = "AUTOPLAN_SIDECAR_"
	SessionMaterialBytes = 32
)

// Config is the complete P002 process configuration. Secrets are deliberately
// absent so they cannot be loaded from argv, URLs, files, or environment.
type Config struct {
	HTTP     HTTP
	Runtime  Runtime
	LogLevel LogLevel
}

// HTTP centralizes every network limit consumed by the future HTTP server.
type HTTP struct {
	ListenHost        string
	ListenPort        int
	AllowedOrigins    []string
	BodyLimitBytes    int64
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Address returns the already-validated loopback listener address.
func (value HTTP) Address() string {
	return net.JoinHostPort(value.ListenHost, strconv.Itoa(value.ListenPort))
}

// RuntimeTargetKind is an explicit authorization class, never an inferred
// description of an existing path.
type RuntimeTargetKind string

const (
	RuntimeTargetNone         RuntimeTargetKind = ""
	RuntimeTargetFixture      RuntimeTargetKind = "fixture"
	RuntimeTargetTemporary    RuntimeTargetKind = "temporary"
	RuntimeTargetDatabaseCopy RuntimeTargetKind = "database-copy"
)

// Runtime remains empty by default. A non-empty target must be explicitly
// classified before any later task may create a runtime dependency there.
type Runtime struct {
	Directory  string
	TargetKind RuntimeTargetKind
}

type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Defaults returns values that do not expose a non-loopback listener and do
// not select, create, or open a runtime directory.
func Defaults() Config {
	return Config{
		HTTP: HTTP{
			ListenHost:        DefaultListenHost,
			ListenPort:        DefaultListenPort,
			AllowedOrigins:    nil,
			BodyLimitBytes:    DefaultBodyLimit,
			ReadHeaderTimeout: DefaultReadHeader,
			ReadTimeout:       DefaultRead,
			WriteTimeout:      DefaultWrite,
			IdleTimeout:       DefaultIdle,
			ShutdownTimeout:   DefaultShutdown,
		},
		Runtime:  Runtime{TargetKind: RuntimeTargetNone},
		LogLevel: LogInfo,
	}
}
