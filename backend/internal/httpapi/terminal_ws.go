package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lyming99/autoplan/backend/internal/application"
	applicationterminal "github.com/lyming99/autoplan/backend/internal/application/terminal"
	"github.com/lyming99/autoplan/backend/internal/config"
	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
	terminalruntime "github.com/lyming99/autoplan/backend/internal/runtime/terminal"
)

const (
	TerminalWebSocketPath = "/api/v1/terminals/{id}/ws"

	terminalWSContinuation = byte(0x0)
	terminalWSText         = byte(0x1)
	terminalWSClose        = byte(0x8)
	terminalWSPing         = byte(0x9)
	terminalWSPong         = byte(0xa)

	terminalWSCloseNormal       = 1000
	terminalWSCloseGoingAway    = 1001
	terminalWSCloseProtocol     = 1002
	terminalWSCloseInvalidData  = 1007
	terminalWSClosePolicy       = 1008
	terminalWSCloseTooLarge     = 1009
	terminalWSCloseRestart      = 1012
	terminalWSCloseSlowConsumer = 1013
	terminalWSCloseInternal     = 1011

	terminalWSMaxClientInput = 64 << 10
	terminalWSMaxNonce       = 128
)

// TerminalWebSocketService deliberately consists only of application commands
// and subscription state. HTTP owns the protocol but never receives a PTY,
// process tree, Files Policy implementation, replay buffer, or event bus.
type TerminalWebSocketService interface {
	AcquireConnection(context.Context, applicationterminal.SessionCommand, applicationterminal.ConnectionLimits) (*applicationterminal.ConnectionLease, error)
	Attach(context.Context, applicationterminal.AttachCommand) (*applicationterminal.Subscription, error)
	DetachSubscription(*applicationterminal.Subscription)
	Write(context.Context, applicationterminal.WriteCommand) (int, error)
	Resize(context.Context, applicationterminal.ResizeCommand) error
}

var _ TerminalWebSocketService = (*applicationterminal.Service)(nil)

type TerminalWebSocketOptions struct {
	Service        TerminalWebSocketService
	FeatureEnabled bool
	Runtime        config.TerminalRuntime
}

type terminalWebSocketRoutes struct {
	service        TerminalWebSocketService
	featureEnabled bool
	runtime        config.TerminalRuntime
}

// RegisterTerminalWebSocket creates a separate authenticated terminal data
// plane. It has no dependency on project SSE registration and never relays raw
// terminal bytes into generic event streams.
func RegisterTerminalWebSocket(router *Router, security *Security, options TerminalWebSocketOptions) error {
	if router == nil || security == nil || options.Service == nil {
		return ErrRouterDependency
	}
	runtime := options.Runtime
	if terminalWebSocketRuntimeUnset(runtime) {
		runtime = config.DefaultTerminalRuntime()
	}
	if !runtime.Valid() {
		return ErrRouterDependency
	}
	routes := terminalWebSocketRoutes{service: options.Service, featureEnabled: options.FeatureEnabled, runtime: runtime}
	return router.HandlePattern(http.MethodGet, TerminalWebSocketPath, security.Protect(TransportWebSocket, routes.endpoint))
}

func terminalWebSocketRuntimeUnset(runtime config.TerminalRuntime) bool {
	return len(runtime.Profiles) == 0 && runtime.DefaultProfile == "" && len(runtime.AllowedEnvironment) == 0 &&
		runtime.MaxSessionsGlobal == 0 && runtime.MaxSessionsPerProject == 0 && runtime.MaxSessionRuntime == 0 && runtime.GracePeriod == 0 &&
		runtime.MaxInputBytes == 0 && runtime.MaxInputRateBytes == 0 && runtime.InputRateWindow == 0 && runtime.MaxResizeRate == 0 &&
		runtime.ResizeRateWindow == 0 && runtime.ReadChunkBytes == 0 && runtime.MaxEnvironmentEntries == 0 && runtime.MaxEnvironmentBytes == 0 &&
		runtime.MaxEnvironmentValueBytes == 0 && runtime.MaxArguments == 0 && runtime.MaxArgumentBytes == 0 && runtime.DefaultCols == 0 && runtime.DefaultRows == 0 &&
		runtime.MaxConnectionsGlobal == 0 && runtime.MaxConnectionsPerSession == 0 && runtime.SendQueueFrames == 0 && runtime.SendQueueBytes == 0 &&
		runtime.PingInterval == 0 && runtime.PongGrace == 0 && runtime.ReadDeadline == 0 && runtime.WriteDeadline == 0 && runtime.MaxWebSocketMessageBytes == 0
}

