// Package config provides shared Project configuration queries and mutations.
package config

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	applicationidempotency "github.com/lyming99/autoplan/backend/internal/application/idempotency"
	applicationsnapshot "github.com/lyming99/autoplan/backend/internal/application/snapshot"
	domainconfig "github.com/lyming99/autoplan/backend/internal/domain/config"
	"github.com/lyming99/autoplan/backend/internal/domain/contracts"
	domainproject "github.com/lyming99/autoplan/backend/internal/domain/project"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const (
	RouteConfigure = "loop:configure"
	RouteReset     = "loop:reset-config"
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Dependencies struct {
	Assembler   *applicationsnapshot.Assembler
	Writer      repository.Transactional
	Idempotency *applicationidempotency.Service
	Clock       Clock
}

type Service struct {
	assembler   *applicationsnapshot.Assembler
	writer      repository.Transactional
	idempotency *applicationidempotency.Service
	clock       Clock
}

type MutationMetadata struct {
	CallerScope    string
	IdempotencyKey string
	RequestID      string
}

type ConfigureCommand struct {
	ProjectID       int64
	ExpectedVersion int64
	Config          domainconfig.LoopConfig
	Settings        []repository.SettingMutation
	Metadata        MutationMetadata
}

type ResetCommand struct {
	ProjectID       int64
	ExpectedVersion int64
	Settings        []repository.SettingMutation
	Metadata        MutationMetadata
}

func NewService(dependencies Dependencies) *Service {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{
		assembler: dependencies.Assembler, writer: dependencies.Writer,
		idempotency: dependencies.Idempotency, clock: clock,
	}
}

func (service *Service) Get(
	ctx context.Context,
	projectID int64,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	if service == nil || service.assembler == nil {
		return contracts.AppSnapshot{}, domainproject.ErrUnavailable
	}
	return service.assembler.Assemble(ctx, &projectID, visibility)
}

func (service *Service) Configure(
	ctx context.Context,
	command ConfigureCommand,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	return service.mutate(ctx, RouteConfigure, command.ProjectID, command.ExpectedVersion,
		command.Config, false, command.Settings, command.Metadata, visibility)
}

func (service *Service) Reset(
	ctx context.Context,
	command ResetCommand,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	return service.mutate(ctx, RouteReset, command.ProjectID, command.ExpectedVersion,
		domainconfig.DefaultLoopConfig(), true, command.Settings, command.Metadata, visibility)
}

func (service *Service) mutate(
	ctx context.Context,
	route string,
	projectID int64,
	expectedVersion int64,
	value domainconfig.LoopConfig,
	reset bool,
	settings []repository.SettingMutation,
	metadata MutationMetadata,
	visibility domainproject.Visibility,
) (contracts.AppSnapshot, error) {
	if err := service.ready(ctx); err != nil {
		return contracts.AppSnapshot{}, err
	}
	occurredAt := service.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	value, err := domainconfig.NormalizeLoopConfig(value)
	if err != nil {
		return contracts.AppSnapshot{}, err
	}
	settings, err = NormalizeSettings(settings)
	if err != nil {
		return contracts.AppSnapshot{}, err
	}
	prepared, err := service.idempotency.Prepare(applicationidempotency.Request{
		Scope: metadata.CallerScope, Key: metadata.IdempotencyKey, RequestID: metadata.RequestID,
		Route: route, ProjectID: &projectID,
		Payload: struct {
			ProjectID       int64
			ExpectedVersion int64
			Config          domainconfig.LoopConfig
			Reset           bool
			Settings        []repository.SettingMutation
		}{projectID, expectedVersion, value, reset, settings}, OccurredAt: occurredAt,
	})
	if err != nil {
		return contracts.AppSnapshot{}, err
	}
	reference := applicationidempotency.Reference{Kind: "active-project", ProjectID: &projectID}
	err = service.writer.Transact(ctx, func(transaction repository.WriteTransaction) error {
		decision, beginErr := service.idempotency.Begin(ctx, transaction, prepared)
		if beginErr != nil {
			return beginErr
		}
		if decision.Replay {
			reference = decision.Reference
			return nil
		}
		if reset {
			if _, _, resetErr := transaction.ResetLoopConfig(ctx, projectID, expectedVersion, occurredAt); resetErr != nil {
				return resetErr
			}
		} else if _, _, configErr := transaction.PutLoopConfig(ctx, projectID, expectedVersion, value, occurredAt); configErr != nil {
			return configErr
		}
		for _, setting := range settings {
			if _, _, settingErr := transaction.PutSetting(ctx, setting); settingErr != nil {
				return settingErr
			}
		}
		return service.idempotency.Complete(ctx, transaction, prepared, reference, occurredAt)
	})
	if err != nil {
		return contracts.AppSnapshot{}, err
	}
	if reference.Kind != "active-project" || reference.ProjectID == nil {
		return contracts.AppSnapshot{}, repository.ErrTransaction
	}
	return service.assembler.Assemble(ctx, reference.ProjectID, visibility)
}

// NormalizeSettings owns the canonical storage values used by config CAS and
// request fingerprints. Transports must pass typed intent, not reproduce these
// rules independently.
func NormalizeSettings(settings []repository.SettingMutation) ([]repository.SettingMutation, error) {
	result := make([]repository.SettingMutation, len(settings))
	copy(result, settings)
	sort.SliceStable(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	for index := range result {
		if strings.TrimSpace(result[index].Key) == "" ||
			(index > 0 && result[index-1].Key == result[index].Key) {
			return nil, repository.ErrDuplicate
		}
		value, err := normalizeSettingValue(result[index].Key, result[index].Value)
		if err != nil {
			return nil, err
		}
		result[index].Value = value
	}
	return result, nil
}

func normalizeSettingValue(key, value string) (string, error) {
	switch key {
	case "mcp.enabled":
		return normalizedBoolean(value, true)
	case "mcp.portExplicit":
		return normalizedBoolean(value, false)
	case "mcp.transport":
		transport := strings.ToLower(strings.TrimSpace(value))
		if transport == "" {
			return domainproject.DefaultMCPTransport, nil
		}
		if transport != "http" && transport != "stdio" {
			return "", domainconfig.ErrInvalid
		}
		return transport, nil
	case "mcp.host":
		host := strings.ToLower(strings.TrimSpace(value))
		if host == "" {
			return domainproject.DefaultMCPHost, nil
		}
		parsed := net.ParseIP(host)
		if host != "localhost" && (parsed == nil || !parsed.IsLoopback()) {
			return "", domainconfig.ErrInvalid
		}
		return host, nil
	case "mcp.port":
		port := strings.TrimSpace(value)
		if port == "" {
			return strconv.FormatInt(domainproject.DefaultMCPPort, 10), nil
		}
		parsed, err := strconv.ParseInt(port, 10, 64)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return "", domainconfig.ErrInvalid
		}
		return strconv.FormatInt(parsed, 10), nil
	case "mcp.path":
		path := strings.TrimSpace(value)
		if path == "" || path == "/" {
			return "/mcp", nil
		}
		if strings.ContainsRune(path, 0) {
			return "", domainconfig.ErrInvalid
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return path, nil
	case "mcp.authToken":
		return value, nil
	default:
		return "", repository.ErrSettingNotWritable
	}
}

func normalizedBoolean(value string, fallback bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "enabled":
		return "true", nil
	case "0", "false", "off", "disabled":
		return "false", nil
	case "":
		return strconv.FormatBool(fallback), nil
	default:
		return "", domainconfig.ErrInvalid
	}
}

func (service *Service) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.assembler == nil || service.writer == nil || service.idempotency == nil || service.clock == nil {
		return domainproject.ErrUnavailable
	}
	return service.writer.Check(ctx)
}
