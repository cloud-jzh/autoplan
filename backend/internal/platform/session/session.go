// Package session owns the process-local credential shared by REST, SSE, and
// WebSocket handshakes. Credentials never have a String or marshal method.
package session

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"sync"
)

const (
	HeaderName      = "X-Autoplan-Session"
	CookieName      = "autoplan_session"
	materialBytes   = 32
	credentialBytes = 43
)

var ErrGenerationFailed = errors.New("session generation failed")

type Manager struct {
	mu         sync.RWMutex
	credential []byte
	closed     bool
	once       sync.Once
}

func New(source io.Reader) (*Manager, error) {
	if source == nil {
		source = rand.Reader
	}
	raw := make([]byte, materialBytes)
	if _, err := io.ReadFull(source, raw); err != nil {
		zero(raw)
		return nil, ErrGenerationFailed
	}
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(encoded, raw)
	zero(raw)
	return &Manager{credential: encoded}, nil
}

// CredentialCopy is the controlled bootstrap handoff for a future Electron
// process channel. The returned copy must not be logged, persisted, or placed
// in argv, URLs, localStorage, fixtures, readiness, or errors.
func (manager *Manager) CredentialCopy() []byte {
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return nil
	}
	return append([]byte(nil), manager.credential...)
}

func (manager *Manager) AuthenticateRequest(request *http.Request) bool {
	if manager == nil || request == nil {
		return false
	}
	credential, ok := requestCredential(request)
	if !ok {
		return false
	}
	defer zero(credential)
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed || len(manager.credential) != len(credential) {
		return false
	}
	return subtle.ConstantTimeCompare(manager.credential, credential) == 1
}

func requestCredential(request *http.Request) ([]byte, bool) {
	headers := request.Header.Values(HeaderName)
	cookieHeaders := request.Header.Values("Cookie")
	if len(cookieHeaders) > 1 {
		return nil, false
	}
	cookieValue := ""
	cookieCount := 0
	cookies := request.Cookies()
	if len(cookieHeaders) == 1 && len(cookies) == 0 {
		return nil, false
	}
	for _, cookie := range cookies {
		if cookie.Name == CookieName {
			cookieCount++
			cookieValue = cookie.Value
		} else {
			return nil, false
		}
	}
	if len(headers) > 1 || cookieCount > 1 || (len(headers) == 1 && cookieCount == 1) {
		return nil, false
	}
	value := ""
	if len(headers) == 1 {
		value = headers[0]
	} else if cookieCount == 1 {
		value = cookieValue
	} else {
		return nil, false
	}
	if len(value) != credentialBytes {
		return nil, false
	}
	decoded := make([]byte, materialBytes)
	count, err := base64.RawURLEncoding.Decode(decoded, []byte(value))
	zero(decoded)
	if err != nil || count != materialBytes {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

// Cookie returns a host-only cookie: Domain is intentionally absent. Secure is
// selected by the actual sidecar scheme rather than the renderer Origin.
func (manager *Manager) Cookie(secure bool) (*http.Cookie, bool) {
	credential := manager.CredentialCopy()
	if len(credential) == 0 {
		return nil, false
	}
	defer zero(credential)
	return &http.Cookie{
		Name: CookieName, Value: string(credential), Path: "/", Domain: "",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}, true
}

func ClearCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: "", Path: "/", Domain: "", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

func (manager *Manager) Close() {
	if manager == nil {
		return
	}
	manager.once.Do(func() {
		manager.mu.Lock()
		zero(manager.credential)
		manager.credential = nil
		manager.closed = true
		manager.mu.Unlock()
	})
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