func (routes terminalWebSocketRoutes) endpoint(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
	if !routes.featureEnabled {
		WriteError(writer, request, NewAPIError(CodeTerminalFeatureDisabled, nil))
		return
	}
	if !validWebSocketHandshake(request) {
		WriteError(writer, request, NewAPIError(CodeTerminalProtocolError, nil))
		return
	}
	command, lastSeq, failure := terminalWebSocketCommand(request)
	if failure != nil {
		WriteError(writer, request, *failure)
		return
	}
	lease, err := routes.service.AcquireConnection(request.Context(), command, applicationterminal.ConnectionLimits{
		Global: routes.runtime.MaxConnectionsGlobal, PerSession: routes.runtime.MaxConnectionsPerSession,
	})
	if err != nil {
		if errors.Is(err, applicationterminal.ErrConnectionLimit) {
			WriteError(writer, request, NewAPIError(CodeTerminalConnectionLimit, nil))
			return
		}
		writeTerminalServiceError(writer, request, err, CodeTerminalPTYUnavailable)
		return
	}
	defer lease.Close()

	subscription, err := routes.service.Attach(request.Context(), applicationterminal.AttachCommand{
		ReplayCommand: applicationterminal.ReplayCommand{SessionCommand: command, LastSeq: lastSeq},
	})
	if err != nil {
		writeTerminalServiceError(writer, request, err, CodeTerminalReplayGap)
		return
	}
	defer routes.service.DetachSubscription(subscription)
	responseHeader := writer.Header().Clone()

	hijacker, supported := writer.(http.Hijacker)
	if !supported {
		WriteError(writer, request, NewAPIError(CodeTerminalPTYUnavailable, nil))
		return
	}
	connection, readWriter, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	if err := connection.SetWriteDeadline(time.Now().Add(routes.runtime.WriteDeadline)); err != nil {
		return
	}
	if terminalWebSocketHandshake(readWriter, responseHeader, request.Header.Get("Sec-WebSocket-Key")) != nil {
		return
	}
	routes.serveConnection(connection, readWriter, subscription, command, lease)
}

func terminalWebSocketCommand(request *http.Request) (applicationterminal.SessionCommand, uint64, *APIError) {
	caller, failure := terminalAuthenticatedCaller(request)
	if failure != nil {
		return applicationterminal.SessionCommand{}, 0, failure
	}
	sessionID, failure := terminalSessionIDFromPath(request.URL.Path, "/ws")
	if failure != nil {
		return applicationterminal.SessionCommand{}, 0, failure
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		result := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		return applicationterminal.SessionCommand{}, 0, &result
	}
	if len(values) == 0 || len(values) > 2 || len(values["project_id"]) != 1 || len(values["last_seq"]) > 1 {
		result := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "query"})
		return applicationterminal.SessionCommand{}, 0, &result
	}
	projectID, valid := parseCanonicalPositiveID(values.Get("project_id"))
	if !valid {
		result := NewAPIError(CodeInvalidProjectID, &ErrorDetails{Field: "project_id"})
		return applicationterminal.SessionCommand{}, 0, &result
	}
	lastSeq := uint64(0)
	if value, found := values["last_seq"]; found {
		parsed, err := strconv.ParseUint(value[0], 10, 64)
		if err != nil || parsed > terminalMaximumSeq || strconv.FormatUint(parsed, 10) != value[0] {
			result := NewAPIError(CodeTerminalInvalidPayload, &ErrorDetails{Field: "last_seq"})
			return applicationterminal.SessionCommand{}, 0, &result
		}
		lastSeq = parsed
	}
	return applicationterminal.SessionCommand{Caller: caller, ProjectID: projectID, SessionID: sessionID}, lastSeq, nil
}

func terminalWebSocketHandshake(readWriter *bufio.ReadWriter, header http.Header, key string) error {
	response := []string{
		"HTTP/1.1 101 Switching Protocols\r\n",
		"Upgrade: websocket\r\n",
		"Connection: Upgrade\r\n",
		"Sec-WebSocket-Accept: " + webSocketAccept(key) + "\r\n",
		"Cache-Control: no-store\r\n",
	}
	for _, name := range []string{RequestIDHeader, "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Vary"} {
		for _, value := range header.Values(name) {
			response = append(response, name+": "+value+"\r\n")
		}
	}
	response = append(response, "\r\n")
	for _, value := range response {
		if _, err := readWriter.WriteString(value); err != nil {
			return err
		}
	}
	return readWriter.Flush()
}

