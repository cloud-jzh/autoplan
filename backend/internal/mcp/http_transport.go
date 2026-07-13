package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type httpTransport struct {
	owner *Server
	limit chan struct{}

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
}

func newHTTPTransport(owner *Server) *httpTransport {
	limit := DefaultConnectionLimit
	if owner != nil && owner.config.ConnectionLimit > 0 {
		limit = owner.config.ConnectionLimit
	}
	return &httpTransport{owner: owner, limit: make(chan struct{}, limit)}
}

func (transport *httpTransport) start(ctx context.Context) error {
	if transport == nil || transport.owner == nil {
		return ErrTransportInvalid
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.server != nil || transport.listener != nil {
		return ErrAlreadyRunning
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(transport.owner.config.Host, strconv.Itoa(transport.owner.config.Port)))
	if err != nil {
		return ErrTransportInvalid
	}
	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           transport,
		ReadHeaderTimeout: transport.owner.config.RequestTimeout / 3,
		ReadTimeout:       transport.owner.config.RequestTimeout,
		WriteTimeout:      transport.owner.config.RequestTimeout,
		IdleTimeout:       transport.owner.config.RequestTimeout,
		MaxHeaderBytes:    8 << 10,
	}
	transport.listener, transport.server = listener, server
	transport.owner.markRunning()
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			transport.owner.markFailure()
			return
		}
		transport.owner.markStopped()
	}()
	return nil
}

func (transport *httpTransport) close(ctx context.Context) error {
	if transport == nil {
		return nil
	}
	transport.mu.Lock()
	server, listener := transport.server, transport.listener
	transport.server, transport.listener = nil, nil
	transport.mu.Unlock()
	if server == nil {
		return nil
	}
	err := server.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
	}
	return err
}

func (transport *httpTransport) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if transport == nil || transport.owner == nil || request == nil {
		writeHTTPFailure(writer, http.StatusServiceUnavailable, "mcp_transport_invalid")
		return
	}
	select {
	case transport.limit <- struct{}{}:
		defer func() { <-transport.limit }()
	default:
		recordAudit(request.Context(), transport.owner.audit, AuditEvent{Transport: string(TransportHTTP), Action: "request", Outcome: "mcp_tool_unavailable"})
		writeHTTPFailure(writer, http.StatusServiceUnavailable, "mcp_tool_unavailable")
		return
	}
	if !transport.owner.auth.authorizeHTTP(request, transport.owner.config.Host, transport.owner.config.Port) {
		recordAudit(request.Context(), transport.owner.audit, AuditEvent{Transport: string(TransportHTTP), Action: "request", Outcome: "mcp_auth_failed"})
		writeHTTPFailure(writer, http.StatusUnauthorized, "mcp_auth_failed")
		return
	}
	writer.Header().Set("Vary", "Origin")
	writer.Header().Set("Access-Control-Allow-Origin", request.Header.Get("Origin"))
	writer.Header().Set("Access-Control-Allow-Credentials", "true")
	if request.URL.RawQuery != "" || request.URL.RawPath != "" || request.URL.Fragment != "" {
		writeHTTPFailure(writer, http.StatusBadRequest, "mcp_transport_invalid")
		return
	}
	healthPath := transport.owner.config.Path + "/health"
	if request.URL.Path == healthPath {
		if request.Method != http.MethodGet || request.ContentLength > 0 {
			writeHTTPFailure(writer, http.StatusMethodNotAllowed, "mcp_transport_invalid")
			return
		}
		writeHTTPJSON(writer, http.StatusOK, transport.owner.Status())
		return
	}
	if request.URL.Path != transport.owner.config.Path || request.Method != http.MethodPost || !jsonContentType(request.Header.Get("Content-Type")) {
		writeHTTPFailure(writer, http.StatusMethodNotAllowed, "mcp_transport_invalid")
		return
	}
	if request.ContentLength > transport.owner.config.BodyLimitBytes {
		writeHTTPFailure(writer, http.StatusRequestEntityTooLarge, "mcp_transport_invalid")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, transport.owner.config.BodyLimitBytes+1))
	if err != nil || len(body) == 0 || int64(len(body)) > transport.owner.config.BodyLimitBytes {
		writeHTTPFailure(writer, http.StatusBadRequest, "mcp_transport_invalid")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), transport.owner.config.RequestTimeout)
	defer cancel()
	caller := ToolContext{CallerScope: "mcp-http", IdempotencyKey: boundedIdempotency(request.Header.Get("Idempotency-Key"))}
	response, respond := transport.owner.processFrame(ctx, body, TransportHTTP, caller)
	if !respond || response == nil {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	if ctx.Err() != nil {
		writeHTTPFailure(writer, http.StatusGatewayTimeout, "mcp_tool_timeout")
		return
	}
	writeHTTPJSON(writer, http.StatusOK, response)
}

func jsonContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func boundedIdempotency(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func writeHTTPFailure(writer http.ResponseWriter, status int, code string) {
	writeHTTPJSON(writer, status, map[string]string{"error": code, "code": code})
}

func writeHTTPJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
