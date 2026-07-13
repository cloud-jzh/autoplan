package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/lyming99/autoplan/backend/internal/platform/session"
)

type mutationContext struct {
	CallerScope    string
	IdempotencyKey string
	RequestID      string
}

// mutationRequestContext derives a non-reversible caller identity only after
// Security has authenticated the request. Raw session material is never
// passed to application services, responses, errors, or logs.
func mutationRequestContext(request *http.Request) (mutationContext, *APIError) {
	key, failure := IdempotencyKey(request)
	if failure != nil {
		return mutationContext{}, failure
	}
	credential := request.Header.Get(session.HeaderName)
	if credential == "" {
		if cookie, err := request.Cookie(session.CookieName); err == nil {
			credential = cookie.Value
		}
	}
	security, authorized := RequestSecurity(request.Context())
	if !authorized || credential == "" {
		failure := NewAPIError(CodeUnauthorized, nil)
		return mutationContext{}, &failure
	}
	digest := sha256.Sum256([]byte("autoplan-p05-http-caller\x00" + credential + "\x00" + security.Origin))
	return mutationContext{
		CallerScope:    "http-" + hex.EncodeToString(digest[:]),
		IdempotencyKey: key,
		RequestID:      RequestID(request.Context()),
	}, nil
}