func (routes terminalWebSocketRoutes) serveConnection(
	connection net.Conn,
	readWriter *bufio.ReadWriter,
	subscription *applicationterminal.Subscription,
	command applicationterminal.SessionCommand,
	lease *applicationterminal.ConnectionLease,
) {
	outbound := newTerminalWSOutbound(routes.runtime.SendQueueFrames, routes.runtime.SendQueueBytes)
	writerDone := make(chan error, 1)
	go func() { writerDone <- outbound.write(connection, readWriter.Writer, routes.runtime) }()
	for _, output := range subscription.Replay.Entries {
		queued, writerFinished := outbound.enqueueInitial(terminalWSText, terminalWSOutputFrame(output), writerDone)
		if !queued {
			if !writerFinished {
				outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
				<-writerDone
			}
			return
		}
	}
	for _, event := range subscription.Initial {
		payload, err := terminalWSEventFrame(event)
		if err != nil {
			outbound.stop(terminalWSCloseInternal, "invalid terminal event")
			<-writerDone
			return
		}
		queued, writerFinished := outbound.enqueueInitial(terminalWSText, payload, writerDone)
		if !queued {
			if !writerFinished {
				outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
				<-writerDone
			}
			return
		}
	}

	readerStop := make(chan struct{})
	readerResults := make(chan terminalWSReadResult, 1)
	go terminalWSReadLoop(connection, readWriter.Reader, routes.runtime, readerStop, readerResults)
	defer close(readerStop)

	writerFinished := false
	finish := func(code int, reason string) {
		outbound.stop(code, reason)
		if !writerFinished {
			<-writerDone
			writerFinished = true
		}
	}
	defer func() { finish(terminalWSCloseNormal, "") }()

	lastPong := time.Now()
	heartbeat := time.NewTicker(routes.runtime.PongGrace)
	defer heartbeat.Stop()
	events := subscription.Events
	done := subscription.Done
	for {
		select {
		case <-lease.Done():
			outbound.stop(terminalWSCloseRestart, "service restart")
			return
		case <-heartbeat.C:
			if time.Since(lastPong) > routes.runtime.PingInterval+routes.runtime.PongGrace {
				outbound.stop(terminalWSCloseGoingAway, "heartbeat timeout")
				return
			}
		case err := <-writerDone:
			writerFinished = true
			if err != nil {
				return
			}
			return
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if err := subscription.Reauthorize(context.Background()); err != nil {
				outbound.stop(terminalWSServiceCloseCode(err), "authorization changed")
				return
			}
			if !outbound.enqueueEvent(event) {
				outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
				return
			}
		case err, open := <-done:
			if !open {
				done = nil
				continue
			}
			done = nil
			if errors.Is(err, domainterminal.ErrSlowConsumer) {
				outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
				return
			}
		case result := <-readerResults:
			if result.err != nil {
				outbound.stop(result.closeCode, "protocol error")
				return
			}
			switch result.frame.opcode {
			case terminalWSClose:
				outbound.stop(terminalWSCloseNormal, "")
				return
			case terminalWSPing:
				if !outbound.enqueue(terminalWSPong, result.frame.payload) {
					outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
					return
				}
			case terminalWSPong:
				lastPong = time.Now()
			case terminalWSText:
				message, err := terminalWSDecodeCommand(result.frame.payload)
				if err != nil {
					outbound.stop(err.closeCode, "protocol error")
					return
				}
				if !routes.dispatchMessage(subscription, command, message, outbound) {
					return
				}
			}
		}
	}
}

func (routes terminalWebSocketRoutes) dispatchMessage(
	subscription *applicationterminal.Subscription,
	command applicationterminal.SessionCommand,
	message terminalWSCommand,
	outbound *terminalWSOutbound,
) bool {
	if message.kind == "ping" {
		return outbound.enqueueText(terminalWSPongFrame(message.nonce))
	}
	// The application service rechecks caller/project/session authorization on
	// each mutation, rather than treating a successful upgrade as permanent.
	if err := subscription.Reauthorize(context.Background()); err != nil {
		outbound.stop(terminalWSServiceCloseCode(err), "authorization changed")
		return false
	}
	var err error
	switch message.kind {
	case "input":
		_, err = routes.service.Write(context.Background(), applicationterminal.WriteCommand{SessionCommand: command, Data: message.data})
	case "resize":
		err = routes.service.Resize(context.Background(), applicationterminal.ResizeCommand{SessionCommand: command, Cols: message.cols, Rows: message.rows})
	default:
		outbound.stop(terminalWSCloseProtocol, "protocol error")
		return false
	}
	if err != nil {
		outbound.stop(terminalWSServiceCloseCode(err), "command rejected")
		return false
	}
	return true
}

