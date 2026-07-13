// Package secrets defines the metadata boundary between business records and
// platform-managed secret material.
package secrets

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidSecretRef = errors.New("secret reference is invalid")
	ErrInvalidSecret    = errors.New("secret value is invalid")
)

const (
	KindAIConfigAPIKey                Kind = "ai_config_api_key"
	KindClaudeCLIAuthToken            Kind = "claude_cli_auth_token"
	KindMCPAuthToken                  Kind = "mcp_auth_token"
	KindPlanGenerationClaudeAuthToken Kind = "plan_generation_claude_auth_token"
	KindPlanExecutionClaudeAuthToken  Kind = "plan_execution_claude_auth_token"
	KindProjectEnvironment            Kind = "project_environment"
	maximumSecretBytes                     = 1 << 20
)

var opaqueReference = regexp.MustCompile(`^[A-Za-z0-9_-]{20,200}$`)
var metadataIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// Kind identifies the semantic secret without storing a provider locator.
type Kind string

// Owner scopes a secret to one business record. IDs are opaque business
// identifiers, never filesystem paths or platform credential locators.
type Owner struct {
	Type string
	ID   string
}

type Binding struct {
	Kind    Kind
	Owner   Owner
	Version int64
}

// Ref is persisted in secret_refs. Reference is intentionally excluded from
// ordinary serialization; only platform services receive it.
type Ref struct {
	ID        int64   `json:"-"`
	Binding   Binding `json:"-"`
	Provider  string  `json:"-"`
	Reference string  `json:"-"`
	HasValue  bool    `json:"-"`
	CreatedAt string  `json:"-"`
	UpdatedAt string  `json:"-"`
	Version   int64   `json:"-"`
}

type Create struct {
	Binding   Binding
	Provider  string
	Reference string
	CreatedAt string
}

type Replace struct {
	Binding         Binding
	ExpectedVersion int64
	Provider        string
	Reference       string
	UpdatedAt       string
}

type Delete struct {
	Binding         Binding
	ExpectedVersion int64
}

type Retire struct {
	Binding         Binding
	ExpectedVersion int64
	UpdatedAt       string
}

func (kind Kind) Valid() bool {
	value := string(kind)
	if !metadataIdentifier.MatchString(value) {
		return false
	}
	if strings.HasPrefix(value, "retired.") {
		return len(value) <= 128
	}
	return len(value) <= 80
}

func (owner Owner) Valid() bool {
	return metadataIdentifier.MatchString(strings.ToLower(strings.TrimSpace(owner.Type))) &&
		validOwnerID(owner.ID)
}

func (binding Binding) Valid() bool {
	return binding.Kind.Valid() && binding.Owner.Valid() && binding.Version > 0
}

func ValidateScope(kind Kind, owner Owner) error {
	if !kind.Valid() || ValidateOwner(owner) != nil {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateOwner(owner Owner) error {
	if !owner.Valid() {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateSecret(value []byte) error {
	if len(value) == 0 || len(value) > maximumSecretBytes {
		return ErrInvalidSecret
	}
	return nil
}

func ValidateRef(value Ref) error {
	if value.ID <= 0 || !value.Binding.Valid() || !metadataIdentifier.MatchString(value.Provider) ||
		!opaqueReference.MatchString(value.Reference) || value.Version <= 0 || value.Binding.Version != value.Version ||
		!ValidUTCTimestamp(value.CreatedAt) || !ValidUTCTimestamp(value.UpdatedAt) ||
		later(value.CreatedAt, value.UpdatedAt) {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateCreate(value Create) error {
	if !value.Binding.Valid() || !metadataIdentifier.MatchString(value.Provider) ||
		!opaqueReference.MatchString(value.Reference) || !ValidUTCTimestamp(value.CreatedAt) {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateReplace(value Replace) error {
	if !value.Binding.Valid() || value.ExpectedVersion <= 0 || !metadataIdentifier.MatchString(value.Provider) ||
		!opaqueReference.MatchString(value.Reference) || !ValidUTCTimestamp(value.UpdatedAt) {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateDelete(value Delete) error {
	if !validBindingWithoutVersion(value.Binding) || value.ExpectedVersion <= 0 {
		return ErrInvalidSecretRef
	}
	return nil
}

func ValidateRetire(value Retire) error {
	if ValidateDelete(Delete{Binding: value.Binding, ExpectedVersion: value.ExpectedVersion}) != nil ||
		!ValidUTCTimestamp(value.UpdatedAt) {
		return ErrInvalidSecretRef
	}
	return nil
}

// RetirementKind identifies a provider-cleanup tombstone without recording a
// provider locator or exposing the original secret reference.
func RetirementKind(binding Binding) Kind {
	return Kind("retired." + string(binding.Kind) + ".v" + strconv.FormatInt(binding.Version, 10))
}

func ParseRetirementBinding(kind Kind, owner Owner) (Binding, bool) {
	value := string(kind)
	if !strings.HasPrefix(value, "retired.") {
		return Binding{}, false
	}
	separator := strings.LastIndex(value, ".v")
	if separator <= len("retired.") || separator == len(value)-2 {
		return Binding{}, false
	}
	version, err := strconv.ParseInt(value[separator+2:], 10, 64)
	if err != nil || version <= 0 {
		return Binding{}, false
	}
	binding := Binding{Kind: Kind(value[len("retired."):separator]), Owner: owner, Version: version}
	return binding, binding.Valid()
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func validBindingWithoutVersion(value Binding) bool {
	return ValidateScope(value.Kind, value.Owner) == nil
}

func validOwnerID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 || strings.ContainsAny(value, "\\/\x00") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '_' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func later(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr != nil || rightErr != nil || leftTime.After(rightTime)
}
