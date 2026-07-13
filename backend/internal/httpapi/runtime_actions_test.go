package httpapi

import "testing"

func TestRuntimeActionPathParsersRejectAmbiguousIdentifiers(t *testing.T) {
	if projectID, failure := projectIDFromRuntimeActionPath("/api/v1/projects/7/loop/actions/start"); failure != nil || projectID != 7 {
		t.Fatalf("project id=%d failure=%v", projectID, failure)
	}
	if _, failure := projectIDFromRuntimeActionPath("/api/v1/projects/07/loop/actions/start"); failure == nil {
		t.Fatal("leading-zero project id was accepted")
	}
	planID, failure := planIDFromRuntimeActionPath("/api/v1/projects/7/plans/9/actions/stop", 7)
	if failure != nil || planID != 9 {
		t.Fatalf("plan id=%d failure=%v", planID, failure)
	}
	if _, failure := planIDFromRuntimeActionPath("/api/v1/projects/7/plans/09/actions/stop", 7); failure == nil {
		t.Fatal("leading-zero plan id was accepted")
	}
	intakeType, intakeID, failure := intakeTargetFromRuntimeActionPath("/api/v1/projects/7/intake/requirement/11/actions/retry-plan-generation", 7)
	if failure != nil || intakeType != "requirement" || intakeID != 11 {
		t.Fatalf("intake=%q/%d failure=%v", intakeType, intakeID, failure)
	}
	if _, _, failure := intakeTargetFromRuntimeActionPath("/api/v1/projects/7/intake/other/11/actions/retry-plan-generation", 7); failure == nil {
		t.Fatal("unknown intake type was accepted")
	}
}

func TestRuntimeActionPatternsSupportScopedResourcesOnly(t *testing.T) {
	for _, pattern := range []string{
		LoopStartActionPath,
		ProjectPlanStopActionPath,
		ProjectTaskRunActionPath,
		ProjectTaskRunBatchesActionPath,
		AcceptanceAcceptActionPath,
		IntakeRetryPlanGenerationActionPath,
	} {
		if !validResourceRoutePattern(pattern) {
			t.Fatalf("runtime action pattern rejected: %s", pattern)
		}
	}
	if validResourceRoutePattern("/api/v1/projects/{project_id}/intake/{intake_type}/{intake_id}/tasks/{task_id}/actions/run") {
		t.Fatal("unbounded four-resource action route was accepted")
	}
}