func terminalWSServiceCloseCode(err error) int {
	switch {
	case errors.Is(err, domainterminal.ErrInvalidCommand), errors.Is(err, domainterminal.ErrForbidden),
		errors.Is(err, domainterminal.ErrNotFound), errors.Is(err, domainterminal.ErrClosed),
		errors.Is(err, applicationterminal.ErrConnectionLimit), errors.Is(err, terminalruntime.ErrInputLimit),
		errors.Is(err, terminalruntime.ErrResizeLimit):
		return terminalWSClosePolicy
	default:
		return terminalWSCloseInternal
	}
}

type terminalWSFrame struct {
	opcode  byte
	payload []byte
}

type terminalWSReadResult struct {
	frame     terminalWSFrame
	err       error
	closeCode int
}

func terminalWSReadLoop(connection net.Conn, reader *bufio.Reader, runtime config.TerminalRuntime, stop <-chan struct{}, results chan<- terminalWSReadResult) {
	for {
		frame, err := terminalWSReadFrame(connection, reader, runtime.MaxWebSocketMessageBytes, runtime.ReadDeadline)
		result := terminalWSReadResult{frame: frame, err: err, closeCode: terminalWSCloseProtocol}
		if err != nil {
			var protocol *terminalWSProtocolError
			if errors.As(err, &protocol) {
				result.closeCode = protocol.closeCode
			} else {
				var networkError net.Error
				if errors.As(err, &networkError) && networkError.Timeout() {
					result.closeCode = terminalWSCloseGoingAway
				}
			}
			select {
			case results <- result:
			case <-stop:
			}
			return
		}
		select {
		case results <- result:
		case <-stop:
			return
		}
		if frame.opcode == terminalWSClose {
			return
		}
	}
}

func terminalWSReadFrame(connection net.Conn, reader *bufio.Reader, maximum int, deadline time.Duration) (terminalWSFrame, error) {
	if err := connection.SetReadDeadline(time.Now().Add(deadline)); err != nil {
		return terminalWSFrame{}, err
	}
	header := [2]byte{}
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return terminalWSFrame{}, err
	}
	if header[0]&0x70 != 0 || header[0]&0x80 == 0 {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
	}
	opcode := header[0] & 0x0f
	if opcode == terminalWSContinuation || (opcode != terminalWSText && opcode != terminalWSClose && opcode != terminalWSPing && opcode != terminalWSPong) {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
	}
	if header[1]&0x80 == 0 {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
	}
	size := uint64(header[1] & 0x7f)
	switch size {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return terminalWSFrame{}, err
		}
		size = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return terminalWSFrame{}, err
		}
		if extended[0]&0x80 != 0 {
			return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
		}
		size = binary.BigEndian.Uint64(extended[:])
	}
	if size > uint64(maximum) {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseTooLarge)
	}
	if (opcode == terminalWSClose || opcode == terminalWSPing || opcode == terminalWSPong) && (size > 125 || header[0]&0x80 == 0) {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
	}
	mask := [4]byte{}
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return terminalWSFrame{}, err
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return terminalWSFrame{}, err
	}
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	if opcode == terminalWSText && !utf8.Valid(payload) {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseInvalidData)
	}
	if opcode == terminalWSClose && !terminalWSValidClosePayload(payload) {
		return terminalWSFrame{}, newTerminalWSProtocolError(terminalWSCloseProtocol)
	}
	return terminalWSFrame{opcode: opcode, payload: payload}, nil
}

func terminalWSValidClosePayload(payload []byte) bool {
	if len(payload) == 1 || (len(payload) > 2 && !utf8.Valid(payload[2:])) {
		return false
	}
	if len(payload) == 0 {
		return true
	}
	code := int(binary.BigEndian.Uint16(payload[:2]))
	return code >= 1000 && code < 5000 && code != 1004 && code != 1005 && code != 1006 && code != 1015
}

type terminalWSProtocolError struct{ closeCode int }

func (errorValue *terminalWSProtocolError) Error() string { return "terminal websocket protocol error" }

