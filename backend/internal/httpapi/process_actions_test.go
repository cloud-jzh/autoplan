package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestProcessActionPathsRejectAmbiguousResourceAndActionValues(t *testing.T) {
	run := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/projects/7/scripts/8/actions/run", nil)
	projectID, scriptID, failure := processResourceActionTarget(run, "scripts", "script_id", "run")
	if failure != nil || projectID != 7 || scriptID != 8 {
		t.Fatalf("script target=%d/%d failure=%v", projectID, scriptID, failure)
	}
	leadingZero := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/projects/07/scripts/8/actions/run", nil)
	if _, _, failure := processResourceActionTarget(leadingZero, "scripts", "script_id", "run"); failure == nil {
		t.Fatal("leading-zero project id was accepted")
	}
	unsafeQuery := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/projects/7/scripts/8/actions/run?pid=1", nil)
	if _, _, failure := processResourceActionTarget(unsafeQuery, "scripts", "script_id", "run"); failure == nil {
		t.Fatal("process selector query was accepted")
	}
	reload := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/projects/7/executors/8/actions/reload", nil)
	projectID, executorID, action, failure := executorPluginActionTarget(reload)
	if failure != nil || projectID != 7 || executorID != 8 || action != "reload" {
		t.Fatalf("executor target=%d/%d/%q failure=%v", projectID, executorID, action, failure)
	}
	unknown := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/projects/7/executors/8/actions/shell", nil)
	if _, _, _, failure := executorPluginActionTarget(unknown); failure == nil {
		t.Fatal("arbitrary executor action was accepted")
	}
}

func TestProcessActionRoutePatternsRemainBounded(t *testing.T) {
	for _, route := range []string{
		ProjectScriptRunActionPath,
		ProjectScriptStopActionPath,
		ProjectExecutorRunActionPath,
		ProjectExecutorStopActionPath,
		ProjectExecutorPluginActionPath,
	} {
		if !validResourceRoutePattern(route) {
			t.Fatalf("process route rejected: %s", route)
		}
	}
	if validResourceRoutePattern("/api/v1/projects/{project_id}/executors/{executor_id}/actions/{action}/extra/{script_id}") {
		t.Fatal("unbounded process route was accepted")
	}
}
