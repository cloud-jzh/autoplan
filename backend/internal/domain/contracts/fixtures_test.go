package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type fixtureManifest struct {
	SchemaVersion int           `json:"schema_version"`
	SyntheticOnly bool          `json:"synthetic_only"`
	Shape         fixtureShape  `json:"shape"`
	Cases         []fixtureCase `json:"cases"`
}

type fixtureShape struct {
	ProjectFields     []string       `json:"project_fields"`
	ProjectRequired   []string       `json:"project_required"`
	SnapshotFields    []string       `json:"snapshot_fields"`
	OperationStatuses []string       `json:"operation_statuses"`
	OperationRequired []string       `json:"operation_required"`
	SSERequired       []string       `json:"sse_required"`
	WSRequired        []string       `json:"ws_required"`
	ErrorCatalog      []fixtureError `json:"error_catalog"`
}

type fixtureError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type fixtureCase struct {
	ID       string          `json:"id"`
	Contract string          `json:"contract"`
	Valid    bool            `json:"valid"`
	Value    json.RawMessage `json:"value"`
	Base     string          `json:"base"`
	Mutation string          `json:"mutation"`
}

func TestSharedFixturesStrictDecodeAndRoundTrip(t *testing.T) {
	manifest, cases, _ := loadFixtureManifest(t)
	if manifest.SchemaVersion != 1 || !manifest.SyntheticOnly {
		t.Fatal("fixture manifest must be synthetic schema version 1")
	}
	for _, item := range manifest.Cases {
		item := item
		t.Run(item.ID, func(t *testing.T) {
			content := materializeFixture(t, item, cases)
			value, err := decodeFixture(item.Contract, content)
			if !item.Valid {
				if err == nil {
					t.Fatal("invalid shared fixture was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid shared fixture was rejected: %v", err)
			}
			reencoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("re-encode failed: %v", err)
			}
			if _, err := decodeFixture(item.Contract, reencoded); err != nil {
				t.Fatalf("re-encoded fixture drifted: %v", err)
			}
			assertRoundTripSemantics(t, item.Contract, content, reencoded)
		})
	}
}

func assertRoundTripSemantics(t *testing.T, contract string, before, after []byte) {
	t.Helper()
	var left map[string]any
	var right map[string]any
	if json.Unmarshal(before, &left) != nil || json.Unmarshal(after, &right) != nil {
		t.Fatal("round-trip JSON could not be compared")
	}
	if contract == "project" {
		for name, value := range left {
			if value == nil {
				delete(left, name)
				delete(right, name)
			}
		}
	}
	normalizeWorkspaceCompatibility(left, right)
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("round-trip semantic drift: before=%s after=%s", before, after)
	}
}

func normalizeWorkspaceCompatibility(before, after any) {
	leftObject, leftIsObject := before.(map[string]any)
	rightObject, rightIsObject := after.(map[string]any)
	if leftIsObject && rightIsObject {
		if _, existed := leftObject["workspace_path"]; !existed && rightObject["workspace_path"] == "" {
			delete(rightObject, "workspace_path")
		}
		for name, left := range leftObject {
			if right, exists := rightObject[name]; exists {
				normalizeWorkspaceCompatibility(left, right)
			}
		}
		return
	}
	leftArray, leftIsArray := before.([]any)
	rightArray, rightIsArray := after.([]any)
	if leftIsArray && rightIsArray {
		for index := 0; index < len(leftArray) && index < len(rightArray); index++ {
			normalizeWorkspaceCompatibility(leftArray[index], rightArray[index])
		}
	}
}