func newTerminalWSProtocolError(closeCode int) error {
	return &terminalWSProtocolError{closeCode: closeCode}
}

type terminalWSOutbound struct {
	frames    chan terminalWSFrame
	control   chan terminalWSCloseFrame
	available chan struct{}
	maximum   int
	mu        sync.Mutex
	bytes     int
	stopped   bool
	stopOnce  sync.Once
}

type terminalWSCloseFrame struct {
	code   int
	reason string
}

func newTerminalWSOutbound(frames, maximum int) *terminalWSOutbound {
	return &terminalWSOutbound{
		frames: make(chan terminalWSFrame, frames), control: make(chan terminalWSCloseFrame, 1),
		available: make(chan struct{}, 1), maximum: maximum,
	}
}

func (outbound *terminalWSOutbound) enqueueText(payload []byte) bool {
	return outbound.enqueue(terminalWSText, payload)
}

func (outbound *terminalWSOutbound) enqueueEvent(event domainterminal.Event) bool {
	payload, err := terminalWSEventFrame(event)
	return err == nil && outbound.enqueueText(payload)
}

func (outbound *terminalWSOutbound) enqueue(opcode byte, payload []byte) bool {
	if outbound == nil || len(payload) > outbound.maximum {
		return false
	}
	copyPayload := append([]byte(nil), payload...)
	outbound.mu.Lock()
	defer outbound.mu.Unlock()
	if outbound.stopped || outbound.bytes+len(copyPayload) > outbound.maximum {
		return false
	}
	select {
	case outbound.frames <- terminalWSFrame{opcode: opcode, payload: copyPayload}:
		outbound.bytes += len(copyPayload)
		return true
	default:
		return false
	}
}

// enqueueInitial permits the finite replay snapshot to wait for a writer slot.
// It is never used for live PTY output: live delivery must remain nonblocking
// so a client cannot stall the runtime reader.
func (outbound *terminalWSOutbound) enqueueInitial(opcode byte, payload []byte, writerDone <-chan error) (queued, writerFinished bool) {
	if outbound == nil || len(payload) > outbound.maximum {
		return false, false
	}
	for {
		if outbound.enqueue(opcode, payload) {
			return true, false
		}
		outbound.mu.Lock()
		stopped := outbound.stopped
		outbound.mu.Unlock()
		if stopped {
			return false, false
		}
		select {
		case <-outbound.available:
		case <-writerDone:
			return false, true
		}
	}
}

func (outbound *terminalWSOutbound) stop(code int, reason string) {
	if outbound == nil {
		return
	}
	outbound.stopOnce.Do(func() {
		outbound.mu.Lock()
		outbound.stopped = true
		outbound.mu.Unlock()
		outbound.control <- terminalWSCloseFrame{code: code, reason: reason}
	})
}

func (outbound *terminalWSOutbound) write(connection net.Conn, writer *bufio.Writer, runtime config.TerminalRuntime) error {
	ticker := time.NewTicker(runtime.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case closeFrame := <-outbound.control:
			return terminalWSWriteClose(connection, writer, closeFrame, runtime.WriteDeadline)
		default:
		}
		select {
		case closeFrame := <-outbound.control:
			return terminalWSWriteClose(connection, writer, closeFrame, runtime.WriteDeadline)
		case <-ticker.C:
			if err := terminalWSWriteFrame(connection, writer, terminalWSPing, nil, runtime.WriteDeadline); err != nil {
				return err
			}
		case frame := <-outbound.frames:
			outbound.mu.Lock()
			outbound.bytes -= len(frame.payload)
			if outbound.bytes < 0 {
				outbound.bytes = 0
			}
			outbound.mu.Unlock()
			select {
			case outbound.available <- struct{}{}:
			default:
			}
			if err := terminalWSWriteFrame(connection, writer, frame.opcode, frame.payload, runtime.WriteDeadline); err != nil {
				return err
			}
		}
	}
}

func terminalWSWriteClose(connection net.Conn, writer *bufio.Writer, closeFrame terminalWSCloseFrame, deadline time.Duration) error {
	reason := strings.ToValidUTF8(closeFrame.reason, "")
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], uint16(closeFrame.code))
	copy(payload[2:], reason)
	return terminalWSWriteFrame(connection, writer, terminalWSClose, payload, deadline)
}

func terminalWSWriteFrame(connection net.Conn, writer *bufio.Writer, opcode byte, payload []byte, deadline time.Duration) error {
	if err := connection.SetWriteDeadline(time.Now().Add(deadline)); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) <= 125:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}

