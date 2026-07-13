package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/application"
	"github.com/lyming99/autoplan/backend/internal/application/maintenance"
)

const (
	MaintenanceStatusPath  = "/api/v1/maintenance"
	MaintenanceCutoverPath = "/api/v1/maintenance/cutover"
)

// MaintenanceService is deliberately narrow: callers can observe redacted
// state and initiate the single cutover operation, but cannot submit paths,
// SQL, ownership data, or any force/rollback flag through HTTP.
type MaintenanceService interface {
	Status() maintenance.Status
	Cutover(context.Context) (maintenance.Status, error)
}

type maintenanceEnvelope struct {
	Data      maintenance.Status `json:"data"`
	RequestID string             `json:"request_id"`
}

func RegisterMaintenance(router *Router, security *Security, service MaintenanceService) error {
	if router == nil || security == nil || service == nil {
		return ErrRouterDependency
	}
	if err := router.Handle(http.MethodGet, MaintenanceStatusPath, security.Protect(TransportREST,
		maintenanceStatusEndpoint(service),
	)); err != nil {
		return err
	}
	return router.Handle(http.MethodPost, MaintenanceCutoverPath, security.Protect(TransportREST,
		maintenanceCutoverEndpoint(service),
	))
}

func maintenanceStatusEndpoint(service MaintenanceService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		WriteResponse(writer, request, http.StatusOK, maintenanceEnvelope{Data: service.Status(), RequestID: RequestID(request.Context())})
	}
}

func maintenanceCutoverEndpoint(service MaintenanceService) Endpoint {
	return func(_ application.Boundary, writer http.ResponseWriter, request *http.Request) {
		status, err := service.Cutover(request.Context())
		if err != nil {
			code := CodeServiceUnavailable
			if errors.Is(err, maintenance.ErrOperationInProgress) {
				code = CodeRequestInProgress
			}
			WriteError(writer, request, NewAPIError(code, nil))
			return
		}
		WriteResponse(writer, request, http.StatusOK, maintenanceEnvelope{Data: status, RequestID: RequestID(request.Context())})
	}
}
