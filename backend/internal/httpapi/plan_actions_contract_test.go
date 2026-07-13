package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lyming99/autoplan/backend/internal/application/capabilities"
	planactions "github.com/lyming99/autoplan/backend/internal/application/plans"
	taskactions "github.com/lyming99/autoplan/backend/internal/application/tasks"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
)

func TestP07DisabledActionServicesNeverCreateOperations(t *testing.T) {
	planService := planactions.NewActionService()
	for _, action := range []struct {
		capability capabilities.ID
		call       func() (planactions.ActionAccepted, error)
	}{
		{capabilities.PlansRun, func() (planactions.ActionAccepted, error) {
			return planService.Run(context.Background(), planactions.RunRequest{})
		}},
		{capabilities.PlansStop, func() (planactions.ActionAccepted, error) {
			return planService.Stop(context.Background(), planactions.StopRequest{})
		}},
		{capabilities.PlansResume, func() (planactions.ActionAccepted, error) {
			return planService.Resume(context.Background(), planactions.ResumeRequest{})
		}},
		{capabilities.PlansReexecute, func() (planactions.ActionAccepted, error) {
			return planService.Reexecute(context.Background(), planactions.ReexecuteRequest{})
		}},
		{capabilities.PlansRecreate, func() (planactions.ActionAccepted, error) {
			return planService.Recreate(context.Background(), planactions.RecreateRequest{})
		}},
	} {
		accepted, err := action.call()
		var disabled *capabilities.DisabledActionError
		if !errors.Is(err, capabilities.ErrNotImplemented) || !errors.As(err, &disabled) || disabled.Capability() != action.capability || accepted.Operation.OperationID != "" {
			t.Fatalf("plan action %q accepted=%#v error=%v", action.capability, accepted, err)
		}
	}

	taskService := taskactions.NewActionService()
	for _, action := range []struct {
		capability capabilities.ID
		call       func() (taskactions.ActionAccepted, error)
	}{
		{capabilities.TasksRun, func() (taskactions.ActionAccepted, error) {
			return taskService.Run(context.Background(), taskactions.RunRequest{})
		}},
		{capabilities.TasksRunBatches, func() (taskactions.ActionAccepted, error) {
			return taskService.RunBatches(context.Background(), taskactions.RunBatchesRequest{})
		}},
		{capabilities.TasksStop, func() (taskactions.ActionAccepted, error) {
			return taskService.Stop(context.Background(), taskactions.StopRequest{})
		}},
	} {
		accepted, err := action.call()
		var disabled *capabilities.DisabledActionError
		if !errors.Is(err, capabilities.ErrNotImplemented) || !errors.As(err, &disabled) || disabled.Capability() != action.capability || accepted.Operation.OperationID != "" {
			t.Fatalf("task action %q accepted=%#v error=%v", action.capability, accepted, err)
		}
	}
}

func TestP07DisabledActionHTTPContractIsStableAndTargetIndependent(t *testing.T) {
	router, err := p07ContractRouter()
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct {
		path       string
		capability capabilities.ID
	}{
		{PlanRunActionPath, capabilities.PlansRun},
		{PlanStopActionPath, capabilities.PlansStop},
		{PlanResumeActionPath, capabilities.PlansResume},
		{PlanReexecuteActionPath, capabilities.PlansReexecute},
		{PlanRecreateActionPath, capabilities.PlansRecreate},
		{TaskRunActionPath, capabilities.TasksRun},
		{TaskRunBatchesActionPath, capabilities.TasksRunBatches},
		{TaskStopActionPath, capabilities.TasksStop},
	}
	for _, route := range routes {
		if err := router.Handle(http.MethodPost, route.path, DisabledActionEndpoint(route.capability)); err != nil {
			t.Fatal(err)
		}
	}
	for _, route := range routes {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, route.path, nil)
		router.ServeHTTP(response, request)
		assertContractError(t, response, http.StatusNotImplemented, string(CodeNotImplemented), false)
		var failure contracts.Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Details == nil {
			t.Fatalf("disabled action %q omitted capability details", route.capability)
		}
		var capability string
		if err := json.Unmarshal((*failure.Details)["capability"], &capability); err != nil || capability != string(route.capability) {
			t.Fatalf("disabled action %q details=%#v error=%v", route.capability, failure.Details, err)
		}
	}
}
