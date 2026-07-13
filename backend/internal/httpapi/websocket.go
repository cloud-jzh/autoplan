package httpapi

import (
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/application"
)

const WebSocketSkeletonPath = "/api/v1/skeleton/websocket"

const webSocketAcceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func RegisterWebSocketSkeleton(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	endpoint := security.Protect(TransportWebSocket, func(
		app application.Boundary,
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if !validWebSocketHandshake(request) {
			WriteError(writer, request, NewAPIError(CodeNotImplemented, nil))
			return
		}
		_ = app.Capabilities(request.Context())
		writer.Header().Set(TransportVersionHeader, TransportVersion)
		WriteError(writer, request, NewAPIError(CodeNotImplemented, nil))
	})
	return router.Handle(http.MethodGet, WebSocketSkeletonPath, endpoint)
}

func validWebSocketHandshake(request *http.Request) bool {
	if request == nil || !headerContainsToken(request.Header.Values("Connection"), "upgrade") ||
		len(request.Header.Values("Upgrade")) != 1 ||
		!strings.EqualFold(request.Header.Get("Upgrade"), "websocket") ||
		len(request.Header.Values("Sec-WebSocket-Version")) != 1 ||
		request.Header.Get("Sec-WebSocket-Version") != "13" ||
		len(request.Header.Values("Sec-WebSocket-Key")) != 1 ||
		len(request.Header.Values("Sec-WebSocket-Protocol")) != 0 ||
		len(request.Header.Values("Sec-WebSocket-Extensions")) != 0 {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(request.Header.Get("Sec-WebSocket-Key"))
	return err == nil && len(decoded) == 16
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + webSocketAcceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}
