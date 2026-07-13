package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Origin struct {
	Scheme    string
	Host      string
	Port      int
	Canonical string
}

func (origin Origin) Secure() bool { return origin.Scheme == "https" }

type OriginSet struct {
	allowed map[string]Origin
}

func NewOriginSet(values []string) (OriginSet, error) {
	result := OriginSet{allowed: make(map[string]Origin, len(values))}
	for _, value := range values {
		origin, err := parseExactOrigin(value)
		if err != nil {
			return OriginSet{}, err
		}
		if _, duplicate := result.allowed[origin.Canonical]; duplicate {
			return OriginSet{}, newError("origin_duplicate")
		}
		result.allowed[origin.Canonical] = origin
	}
	return result, nil
}

func (set OriginSet) Match(value string) (Origin, bool) {
	origin, err := parseExactOrigin(value)
	if err != nil || set.allowed == nil {
		return Origin{}, false
	}
	allowed, ok := set.allowed[origin.Canonical]
	return allowed, ok
}

func (set OriginSet) Empty() bool { return len(set.allowed) == 0 }

func parseExactOrigin(value string) (Origin, error) {
	if value == "" || value == "null" || strings.Contains(value, "*") || strings.TrimSpace(value) != value {
		return Origin{}, newError("origin_invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Origin{}, newError("origin_invalid")
	}
	scheme := parsed.Scheme
	if scheme != "http" && scheme != "https" {
		return Origin{}, newError("origin_scheme_invalid")
	}
	host := parsed.Hostname()
	if host == "" || host != strings.ToLower(host) || !asciiOriginHost(host) {
		return Origin{}, newError("origin_host_invalid")
	}
	portText := parsed.Port()
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || strconv.Itoa(port) != portText {
		return Origin{}, newError("origin_port_invalid")
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return Origin{}, newError("origin_default_port_forbidden")
	}
	canonical := scheme + "://" + net.JoinHostPort(host, portText)
	if value != canonical {
		return Origin{}, newError("origin_not_canonical")
	}
	return Origin{Scheme: scheme, Host: host, Port: port, Canonical: canonical}, nil
}

// MatchLoopbackAuthority verifies the Host header against the configured
// sidecar listener. A zero expected port permits the post-listen random port,
// but still requires an explicit canonical numeric port in the request.
func MatchLoopbackAuthority(authority, expectedHost string, expectedPort int) bool {
	if expectedHost != DefaultListenHost || authority == "" || strings.TrimSpace(authority) != authority {
		return false
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil || host != expectedHost {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || strconv.Itoa(port) != portText {
		return false
	}
	if expectedPort != 0 && port != expectedPort {
		return false
	}
	return authority == net.JoinHostPort(expectedHost, portText)
}