type terminalWSCommand struct {
	kind  string
	data  string
	cols  int
	rows  int
	nonce string
}

func terminalWSDecodeCommand(payload []byte) (terminalWSCommand, *terminalWSProtocolError) {
	if len(payload) > terminalWSMaxClientInput {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseTooLarge}
	}
	if len(payload) == 0 || !utf8.Valid(payload) || !terminalWSJSONDepthWithin(payload, 1) {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString || key == "" {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		if _, duplicate := fields[key]; duplicate {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		fields[key] = raw
		if len(fields) > 3 {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	if decoder.More() {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	typeValue, valid := terminalWSString(fields["type"])
	if !valid {
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
	switch typeValue {
	case "input":
		if len(fields) != 2 {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		data, valid := terminalWSString(fields["data"])
		if !valid || data == "" || len(data) > terminalWSMaxClientInput {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseInvalidData}
		}
		return terminalWSCommand{kind: typeValue, data: data}, nil
	case "resize":
		if len(fields) != 3 {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		cols, colsValid := terminalWSInt(fields["cols"])
		rows, rowsValid := terminalWSInt(fields["rows"])
		if !colsValid || !rowsValid || cols < config.TerminalMinimumColumns || cols > config.TerminalMaximumColumns || rows < config.TerminalMinimumRows || rows > config.TerminalMaximumRows {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseInvalidData}
		}
		return terminalWSCommand{kind: typeValue, cols: cols, rows: rows}, nil
	case "ping":
		if len(fields) != 2 {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
		}
		nonce, valid := terminalWSString(fields["nonce"])
		if !valid || nonce == "" || len(nonce) > terminalWSMaxNonce {
			return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseInvalidData}
		}
		return terminalWSCommand{kind: typeValue, nonce: nonce}, nil
	default:
		return terminalWSCommand{}, &terminalWSProtocolError{closeCode: terminalWSCloseProtocol}
	}
}

// Frozen client envelopes are one shallow object. Braces inside a JSON string
// are data, not nesting; every real nested object/array is rejected before it
// can become an ignored or ambiguous extension point.
func terminalWSJSONDepthWithin(payload []byte, maximum int) bool {
	depth := 0
	inString := false
	escaped := false
	for _, value := range payload {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maximum {
				return false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return !inString && !escaped && depth == 0
}

func terminalWSString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
		return "", false
	}
	return value, true
}

func terminalWSInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func terminalWSOutputFrame(output domainterminal.Output) []byte {
	payload, _ := json.Marshal(struct {
		Type string `json:"type"`
		Seq  uint64 `json:"seq"`
		Data string `json:"data"`
	}{Type: domainterminal.EventOutput, Seq: output.Seq, Data: output.Data})
	return payload
}

func terminalWSPongFrame(nonce string) []byte {
	payload, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Nonce string `json:"nonce"`
	}{Type: "pong", Nonce: nonce})
	return payload
}

func terminalWSEventFrame(event domainterminal.Event) ([]byte, error) {
	switch event.Type {
	case domainterminal.EventOutput:
		if event.Output == nil {
			return nil, errors.New("terminal output event is invalid")
		}
		return terminalWSOutputFrame(*event.Output), nil
	case domainterminal.EventExit:
		if event.Exit == nil {
			return nil, errors.New("terminal exit event is invalid")
		}
		signal := event.Exit.Signal
		return json.Marshal(struct {
			Type     string  `json:"type"`
			ExitCode *int    `json:"exit_code"`
			Signal   *string `json:"signal"`
		}{Type: domainterminal.EventExit, ExitCode: copyTerminalExitCode(event.Exit.Code), Signal: &signal})
	case domainterminal.EventStatus:
		if event.Session == nil {
			return nil, errors.New("terminal status event is invalid")
		}
		return json.Marshal(struct {
			Type    string             `json:"type"`
			Session terminalSessionDTO `json:"session"`
		}{Type: domainterminal.EventStatus, Session: terminalSessionProjection(*event.Session)})
	case domainterminal.EventClosed:
		if event.Session == nil {
			return nil, errors.New("terminal closed event is invalid")
		}
		return json.Marshal(struct {
			Type    string             `json:"type"`
			Closed  bool               `json:"closed"`
			Session terminalSessionDTO `json:"session"`
		}{Type: domainterminal.EventClosed, Closed: true, Session: terminalSessionProjection(*event.Session)})
	default:
		return nil, errors.New("terminal event is invalid")
	}
}
