// Package idempotency implements transport-neutral mutation replay semantics.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/lyming99/autoplan/backend/internal/repository"
)

const maximumFingerprintPayload = 2 << 20

var ErrInvalidRequest = errors.New("idempotency request is invalid")

type Request struct {
	Scope      string
	Key        string
	RequestID  string
	Route      string
	ProjectID  *int64
	Payload    any
	OccurredAt string
}

type Prepared struct {
	Enabled     bool
	Scope       string
	Key         string
	RequestID   string
	Route       string
	ProjectID   *int64
	RequestHash string
	OperationID string
	OccurredAt  string
}

type Reference struct {
	Kind      string `json:"kind"`
	ProjectID *int64 `json:"project_id"`
}

type Decision struct {
	Replay    bool
	Reference Reference
}

type Service struct{}

func New() *Service { return &Service{} }

func (service *Service) Prepare(request Request) (Prepared, error) {
	if strings.TrimSpace(request.Key) == "" {
		return Prepared{}, nil
	}
	callerScope := strings.TrimSpace(request.Scope)
	key := strings.TrimSpace(request.Key)
	requestID := strings.TrimSpace(request.RequestID)
	route := strings.TrimSpace(request.Route)
	if service == nil || !validOpaque(callerScope, 256) || !validOpaque(key, 256) ||
		!validOpaque(requestID, 256) || !validOpaque(route, 128) || strings.TrimSpace(request.OccurredAt) == "" ||
		(request.ProjectID != nil && *request.ProjectID <= 0) {
		return Prepared{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(request.Payload)
	if err != nil || len(payload) == 0 || len(payload) > maximumFingerprintPayload {
		return Prepared{}, ErrInvalidRequest
	}
	projectScope := "none"
	if request.ProjectID != nil {
		projectScope = strconv.FormatInt(*request.ProjectID, 10)
	}
	callerHash := sha256.Sum256([]byte("autoplan-p05-caller\x00" + callerScope))
	scope := "caller-" + hex.EncodeToString(callerHash[:]) + ":" + route + ":" + projectScope
	if !validOpaque(scope, 512) {
		return Prepared{}, ErrInvalidRequest
	}
	fingerprint := sha256.Sum256(append([]byte(route+"\x00"+projectScope+"\x00"), payload...))
	requestHash := hex.EncodeToString(fingerprint[:])
	operationHash := sha256.Sum256([]byte("autoplan-p05-operation\x00" + scope + "\x00" + key + "\x00" + requestHash))
	var projectID *int64
	if request.ProjectID != nil {
		value := *request.ProjectID
		projectID = &value
	}
	return Prepared{
		Enabled: true, Scope: scope, Key: key, RequestID: requestID,
		Route: route, ProjectID: projectID, RequestHash: requestHash,
		OperationID: "op-" + hex.EncodeToString(operationHash[:16]), OccurredAt: request.OccurredAt,
	}, nil
}

func (service *Service) Begin(
	ctx context.Context,
	transaction repository.WriteTransaction,
	prepared Prepared,
) (Decision, error) {
	if !prepared.Enabled {
		return Decision{}, nil
	}
	if service == nil || transaction == nil {
		return Decision{}, ErrInvalidRequest
	}
	existing, found, err := transaction.FindIdempotency(ctx, prepared.Scope, prepared.Key)
	if err != nil {
		return Decision{}, err
	}
	if found {
		if existing.Route != prepared.Route || existing.RequestHash != prepared.RequestHash {
			return Decision{}, repository.ErrIdempotencyKeyReuse
		}
		if !sameProject(existing.ProjectID, prepared.ProjectID) {
			return Decision{}, repository.ErrTransaction
		}
		if existing.Status != "succeeded" || existing.ResultJSON == nil {
			return Decision{}, repository.ErrDuplicate
		}
		reference, err := decodeReference(*existing.ResultJSON)
		if err != nil {
			return Decision{}, err
		}
		return Decision{Replay: true, Reference: reference}, nil
	}
	err = transaction.ReserveIdempotency(ctx, repository.IdempotencyRecord{
		OperationID: prepared.OperationID, ProjectID: prepared.ProjectID, Route: prepared.Route, RequestID: prepared.RequestID,
		Scope: prepared.Scope, Key: prepared.Key, RequestHash: prepared.RequestHash,
		Status: "running", CreatedAt: prepared.OccurredAt, UpdatedAt: prepared.OccurredAt,
	})
	return Decision{}, err
}

func (service *Service) Complete(
	ctx context.Context,
	transaction repository.WriteTransaction,
	prepared Prepared,
	reference Reference,
	updatedAt string,
) error {
	if !prepared.Enabled {
		return nil
	}
	if service == nil || transaction == nil || !validReference(reference) {
		return ErrInvalidRequest
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		return ErrInvalidRequest
	}
	result := string(encoded)
	return transaction.CompleteIdempotency(ctx, prepared.Scope, prepared.Key, "succeeded", &result, nil, updatedAt)
}

func decodeReference(value string) (Reference, error) {
	var reference Reference
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reference) != nil || !validReference(reference) {
		return Reference{}, repository.ErrTransaction
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Reference{}, repository.ErrTransaction
	}
	return reference, nil
}

func validReference(reference Reference) bool {
	switch reference.Kind {
	case "active-project":
		return reference.ProjectID != nil && *reference.ProjectID > 0
	case "project-list":
		return reference.ProjectID == nil
	default:
		return false
	}
}

func sameProject(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func validOpaque(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