func TestSharedFixtureShapeMatchesSchemas(t *testing.T) {
	manifest, _, root := loadFixtureManifest(t)
	assertSchemaObject(t, filepath.Join(root, "backend", "openapi", "schemas", "project.schema.json"),
		manifest.Shape.ProjectFields, manifest.Shape.ProjectRequired)
	assertSchemaObject(t, filepath.Join(root, "backend", "openapi", "schemas", "snapshot.schema.json"),
		manifest.Shape.SnapshotFields, manifest.Shape.SnapshotFields)

	operation := readSchema(t, filepath.Join(root, "backend", "openapi", "schemas", "operation.schema.json"))
	definitions := objectValue(t, operation, "$defs")
	operationDefinition := objectValue(t, definitions, "Operation")
	assertStringSet(t, stringArray(t, operationDefinition["required"]), manifest.Shape.OperationRequired)
	properties := objectValue(t, operationDefinition, "properties")
	status := objectValue(t, properties, "status")
	assertStringSet(t, stringArray(t, status["enum"]), manifest.Shape.OperationStatuses)

	assertSchemaRequired(t, filepath.Join(root, "backend", "openapi", "schemas", "sse-envelope-v1.schema.json"), manifest.Shape.SSERequired)
	assertSchemaRequired(t, filepath.Join(root, "backend", "openapi", "schemas", "ws-envelope-v1.schema.json"), manifest.Shape.WSRequired)
	assertErrorCatalogCoverage(t, manifest)
}

