package mcp

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultHost                  = "127.0.0.1"
	DefaultPort                  = 43847
	DefaultPath                  = "/mcp"
	DefaultBodyLimit       int64 = 1 << 20
	DefaultRequestTimeout        = 30 * time.Second
	DefaultShutdownTimeout       = 10 * time.Second
	DefaultConnectionLimit       = 32
)

var ErrInvalidConfiguration = errors.New("mcp configuration is invalid")

// Transport is intentionally closed. A new transport must use the same
// Registry and AdapterFactory rather than introducing a parallel tool path.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportStdio Transport = "stdio"
)

// Config contains listener-only configuration. Credential material is held by
// Authenticator, never by Config or its public projection.
type Config struct {
	Enabled         bool
	Transport       Transport
	Host            string
	Port            int
	Path            string
	PortExplicit    bool
	AllowedOrigins  []string
	BodyLimitBytes  int64
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	ConnectionLimit int
}

// ConfigDTO is the only configuration projection intended for an API, event,
// or audit record. It contains a presence bit and a mask, but never a token.
type ConfigDTO struct {
	Enabled              bool   `json:"enabled"`
	Transport            string `json:"transport"`
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	Path                 string `json:"path"`
	PortExplicit         bool   `json:"port_explicit"`
	PortExplicitCamel    bool   `json:"portExplicit"`
	HasAuthToken         bool   `json:"has_auth_token"`
	HasAuthTokenCamel    bool   `json:"hasAuthToken"`
	AuthTokenMasked      string `json:"auth_token_masked"`
	AuthTokenMaskedCamel string `json:"authTokenMasked"`
}

// Status is a safe runtime projection used by future REST status endpoints.
// URL and examples intentionally use a token placeholder.
type Status struct {
	Enabled           bool             `json:"enabled"`
	Running           bool             `json:"running"`
	State             string           `json:"status"`
	Transport         string           `json:"transport"`
	Host              *string          `json:"host"`
	Port              *int             `json:"port"`
	Path              *string          `json:"path"`
	URL               *string          `json:"url"`
	HasAuthToken      bool             `json:"hasAuthToken"`
	AuthTokenMasked   string           `json:"authTokenMasked"`
	AuthHeader        string           `json:"authHeader"`
	LocalOnly         bool             `json:"localOnly"`
	Tools             []string         `json:"tools"`
	ToolDocs          []ToolDescriptor `json:"toolDocs"`
	ConnectionExample string           `json:"connectionExample"`
	Note              string           `json:"note"`
	LastError         *string          `json:"lastError"`
	StartedAt         *time.Time       `json:"startedAt"`
}

// DefaultConfig is deliberately disabled. The independent P13B feature gate
// must opt in before a listener or stdio protocol loop can start.
func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		Transport:       TransportHTTP,
		Host:            DefaultHost,
		Port:            DefaultPort,
		Path:            DefaultPath,
		BodyLimitBytes:  DefaultBodyLimit,
		RequestTimeout:  DefaultRequestTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
		ConnectionLimit: DefaultConnectionLimit,
	}
}

func (value Config) clone() Config {
	value.AllowedOrigins = append([]string(nil), value.AllowedOrigins...)
	return value
}

func (value Config) Validate() error {
	if value.Transport != TransportHTTP && value.Transport != TransportStdio ||
		value.Host != DefaultHost || value.Port < 1 || value.Port > 65535 ||
		value.BodyLimitBytes < 1024 || value.BodyLimitBytes > 16<<20 ||
		value.RequestTimeout < time.Second || value.RequestTimeout > 5*time.Minute ||
		value.ShutdownTimeout < time.Second || value.ShutdownTimeout > 2*time.Minute ||
		value.ConnectionLimit < 1 || value.ConnectionLimit > 256 ||
		!validPath(value.Path) {
		return ErrInvalidConfiguration
	}
	if value.Enabled && value.Transport == TransportHTTP && len(value.AllowedOrigins) == 0 {
		return ErrInvalidConfiguration
	}
	seen := make(map[string]struct{}, len(value.AllowedOrigins))
	for _, origin := range value.AllowedOrigins {
		canonical, ok := canonicalOrigin(origin)
		if !ok {
			return ErrInvalidConfiguration
		}
		if _, duplicate := seen[canonical]; duplicate {
			return ErrInvalidConfiguration
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func (value Config) Public(hasAuthToken bool, maskedToken string) ConfigDTO {
	return ConfigDTO{
		Enabled: value.Enabled, Transport: string(value.Transport), Host: value.Host,
		Port: value.Port, Path: value.Path, PortExplicit: value.PortExplicit,
		PortExplicitCamel: value.PortExplicit, HasAuthToken: hasAuthToken,
		HasAuthTokenCamel: hasAuthToken, AuthTokenMasked: maskedToken,
		AuthTokenMaskedCamel: maskedToken,
	}
}

func validPath(value string) bool {
	if len(value) == 0 || len(value) > 1024 || !strings.HasPrefix(value, "/") ||
		value == "/" || strings.ContainsAny(value, "?#\\\x00") || strings.Contains(value, "//") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func canonicalOrigin(value string) (string, bool) {
	if value == "" || value == "null" || strings.TrimSpace(value) != value {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Host == "" || parsed.Hostname() != DefaultHost {
		return "", false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != parsed.Port() {
		return "", false
	}
	canonical := "http://" + net.JoinHostPort(DefaultHost, strconv.Itoa(port))
	return canonical, value == canonical
}

func maskedToken(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	const prefix = "****"
	if len(value) <= 4 {
		return prefix
	}
	return prefix + string(value[len(value)-4:])
}
