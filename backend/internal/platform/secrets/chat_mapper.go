package secrets

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
)

var ErrChatCredential = errors.New("chat credential is unavailable")

const maximumChatCredentialBytes = 64 << 10

type ChatHTTPProvider string

const (
	ChatOpenAIHTTP    ChatHTTPProvider = "openai"
	ChatAnthropicHTTP ChatHTTPProvider = "anthropic"
)

// ChatMapper is the sole point at which a Chat secret reference becomes a
// transient request header. It owns no persistence and exposes no DTO-safe
// representation of secret material.
type ChatMapper struct{ provider Provider }

type HTTPHeader struct {
	Name  string
	Value string
}

// Clear shortens the lifetime of material held by a caller-owned header after
// the outbound request has completed. It must never be logged or serialized.
func (header *HTTPHeader) Clear() {
	if header == nil {
		return
	}
	header.Name, header.Value = "", ""
}

type ChatEnvironment struct {
	Name      string
	Binding   domainsecrets.Binding
	Reference string
}

func NewChatMapper(provider Provider) *ChatMapper { return &ChatMapper{provider: provider} }

func (mapper *ChatMapper) ResolveHTTP(
	ctx context.Context,
	provider ChatHTTPProvider,
	binding domainsecrets.Binding,
	reference string,
) (HTTPHeader, error) {
	if mapper == nil || mapper.provider == nil || !validChatBinding(binding, reference) ||
		(provider != ChatOpenAIHTTP && provider != ChatAnthropicHTTP) {
		return HTTPHeader{}, ErrChatCredential
	}
	if ctx == nil {
		ctx = context.Background()
	}
	value, err := mapper.provider.Get(ctx, binding, reference)
	if err != nil {
		return HTTPHeader{}, ErrChatCredential
	}
	defer clearBytes(value)
	if len(value) == 0 || len(value) > maximumChatCredentialBytes || !utf8.Valid(value) || strings.ContainsAny(string(value), "\x00\r\n") {
		return HTTPHeader{}, ErrChatCredential
	}
	secret := string(value)
	if provider == ChatAnthropicHTTP {
		return HTTPHeader{Name: "x-api-key", Value: secret}, nil
	}
	return HTTPHeader{Name: "Authorization", Value: "Bearer " + secret}, nil
}

// ClaudeEnvironment maps only opaque binding metadata. The Process Runner
// resolves it immediately before spawn, keeping the token out of arguments,
// ordinary process specs, and Chat application values.
func ClaudeEnvironment(binding domainsecrets.Binding, reference string) (ChatEnvironment, error) {
	if !validChatBinding(binding, reference) {
		return ChatEnvironment{}, ErrChatCredential
	}
	return ChatEnvironment{Name: "ANTHROPIC_AUTH_TOKEN", Binding: binding, Reference: reference}, nil
}

func validChatBinding(binding domainsecrets.Binding, reference string) bool {
	return domainsecrets.ValidateScope(binding.Kind, binding.Owner) == nil && binding.Version > 0 &&
		ValidateReference(reference) == nil
}
