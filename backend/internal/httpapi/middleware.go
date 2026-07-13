package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	pathpkg "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	"github.com/lyming99/autoplan/backend/internal/platform/logging"
	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

const (
	RequestIDHeader          = "X-Request-ID"
	IdempotencyKeyHeader     = "Idempotency-Key"
	MaximumRequestIDLength   = 64
	MaximumIdempotencyLength = 128
	unavailableRequestID     = "request_id_unavailable"
	DefaultRequestTimeout    = 15 * time.Second
	MaximumRequestTimeout    = 2 * time.Minute
)

var (
	requestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fieldPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	methodPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9!#$%&'*+.^_` + "`" + `|~-]{0,31}$`)
)

type requestIDKey struct{}
type requestStateKey struct{}

type requestState struct {
	route string
}

type RequestIDSource interface {
	NewRequestID() (string, error)
}

type CryptoRequestIDs struct{}

func (CryptoRequestIDs) NewRequestID() (string, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", err
	}
	return "req_" + hex.EncodeToString(random), nil
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

// IdempotencyKey validates the placeholder transport convention without
// changing existing non-idempotent business semantics.
func IdempotencyKey(request *http.Request) (string, *APIError) {
	values := request.Header.Values(IdempotencyKeyHeader)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) > MaximumIdempotencyLength || !idempotencyPattern.MatchString(values[0]) {
		failure := NewAPIError(CodeInvalidIdempotencyKey, &ErrorDetails{Field: "idempotency_key"})
		return "", &failure
	}
	return values[0], nil
}

func withRequestID(source RequestIDSource, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := callerRequestID(request)
		if requestID == "" {
			var generated bool
			requestID, generated = generateRequestID(source)
			if !generated {
				requestID = unavailableRequestID
				ctx := context.WithValue(request.Context(), requestIDKey{}, requestID)
				request = request.WithContext(ctx)
				WriteError(writer, request, NewAPIError(CodeInternal, nil))
				return
			}
		}
		state := &requestState{route: "unmatched"}
		ctx := context.WithValue(request.Context(), requestIDKey{}, requestID)
		ctx = context.WithValue(ctx, requestStateKey{}, state)
		writer.Header().Set(RequestIDHeader, requestID)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func withRequestTimeout(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Event streams own their cancellation through the request connection and
		// subscription lifecycle. Applying a short request deadline here would
		// sever an otherwise healthy SSE stream before its heartbeat interval.
		if request.Method == http.MethodGet && (acceptsEventStream(request.Header.Values("Accept")) || requestsWebSocketUpgrade(request)) {
			next.ServeHTTP(writer, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// WebSocket lifetime is governed by its own read, ping/pong and write bounds.
// The normal HTTP deadline would otherwise cancel a healthy upgraded terminal
// stream after one request timeout. Security still rejects malformed upgrade
// attempts before a connection can be hijacked.
func requestsWebSocketUpgrade(request *http.Request) bool {
	return request != nil && headerContainsToken(request.Header.Values("Connection"), "upgrade") &&
		strings.EqualFold(request.Header.Get("Upgrade"), "websocket")
}

// authenticatedCallerID derives the same non-reversible principal for REST
// reads, mutations, and SSE subscriptions. It is available only after
// Security.Protect has accepted the session and exact Origin; raw credential
// bytes never leave this middleware.
func authenticatedCallerID(request *http.Request) (string, *APIError) {
	if request == nil {
		failure := NewAPIError(CodeUnauthorized, nil)
		return "", &failure
	}
	security, authorized := RequestSecurity(request.Context())
	if !authorized {
		failure := NewAPIError(CodeUnauthorized, nil)
		return "", &failure
	}
	credential := request.Header.Get(session.HeaderName)
	if credential == "" {
		if cookie, err := request.Cookie(session.CookieName); err == nil {
			credential = cookie.Value
		}
	}
	if credential == "" {
		failure := NewAPIError(CodeUnauthorized, nil)
		return "", &failure
	}
	digest := sha256.Sum256([]byte("autoplan-p10-http-caller\x00" + credential + "\x00" + security.Origin))
	return "http-" + hex.EncodeToString(digest[:]), nil
}

// terminalAuthenticatedCaller converts the already authenticated HTTP session
// into P14's opaque caller value. It never exposes the session credential to
// the Terminal application service, audit record, response or access log.
func terminalAuthenticatedCaller(request *http.Request) (domainterminal.Caller, *APIError) {
	id, failure := authenticatedCallerID(request)
	if failure != nil {
		return domainterminal.Caller{}, failure
	}
	return domainterminal.Caller{ID: id}, nil
}

// withHeadResponse runs the same authorized handler and preserves its status
// and headers while suppressing the representation body required by HEAD.
func withHeadResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			next.ServeHTTP(writer, request)
			return
		}
		next.ServeHTTP(&headResponseWriter{ResponseWriter: writer}, request)
	})
}

