package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/config"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
	"github.com/lyming99/autoplan/backend/internal/platform/redaction"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

type Transport string

const (
	TransportREST      Transport = "rest"
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
)

var ErrSecurityConfiguration = errors.New("HTTP security configuration is invalid")

type SecurityOptions struct {
	Sessions     *session.Manager
	Origins      config.OriginSet
	ExpectedHost string
	ExpectedPort int
	Logger       logging.Logger
	Clock        logging.Clock
}

type Security struct {
	sessions     *session.Manager
	origins      config.OriginSet
	expectedHost string
	expectedPort int
	logger       logging.Logger
	clock        logging.Clock
}

func NewSecurity(options SecurityOptions) (*Security, error) {
	if options.Sessions == nil || options.Logger == nil || options.Clock == nil ||
		options.ExpectedHost != config.DefaultListenHost || options.ExpectedPort < 0 || options.ExpectedPort > 65535 {
		return nil, ErrSecurityConfiguration
	}
	return &Security{
		sessions: options.Sessions, origins: options.Origins,
		expectedHost: options.ExpectedHost, expectedPort: options.ExpectedPort,
		logger: options.Logger, clock: options.Clock,
	}, nil
}

type SecurityContext struct {
	Transport Transport
	Origin    string
}

type securityContextKey struct{}

func RequestSecurity(ctx context.Context) (SecurityContext, bool) {
	value, ok := ctx.Value(securityContextKey{}).(SecurityContext)
	return value, ok
}

// Protect applies one identical Host, Origin, URL-credential, session,
// request_id, and rejection-log policy before any application service call.
func (security *Security) Protect(transport Transport, next Endpoint) Endpoint {
	return func(app application.Boundary, writer http.ResponseWriter, request *http.Request) {
		writer.Header().Add("Vary", "Origin")
		failure, origin := security.authorize(request, transport)
		if failure != nil {
			if origin.Canonical != "" {
				setExactCORS(writer.Header(), origin.Canonical)
			}
			security.logRejection(request, transport, *failure)
			WriteError(writer, request, *failure)
			return
		}
		setExactCORS(writer.Header(), origin.Canonical)
		ctx := context.WithValue(request.Context(), securityContextKey{}, SecurityContext{
			Transport: transport, Origin: origin.Canonical,
		})
		next(app, writer, request.WithContext(ctx))
	}
}

func (security *Security) authorize(request *http.Request, transport Transport) (*APIError, config.Origin) {
	expectedPort, portOK := security.requestPort(request)
	if security == nil || request == nil || !portOK || !validTransport(transport) || request.URL == nil ||
		request.URL.IsAbs() || request.URL.Host != "" || request.URL.User != nil ||
		!config.MatchLoopbackAuthority(request.Host, security.expectedHost, expectedPort) ||
		hasForwardingHeaders(request.Header) {
		failure := NewAPIError(CodeOriginForbidden, nil)
		return &failure, config.Origin{}
	}
	originValues := request.Header.Values("Origin")
	if len(originValues) != 1 {
		failure := NewAPIError(CodeOriginForbidden, nil)
		return &failure, config.Origin{}
	}
	origin, allowed := security.origins.Match(originValues[0])
	if !allowed {
		failure := NewAPIError(CodeOriginForbidden, nil)
		return &failure, config.Origin{}
	}
	if hasCredentialInURL(request.URL) || hasAlternateCredentialHeader(request.Header) ||
		!security.sessions.AuthenticateRequest(request) {
		failure := NewAPIError(CodeUnauthorized, nil)
		return &failure, origin
	}
	return nil, origin
}

func setExactCORS(header http.Header, origin string) {
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Credentials", "true")
}

func (security *Security) requestPort(request *http.Request) (int, bool) {
	if security == nil || request == nil {
		return 0, false
	}
	if security.expectedPort != 0 {
		return security.expectedPort, true
	}
	local, ok := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return 0, false
	}
	host, portText, err := net.SplitHostPort(local.String())
	if err != nil || host != security.expectedHost {
		return 0, false
	}
	port, err := strconv.Atoi(portText)
	return port, err == nil && port > 0 && port <= 65535 && strconv.Itoa(port) == portText
}

func hasForwardingHeaders(header http.Header) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if len(header.Values(name)) != 0 {
			return true
		}
	}
	return false
}

func hasCredentialInURL(value *url.URL) bool {
	if value == nil || value.User != nil {
		return true
	}
	query, err := url.ParseQuery(value.RawQuery)
	if err != nil {
		return true
	}
	for key := range query {
		if redaction.SensitiveKey(key) || strings.EqualFold(key, "auth") {
			return true
		}
	}
	return false
}

func hasAlternateCredentialHeader(header http.Header) bool {
	for name := range header {
		if strings.EqualFold(name, session.HeaderName) || strings.EqualFold(name, "Cookie") {
			continue
		}
		if redaction.SensitiveKey(name) {
			return true
		}
	}
	return false
}

func validTransport(transport Transport) bool {
	switch transport {
	case TransportREST, TransportSSE, TransportWebSocket:
		return true
	default:
		return false
	}
}

func (security *Security) logRejection(request *http.Request, transport Transport, failure APIError) {
	defer func() { _ = recover() }()
	occurredAt := time.Now().UTC()
	if security.clock != nil {
		occurredAt = security.clock.Now().UTC()
	}
	_ = security.logger.Log(logging.Event{
		OccurredAt: occurredAt, Level: "warn", Code: "security_rejected",
		ErrorCode: string(failure.Code()), RequestID: RequestID(request.Context()),
		Method: request.Method, Route: transportRoute(transport), Status: failure.Status(),
		Retryable: failure.Retryable(),
	})
}

func transportRoute(transport Transport) string {
	switch transport {
	case TransportREST:
		return RESTSkeletonPath
	case TransportSSE:
		return SSESkeletonPath
	case TransportWebSocket:
		return WebSocketSkeletonPath
	default:
		return "unmatched"
	}
}

func headerContainsToken(values []string, expected string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), expected) {
				return true
			}
		}
	}
	return false
}
