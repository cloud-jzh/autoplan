package config

import (
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var asciiHostname = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)

// Validate rejects unsafe values instead of replacing them with defaults.
func (value *Config) Validate(repositoryRoot, temporaryRoot string) error {
	if value.HTTP.ListenHost != DefaultListenHost {
		return newError("listen_host_not_loopback")
	}
	if value.HTTP.ListenPort < 0 || value.HTTP.ListenPort > 65535 {
		return newError("listen_port_out_of_range")
	}
	if value.HTTP.BodyLimitBytes <= 0 || value.HTTP.BodyLimitBytes > MaximumBodyLimit {
		return newError("body_limit_out_of_range")
	}
	for _, timeout := range []time.Duration{
		value.HTTP.ReadHeaderTimeout,
		value.HTTP.ReadTimeout,
		value.HTTP.WriteTimeout,
		value.HTTP.IdleTimeout,
	} {
		if timeout <= 0 || timeout > MaximumHTTPTimeout {
			return newError("http_timeout_out_of_range")
		}
	}
	if value.HTTP.ShutdownTimeout <= 0 || value.HTTP.ShutdownTimeout > MaximumCloseTimeout {
		return newError("shutdown_timeout_out_of_range")
	}
	origins, err := normalizeOrigins(value.HTTP.AllowedOrigins)
	if err != nil {
		return err
	}
	value.HTTP.AllowedOrigins = origins
	if err := validateRuntime(&value.Runtime, repositoryRoot, temporaryRoot); err != nil {
		return err
	}
	switch value.LogLevel {
	case LogDebug, LogInfo, LogWarn, LogError:
	default:
		return newError("log_level_invalid")
	}
	return nil
}

func normalizeOrigins(origins []string) ([]string, error) {
	result := make([]string, 0, len(origins))
	seen := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin == "" || strings.TrimSpace(origin) != origin || origin == "null" || strings.Contains(origin, "*") {
			return nil, newError("origin_invalid")
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, newError("origin_invalid")
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return nil, newError("origin_scheme_invalid")
		}
		if parsed.Port() == "" {
			return nil, newError("origin_port_required")
		}
		hostname := strings.ToLower(parsed.Hostname())
		if hostname == "" || !asciiOriginHost(hostname) {
			return nil, newError("origin_host_invalid")
		}
		port := parsed.Port()
		portNumber, parseErr := strconv.Atoi(port)
		if parseErr != nil || portNumber <= 0 || portNumber > 65535 || strconv.Itoa(portNumber) != port {
			return nil, newError("origin_port_invalid")
		}
		normalized := scheme + "://" + net.JoinHostPort(hostname, port)
		if _, duplicate := seen[normalized]; duplicate {
			return nil, newError("origin_duplicate")
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func asciiOriginHost(hostname string) bool {
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback()
	}
	return len(hostname) <= 253 && asciiHostname.MatchString(hostname)
}

func validateRuntime(value *Runtime, repositoryRoot, temporaryRoot string) error {
	if value.Directory == "" && value.TargetKind == RuntimeTargetNone {
		return nil
	}
	if value.Directory == "" || value.TargetKind == RuntimeTargetNone || !filepath.IsAbs(value.Directory) {
		return newError("runtime_target_incomplete")
	}
	target := filepath.Clean(value.Directory)
	switch value.TargetKind {
	case RuntimeTargetFixture:
		if repositoryRoot == "" || !within(target, filepath.Join(repositoryRoot, "fixtures")) {
			return newError("runtime_fixture_outside_repository")
		}
	case RuntimeTargetTemporary:
		if temporaryRoot == "" || !within(target, temporaryRoot) {
			return newError("runtime_directory_outside_temporary_root")
		}
	case RuntimeTargetDatabaseCopy:
		base := strings.ToLower(filepath.Base(target))
		if base == "autoplan.sqlite" || !(strings.HasSuffix(base, ".copy") || strings.HasSuffix(base, ".backup") || strings.HasSuffix(base, ".bak")) {
			return newError("runtime_database_copy_invalid")
		}
	default:
		return newError("runtime_target_kind_invalid")
	}
	value.Directory = target
	return nil
}

func within(target, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), target)
	return err == nil && relative != "." && relative != ".." &&
		!filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
