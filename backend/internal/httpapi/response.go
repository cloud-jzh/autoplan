package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/config"
)

const contentTypeJSON = "application/json; charset=utf-8"

var errInvalidResponseStatus = errors.New("invalid response status")

type errorEnvelope struct {
	Code      ErrorCode     `json:"code"`
	Message   string        `json:"message"`
	RequestID string        `json:"request_id"`
	Retryable bool          `json:"retryable"`
	Details   *ErrorDetails `json:"details,omitempty"`
}

// WriteJSON buffers encoding before committing headers so encoding failures do
// not produce a partially valid API response.
func WriteJSON(writer http.ResponseWriter, status int, value any) error {
	body, err := encodeJSON(status, value)
	if err != nil {
		return err
	}
	return writeEncodedJSON(writer, status, body)
}

func encodeJSON(status int, value any) ([]byte, error) {
	if status < 100 || status > 599 {
		return nil, errInvalidResponseStatus
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

func writeEncodedJSON(writer http.ResponseWriter, status int, body []byte) error {
	secureJSONHeaders(writer.Header())
	writer.WriteHeader(status)
	_, err := writer.Write(body)
	return err
}

// WriteResponse turns encoding failures into the same generic internal error
// envelope before any application response bytes have been committed.
func WriteResponse(writer http.ResponseWriter, request *http.Request, status int, value any) {
	body, err := encodeJSON(status, value)
	if err != nil {
		WriteError(writer, request, NewAPIError(CodeInternal, nil))
		return
	}
	_ = writeEncodedJSON(writer, status, body)
}

func WriteError(writer http.ResponseWriter, request *http.Request, failure APIError) {
	if _, exists := errorCatalog[failure.code]; !exists {
		failure = NewAPIError(CodeInternal, nil)
	}
	requestID := RequestID(request.Context())
	if requestID == "" {
		requestID = unavailableRequestID
	}
	writer.Header().Set(RequestIDHeader, requestID)
	if marker, ok := writer.(interface{ setAPIError(ErrorCode, bool) }); ok {
		marker.setAPIError(failure.code, failure.retry)
	}
	_ = WriteJSON(writer, failure.status, errorEnvelope{
		Code: failure.code, Message: failure.message, RequestID: requestID,
		Retryable: failure.retry, Details: failure.details,
	})
}

func secureJSONHeaders(header http.Header) {
	header.Set("Content-Type", contentTypeJSON)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
}

// DecodeJSON enforces content type, a hard byte limit, unknown-field rejection,
// exactly one JSON value, and generic errors that never echo parser input.
func DecodeJSON(writer http.ResponseWriter, request *http.Request, destination any, limit int64) *APIError {
	destinationValue := reflect.ValueOf(destination)
	if destination == nil || destinationValue.Kind() != reflect.Pointer || destinationValue.IsNil() ||
		limit <= 0 || limit > config.MaximumBodyLimit {
		failure := NewAPIError(CodeInternal, nil)
		return &failure
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" &&
		!(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		failure := NewAPIError(CodeUnsupportedMediaType, nil)
		return &failure
	}
	if request.Body == nil {
		failure := NewAPIError(CodeInvalidJSON, nil)
		return &failure
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		failure := classifyDecodeError(err, limit)
		return &failure
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		failure := classifyDecodeError(err, limit)
		return &failure
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || !validJSONStructure(raw) {
		failure := NewAPIError(CodeInvalidJSON, nil)
		return &failure
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(destination); err != nil {
		failure := NewAPIError(CodeInvalidJSON, nil)
		return &failure
	}
	if err := strict.Decode(&trailing); err != io.EOF {
		failure := NewAPIError(CodeInvalidJSON, nil)
		return &failure
	}
	return nil
}

func validJSONStructure(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
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

func classifyDecodeError(err error, limit int64) APIError {
	var maximum *http.MaxBytesError
	if errors.As(err, &maximum) {
		return NewAPIError(CodeBodyTooLarge, &ErrorDetails{LimitBytes: limit})
	}
	return NewAPIError(CodeInvalidJSON, nil)
}
