package httpapi

import (
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/application"
)

const (
	RESTSkeletonPath       = "/api/v1/skeleton/rest"
	TransportVersionHeader = "X-Autoplan-Transport-Version"
	TransportVersion       = "1"
)

func RegisterRESTSkeleton(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	endpoint := security.Protect(TransportREST, func(
		app application.Boundary,
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if _, failure := IdempotencyKey(request); failure != nil {
			WriteError(writer, request, *failure)
			return
		}
		_ = app.Capabilities(request.Context())
		writer.Header().Set(TransportVersionHeader, TransportVersion)
		WriteError(writer, request, NewAPIError(CodeNotImplemented, nil))
	})
	return router.Handle(http.MethodPost, RESTSkeletonPath, endpoint)
}

func RegisterTransportSkeletons(router *Router, security *Security) error {
	if err := RegisterRESTSkeleton(router, security); err != nil {
		return err
	}
	if err := RegisterSSESkeleton(router, security); err != nil {
		return err
	}
	return RegisterWebSocketSkeleton(router, security)
}
