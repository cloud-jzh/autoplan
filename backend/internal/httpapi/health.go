package httpapi

import (
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/application"
)

type ReadyProbe interface {
	Ready() bool
}

type probeResponse struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
}

// RegisterProbes keeps liveness independent from dependency readiness. The
// daemon passes its transport policy so probe traffic is still bound to the
// same session, exact Origin and loopback authority as every REST route.
func RegisterProbes(router *Router, readiness ReadyProbe, policies ...*Security) error {
	if router == nil || readiness == nil || len(policies) > 1 {
		return ErrRouterDependency
	}
	var policy *Security
	if len(policies) == 1 {
		policy = policies[0]
		if policy == nil {
			return ErrRouterDependency
		}
	}
	protect := func(endpoint Endpoint) Endpoint {
		if policy == nil {
			return endpoint
		}
		return policy.Protect(TransportREST, endpoint)
	}
	health := func(
		_ application.Boundary,
		writer http.ResponseWriter,
		request *http.Request,
	) {
		WriteResponse(writer, request, http.StatusOK, probeResponse{
			Status: "ok", RequestID: RequestID(request.Context()),
		})
	}
	if err := router.Handle(http.MethodGet, "/healthz", protect(health)); err != nil {
		return err
	}
	ready := func(
		_ application.Boundary,
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if !readiness.Ready() {
			code := CodeServiceUnavailable
			if state, ok := readiness.(interface{ ShuttingDown() bool }); ok && state.ShuttingDown() {
				code = CodeShuttingDown
			}
			WriteError(writer, request, NewAPIError(code, nil))
			return
		}
		WriteResponse(writer, request, http.StatusOK, probeResponse{
			Status: "ready", RequestID: RequestID(request.Context()),
		})
	}
	return router.Handle(http.MethodGet, "/readyz", protect(ready))
}