type headResponseWriter struct {
	http.ResponseWriter
	status    int
	committed bool
}

func (writer *headResponseWriter) WriteHeader(status int) {
	if writer.status != 0 || writer.committed {
		return
	}
	writer.status = status
}

func (writer *headResponseWriter) Write(content []byte) (int, error) {
	if !writer.committed {
		writer.committed = true
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		if writer.Header().Get("Content-Length") == "" {
			writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		}
		writer.ResponseWriter.WriteHeader(status)
	}
	return len(content), nil
}

func (writer *headResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func generateRequestID(source RequestIDSource) (requestID string, valid bool) {
	defer func() {
		if recover() != nil {
			requestID = ""
			valid = false
		}
	}()
	requestID, err := source.NewRequestID()
	return requestID, err == nil && requestIDPattern.MatchString(requestID)
}

func callerRequestID(request *http.Request) string {
	values := request.Header.Values(RequestIDHeader)
	if len(values) != 1 || len(values[0]) > MaximumRequestIDLength || !requestIDPattern.MatchString(values[0]) {
		return ""
	}
	return values[0]
}

func withRecovery(logger logging.Logger, clock logging.Clock, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		buffer := newBufferedResponse(writer, RequestID(request.Context()))
		defer func() {
			if recover() != nil {
				state, _ := request.Context().Value(requestStateKey{}).(*requestState)
				route := "unmatched"
				if state != nil {
					route = state.route
				}
				safeLog(logger, logging.Event{
					OccurredAt: safeNow(clock), Level: "error", Code: "panic_recovered",
					ErrorCode: string(CodeInternal),
					RequestID: RequestID(request.Context()), Method: request.Method,
					Route: route, Status: http.StatusInternalServerError, Retryable: false,
				})
				if !buffer.committed {
					WriteError(writer, request, NewAPIError(CodeInternal, nil))
				}
				return
			}
			buffer.commit()
		}()
		next.ServeHTTP(buffer, request)
	})
}

func withAccessLog(logger logging.Logger, clock logging.Clock, route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := safeNow(clock)
		capture := &statusWriter{ResponseWriter: writer}
		next.ServeHTTP(capture, request)
		duration := safeNow(clock).Sub(started)
		if duration < 0 {
			duration = 0
		}
		safeLog(logger, logging.Event{
			OccurredAt: started, Level: "info", Code: "request_completed",
			RequestID: RequestID(request.Context()), Method: request.Method, Route: route,
			ErrorCode: capture.errorCode, Status: capture.statusCode(),
			DurationMS: duration.Milliseconds(), Retryable: capture.retryable,
		})
	})
}

func safeLog(logger logging.Logger, event logging.Event) {
	defer func() { _ = recover() }()
	_ = logger.Log(event)
}

func safeNow(clock logging.Clock) (result time.Time) {
	defer func() {
		if recover() != nil {
			result = time.Now().UTC()
		}
	}()
	result = clock.Now().UTC()
	if result.IsZero() {
		return time.Now().UTC()
	}
	return result
}

