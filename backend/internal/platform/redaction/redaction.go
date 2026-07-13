// Package redaction removes secrets and unsafe diagnostics before values cross
// a logging, API, event, fixture, or evidence boundary.
package redaction

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	Replacement      = "<redacted>"
	RedactedError    = "<redacted_error>"
	RedactedBinary   = "<redacted_binary>"
	RedactedCycle    = "<redacted_cycle>"
	RedactedOverflow = "<redacted_overflow>"
	maximumDepth     = 8
	maximumItems     = 128
)

var (
	assignmentPattern = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|session|api[_-]?key|auth[_-]?token|access[_-]?token|refresh[_-]?token|token|env[_-]?vars|password|secret|credential)\s*([:=])\s*([^,;\r\n]+)`)
	bearerPattern     = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/-]+`)
	apiKeyPattern     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	jwtPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	queryPattern      = regexp.MustCompile(`(?i)([?&](?:authorization|cookie|session|api[_-]?key|auth[_-]?token|access[_-]?token|refresh[_-]?token|token|env[_-]?vars|password|secret|credential)=)[^&#\s]*`)
	windowsPath       = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^\s"']+`)
	unixPrivatePath   = regexp.MustCompile(`(?:/home/|/Users/|/private/|/tmp/|/var/tmp/)[^\s"']+`)
)

// SensitiveKey is deliberately conservative. Unknown credential-like fields
// are removed even when nested or written with different separators/case.
func SensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	for _, marker := range []string{
		"authorization", "proxyauthorization", "cookie", "setcookie", "session",
		"apikey", "authtoken", "accesstoken", "refreshtoken", "token",
		"envvars", "password", "secret", "credential", "privatekey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	var builder strings.Builder
	for _, character := range key {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
		}
	}
	return builder.String()
}

// String removes common header, assignment, query, and private absolute-path
// forms. Callers should still prefer fixed safe fields over arbitrary text.
func String(value string) string {
	value = bearerPattern.ReplaceAllString(value, `${1}`+Replacement)
	value = apiKeyPattern.ReplaceAllString(value, Replacement)
	value = jwtPattern.ReplaceAllString(value, Replacement)
	value = assignmentPattern.ReplaceAllString(value, `${1}${2}`+Replacement)
	value = queryPattern.ReplaceAllString(value, `${1}`+Replacement)
	value = windowsPath.ReplaceAllString(value, Replacement)
	value = unixPrivatePath.ReplaceAllString(value, Replacement)
	return value
}

// Fields returns a detached, recursively sanitized map. The traversal is
// bounded and replaces error chains instead of serializing their messages.
func Fields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	value := sanitize(reflect.ValueOf(fields), 0, make(map[visit]struct{}))
	result, _ := value.(map[string]any)
	return result
}

// Value sanitizes supported JSON-like values while bounding recursion.
func Value(value any) any {
	return sanitize(reflect.ValueOf(value), 0, make(map[visit]struct{}))
}

type visit struct {
	typeName reflect.Type
	pointer  uintptr
}

func sanitize(value reflect.Value, depth int, seen map[visit]struct{}) any {
	if !value.IsValid() {
		return nil
	}
	if depth > maximumDepth {
		return RedactedOverflow
	}
	if value.CanInterface() {
		if _, ok := value.Interface().(error); ok {
			return RedactedError
		}
		if timestamp, ok := value.Interface().(time.Time); ok {
			return timestamp.UTC().Format(time.RFC3339Nano)
		}
		if parsedURL, ok := value.Interface().(url.URL); ok {
			parsedURL.User = nil
			parsedURL.RawQuery = ""
			parsedURL.ForceQuery = false
			parsedURL.Fragment = ""
			parsedURL.RawFragment = ""
			parsedURL.Opaque = ""
			return String(parsedURL.String())
		}
		if raw, ok := value.Interface().(json.RawMessage); ok {
			if len(raw) == 0 {
				return nil
			}
			return RedactedBinary
		}
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return sanitize(value.Elem(), depth+1, seen)
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if _, exists := seen[key]; exists {
			return RedactedCycle
		}
		seen[key] = struct{}{}
		defer delete(seen, key)
		return sanitize(value.Elem(), depth+1, seen)
	case reflect.String:
		return String(value.String())
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		result := make(map[string]any)
		iterator := value.MapRange()
		for count := 0; iterator.Next() && count < maximumItems; count++ {
			mapKey := iterator.Key()
			if mapKey.Kind() != reflect.String {
				continue
			}
			name := mapKey.String()
			if SensitiveKey(name) {
				result[name] = Replacement
				continue
			}
			result[name] = sanitize(iterator.Value(), depth+1, seen)
		}
		return result
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return RedactedBinary
		}
		length := value.Len()
		if length > maximumItems {
			length = maximumItems
		}
		result := make([]any, 0, length)
		for index := 0; index < length; index++ {
			result = append(result, sanitize(value.Index(index), depth+1, seen))
		}
		return result
	case reflect.Struct:
		result := make(map[string]any)
		typeInfo := value.Type()
		for index := 0; index < value.NumField() && index < maximumItems; index++ {
			field := typeInfo.Field(index)
			if !field.IsExported() {
				continue
			}
			name := field.Name
			if tag := strings.Split(field.Tag.Get("json"), ",")[0]; tag != "" {
				if tag == "-" {
					continue
				}
				name = tag
			}
			if SensitiveKey(name) {
				result[name] = Replacement
				continue
			}
			result[name] = sanitize(value.Field(index), depth+1, seen)
		}
		return result
	default:
		return fmt.Sprintf("<redacted_%s>", value.Kind())
	}
}