func TestSharedFixtureContainsNoMachineOrUsableCredentialData(t *testing.T) {
	_, _, root := loadFixtureManifest(t)
	content, err := os.ReadFile(filepath.Join(root, "fixtures", "contracts", "p02", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for label, pattern := range map[string]string{
		"Windows user profile": `[A-Za-z]:[\\/](?:Users|Documents and Settings|AppData)[\\/]`,
		"POSIX user profile":   `/(?:Users|home)/[^/]+/`,
		"Electron user data":   `(?i)userData`,
		"usable bearer value":  `(?i)Bearer\s+[A-Za-z0-9._~+/-]{12,}`,
		"usable API key":       `\bsk-[A-Za-z0-9_-]{12,}\b`,
		"private key":          `BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY`,
	} {
		if regexp.MustCompile(pattern).Match(content) {
			t.Fatalf("fixture contains %s", label)
		}
	}
	for _, forbidden := range []string{`"workspace_path"`, `"env_vars"`} {
		if bytes.Contains(content, []byte(forbidden)) {
			t.Fatalf("fixture contains forbidden public field %s", forbidden)
		}
	}
}

func loadFixtureManifest(t *testing.T) (fixtureManifest, map[string]fixtureCase, string) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "fixtures", "contracts", "p02", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest fixtureManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	cases := make(map[string]fixtureCase, len(manifest.Cases))
	for _, item := range manifest.Cases {
		if item.ID == "" || item.Contract == "" {
			t.Fatal("fixture case identity is incomplete")
		}
		if _, duplicate := cases[item.ID]; duplicate {
			t.Fatalf("duplicate fixture id %s", item.ID)
		}
		if item.Valid == (len(item.Value) == 0) || item.Valid == (item.Base != "" || item.Mutation != "") {
			t.Fatalf("fixture %s does not follow value/base ownership", item.ID)
		}
		cases[item.ID] = item
	}
	return manifest, cases, root
}

func materializeFixture(t *testing.T, item fixtureCase, cases map[string]fixtureCase) []byte {
	t.Helper()
	if item.Valid {
		return append([]byte(nil), item.Value...)
	}
	base, exists := cases[item.Base]
	if !exists || !base.Valid || base.Contract != item.Contract {
		t.Fatalf("fixture %s has an invalid base", item.ID)
	}
	if item.Mutation == "duplicate_key" {
		return bytes.Replace(base.Value, []byte("{"), []byte(`{"id":999,`), 1)
	}
	var value map[string]any
	if err := json.Unmarshal(base.Value, &value); err != nil {
		t.Fatal(err)
	}
	applyFixtureMutation(t, item.Contract, item.Mutation, value)
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func applyFixtureMutation(t *testing.T, contract, mutation string, value map[string]any) {
	t.Helper()
	switch mutation {
	case "unknown_field":
		value["unexpected_field"] = true
	case "non_utc_time":
		field := "updated_at"
		if contract == "sse" || contract == "ws" {
			field = "occurred_at"
		}
		value[field] = "2026-01-02T11:04:05+08:00"
	case "null_nonnullable":
		value["phase"] = nil
	case "missing_snapshot_field":
		delete(value, "scanSummary")
	case "mismatched_active_project":
		value["activeProjectId"] = float64(2)
	case "absolute_path":
		value["state"] = map[string]any{"source_path": "/synthetic/fixture.json"}
	case "credential_field":
		target := map[string]any{"api_key": "not-a-credential"}
		if contract == "snapshot" {
			value["state"] = target
		} else {
			value["data"] = target
		}
	case "missing_request_id":
		delete(value, "request_id")
	case "unknown_status":
		value["status"] = "unknown"
	case "invalid_terminal":
		value["error"] = nil
	case "invalid_idempotency":
		value["idempotency_key"] = "contains whitespace"
	case "unknown_version":
		value["schema_version"] = float64(2)
	case "missing_data":
		delete(value, "data")
	case "invalid_direction":
		value["direction"] = "sideways"
	default:
		t.Fatalf("unknown fixture mutation %s", mutation)
	}
}

func decodeFixture(contract string, content []byte) (any, error) {
	var destination any
	switch contract {
	case "project":
		destination = &Project{}
	case "snapshot":
		destination = &AppSnapshot{}
	case "error":
		destination = &Error{}
	case "operation_accepted":
		destination = &OperationAccepted{}
	case "operation":
		destination = &Operation{}
	case "sse":
		destination = &SSEEnvelopeV1{}
	case "ws":
		destination = &WSEnvelopeV1{}
	default:
		return nil, ErrInvalidContract
	}
	return destination, DecodeStrict(content, destination)
}

func readSchema(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func objectValue(t *testing.T, source map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(source[key], &value); err != nil || value == nil {
		t.Fatalf("schema object %s is invalid", key)
	}
	return value
}

func stringArray(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertSchemaObject(t *testing.T, name string, fields, required []string) {
	t.Helper()
	document := readSchema(t, name)
	properties := objectValue(t, document, "properties")
	actualFields := make([]string, 0, len(properties))
	for field := range properties {
		actualFields = append(actualFields, field)
	}
	assertStringSet(t, actualFields, fields)
	assertStringSet(t, stringArray(t, document["required"]), required)
}

func assertSchemaRequired(t *testing.T, name string, required []string) {
	t.Helper()
	document := readSchema(t, name)
	assertStringSet(t, stringArray(t, document["required"]), required)
}

func assertStringSet(t *testing.T, actual, expected []string) {
	t.Helper()
	actual = append([]string(nil), actual...)
	expected = append([]string(nil), expected...)
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("contract shape drift: actual=%s expected=%s", strings.Join(actual, ","), strings.Join(expected, ","))
	}
}

func assertErrorCatalogCoverage(t *testing.T, manifest fixtureManifest) {
	t.Helper()
	codes := make(map[string]fixtureError, len(manifest.Shape.ErrorCatalog))
	for _, item := range manifest.Shape.ErrorCatalog {
		codes[item.Code] = item
	}
	for _, item := range manifest.Cases {
		if !item.Valid || item.Contract != "error" {
			continue
		}
		var failure Error
		if err := DecodeStrict(item.Value, &failure); err != nil {
			t.Fatal(err)
		}
		expected, exists := codes[failure.Code]
		if !exists || expected.Message != failure.Message || expected.Retryable != failure.Retryable {
			t.Fatalf("error fixture drift for %s", failure.Code)
		}
		delete(codes, failure.Code)
	}
	if len(codes) != 0 {
		t.Fatalf("error catalog lacks fixtures: %v", codes)
	}
}
