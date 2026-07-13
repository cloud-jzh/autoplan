package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var ErrInvalidContract = errors.New("contract is invalid")

var (
	safePresenceField = regexp.MustCompile(`(?:^|_)has_|(?:^|_)has[A-Z]|Has[A-Z]`)
	safeMaskedField   = regexp.MustCompile(`(?:_masked|Masked)$`)
)

type Validatable interface {
	Validate() error
}

func DecodeStrict(data []byte, destination any) error {
	if destination == nil || !validJSONStructure(data) {
		return ErrInvalidContract
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalidContract
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidContract
	}
	if validatable, ok := destination.(Validatable); ok {
		return validatable.Validate()
	}
	return nil
}

func (object *SanitizedObject) UnmarshalJSON(data []byte) error {
	if object == nil || !validJSONStructure(data) {
		return ErrInvalidContract
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalidContract
	}
	if err := validateSanitizedObject(fields, 0); err != nil {
		return err
	}
	*object = SanitizedObject(fields)
	return nil
}

func (object SanitizedObject) Validate() error {
	if object == nil {
		return ErrInvalidContract
	}
	return validateSanitizedObject(map[string]json.RawMessage(object), 0)
}

func validateSanitizedObject(fields map[string]json.RawMessage, depth int) error {
	if depth > 32 {
		return ErrInvalidContract
	}
	for name, raw := range fields {
		normalized := normalizeField(name)
		if sensitiveField(normalized) {
			if normalized == "workspacepath" && stringJSON(raw) {
				continue
			}
			if normalized == "envvars" && redactedEnvironmentJSON(raw) {
				continue
			}
			if safePresenceField.MatchString(name) && boolJSON(raw) {
				continue
			}
			if safeMaskedField.MatchString(name) && stringJSON(raw) {
				continue
			}
			if strings.Contains(normalized, "authtoken") && maskedTokenJSON(raw) {
				continue
			}
			return ErrInvalidContract
		}
		if normalized == "authheader" {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil ||
				(value != "" && !strings.Contains(value, "<token>")) {
				return ErrInvalidContract
			}
			continue
		}
		if pathField(normalized) && !relativePathOrNull(raw) {
			return ErrInvalidContract
		}
		if err := validateSanitizedValue(raw, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateSanitizedValue(raw json.RawMessage, depth int) error {
	if depth > 32 {
		return ErrInvalidContract
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ErrInvalidContract
	}
	switch trimmed[0] {
	case '{':
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &nested); err != nil || nested == nil {
			return ErrInvalidContract
		}
		return validateSanitizedObject(nested, depth)
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return ErrInvalidContract
		}
		for _, item := range values {
			if err := validateSanitizedValue(item, depth+1); err != nil {
				return err
			}
		}
	default:
		var scalar any
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return ErrInvalidContract
		}
	}
	return nil
}

var nonAlphaNumeric = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeField(value string) string {
	return nonAlphaNumeric.ReplaceAllString(strings.ToLower(value), "")
}

func sensitiveField(value string) bool {
	if strings.Contains(value, "workspacepath") || value == "env" || strings.Contains(value, "envvars") ||
		value == "authorization" || value == "cookie" || value == "password" ||
		strings.Contains(value, "secret") || strings.Contains(value, "credential") ||
		strings.Contains(value, "privatekey") ||
		value == "session" || value == "sessiontoken" || value == "sessionsecret" {
		return true
	}
	for _, marker := range []string{"apikey", "authtoken", "accesstoken", "refreshtoken"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return strings.Contains(value, "token")
}

func pathField(value string) bool {
	return value == "path" || value == "cwd" || value == "workdir" ||
		strings.HasSuffix(value, "logfile") || strings.HasSuffix(value, "path")
}

func relativePathOrNull(raw []byte) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, `\\`) &&
		!(len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') ||
			(value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' &&
			(value[2] == '\\' || value[2] == '/'))
}

func boolJSON(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false"))
}

func stringJSON(raw []byte) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func redactedEnvironmentJSON(raw []byte) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && (value == "" || value == "<redacted-env-vars>")
}

func maskedTokenJSON(raw []byte) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	if value == "" || value == "····" {
		return true
	}
	return strings.HasPrefix(value, "····") && len([]rune(value)) <= 8
}

func validJSONStructure(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !consumeJSONValue(decoder, 0) {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func consumeJSONValue(decoder *json.Decoder, depth int) bool {
	if depth > 128 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return false
			}
			seen[key] = struct{}{}
			if !consumeJSONValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeJSONValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func requireKeys(data []byte, required ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return ErrInvalidContract
	}
	for _, name := range required {
		if _, exists := object[name]; !exists {
			return ErrInvalidContract
		}
	}
	return nil
}