type statusWriter struct {
	http.ResponseWriter
	status    int
	retryable bool
	errorCode string
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(content []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(content)
}

func (writer *statusWriter) statusCode() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func (writer *statusWriter) setAPIError(code ErrorCode, retryable bool) {
	writer.errorCode = string(code)
	writer.retryable = retryable
	if marker, ok := writer.ResponseWriter.(interface{ setAPIError(ErrorCode, bool) }); ok {
		marker.setAPIError(code, retryable)
	}
}

func (writer *statusWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is unavailable")
	}
	connection, readWriter, err := hijacker.Hijack()
	if err == nil && writer.status == 0 {
		writer.status = http.StatusSwitchingProtocols
	}
	return connection, readWriter, err
}

func (writer *statusWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

type bufferedResponse struct {
	underlying http.ResponseWriter
	requestID  string
	header     http.Header
	body       bytes.Buffer
	status     int
	committed  bool
}

func newBufferedResponse(underlying http.ResponseWriter, requestID string) *bufferedResponse {
	return &bufferedResponse{
		underlying: underlying,
		requestID:  requestID,
		header:     underlying.Header().Clone(),
	}
}

func (writer *bufferedResponse) Header() http.Header {
	if writer.committed {
		return writer.underlying.Header()
	}
	return writer.header
}

func (writer *bufferedResponse) WriteHeader(status int) {
	if !writer.committed && writer.status == 0 {
		writer.status = status
	}
}

func (writer *bufferedResponse) Write(content []byte) (int, error) {
	if writer.committed {
		return writer.underlying.Write(content)
	}
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(content)
}

func (writer *bufferedResponse) commit() {
	if writer.committed {
		return
	}
	writer.committed = true
	writer.header.Set(RequestIDHeader, writer.requestID)
	for name, values := range writer.header {
		writer.underlying.Header().Del(name)
		for _, value := range values {
			writer.underlying.Header().Add(name, value)
		}
	}
	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	writer.underlying.WriteHeader(status)
	_, _ = writer.underlying.Write(writer.body.Bytes())
}

func (writer *bufferedResponse) Flush() {
	writer.commit()
	if flusher, ok := writer.underlying.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *bufferedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if writer.status != 0 || writer.body.Len() != 0 {
		return nil, nil, errors.New("response already buffered")
	}
	hijacker, ok := writer.underlying.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is unavailable")
	}
	connection, readWriter, err := hijacker.Hijack()
	if err == nil {
		writer.committed = true
	}
	return connection, readWriter, err
}

func (writer *bufferedResponse) Unwrap() http.ResponseWriter { return writer.underlying }

func allowedMethods(methods map[string]Endpoint) []string {
	result := make([]string, 0, len(methods))
	for method := range methods {
		result = append(result, method)
	}
	sort.Strings(result)
	return result
}

func validField(value string) bool { return fieldPattern.MatchString(value) }

func validMethod(value string) bool { return methodPattern.MatchString(value) }

func validRoute(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) <= 256 && !strings.ContainsAny(value, "\\?#") &&
		pathpkg.Clean(value) == value
}

func validRoutePattern(value string) bool {
	if !validRoute(value) || strings.Count(value, "{project_id}") != 1 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.ContainsAny(segment, "{}") && segment != "{project_id}" {
			return false
		}
	}
	return true
}

func routePatternMatches(pattern, value string) bool {
	patternSegments := strings.Split(pattern, "/")
	valueSegments := strings.Split(value, "/")
	if len(patternSegments) != len(valueSegments) {
		return false
	}
	for index := range patternSegments {
		if patternSegments[index] == "{project_id}" {
			if valueSegments[index] == "" {
				return false
			}
			continue
		}
		if patternSegments[index] != valueSegments[index] {
			return false
		}
	}
	return true
}
