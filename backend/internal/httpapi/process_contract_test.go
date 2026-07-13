package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestP12ProcessActionsRejectCommandShapedBodies(t *testing.T) {
	for _, body := range []string{
		`{"command":"fixture;whoami"}`,
		`{"pid":999}`,
		`{"environment":{"TOKEN":"fixture-secret"}}`,
		`{"working_directory":"C:\\\\fixture"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/projects/7/scripts/11/actions/run", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if failure := decodeEmptyProcessAction(httptest.NewRecorder(), request, 1024); failure == nil || failure.Code() != CodeInvalidJSON {
			t.Fatalf("unsafe body %s failure=%v", body, failure)
		}
	}
}

func TestP12ProcessActionPathsDoNotAcceptProcessSelectors(t *testing.T) {
	for _, target := range []string{
		"/api/v1/projects/7/scripts/11/actions/run?pid=999",
		"/api/v1/projects/7/scripts/11/actions/run/extra",
		"/api/v1/projects/7/scripts/11/actions/shell",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+target, nil)
		if _, _, failure := processResourceActionTarget(request, "scripts", "script_id", "run"); failure == nil {
			t.Fatalf("unsafe target accepted: %s", target)
		}
	}
}
