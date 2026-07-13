package httpapi

import (
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
)

const CapabilitiesPath = "/api/v1/capabilities"

type capabilitiesEnvelope struct {
	Data      capabilitiesResponse `json:"data"`
	RequestID string               `json:"request_id"`
}

type capabilitiesResponse struct {
	Version      string                    `json:"version"`
	Capabilities []capabilities.Capability `json:"capabilities"`
}

func RegisterCapabilityRoutes(router *Router, security *Security) error {
	if router == nil || security == nil {
		return ErrSecurityConfiguration
	}
	service := capabilities.NewService()
	return router.Handle(http.MethodGet, CapabilitiesPath, security.Protect(TransportREST,
		CapabilitiesEndpoint(service),
	))
}

// CapabilitiesEndpoint is separated from route protection so the router tests
// can exercise the data contract without admitting an unprotected route.
func CapabilitiesEndpoint(service *capabilities.Service) Endpoint {
	if service == nil {
		service = capabilities.NewService()
	}
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		WriteResponse(writer, request, http.StatusOK, capabilitiesEnvelope{
			Data: capabilitiesResponse{
				Version: capabilities.ContractVersion, Capabilities: service.List(),
			},
			RequestID: RequestID(request.Context()),
		})
	}
}

// RegisterP07ActionRoutes is the explicit composition point for the static
// P003 contract. Bootstrap may opt into it only once the separate migration
// gate permits these HTTP routes; registering it never enables an action.
func RegisterP07ActionRoutes(router *Router, security *Security) error {
	if err := RegisterCapabilityRoutes(router, security); err != nil {
		return err
	}
	if err := RegisterPlanActionRoutes(router, security); err != nil {
		return err
	}
	return RegisterTaskActionRoutes(router, security)
}
