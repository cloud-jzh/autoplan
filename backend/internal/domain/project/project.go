// Package project owns authorization-neutral Project invariants.
package project

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound      = errors.New("project not found")
	ErrUnavailable   = errors.New("project repository unavailable")
	ErrInvalidRecord = errors.New("project record is invalid")
	ErrRunning       = errors.New("project is running")
	ErrRelation      = errors.New("project relation conflict")
)

const DefaultName = "未命名项目"

// Project keeps database column names out of application and transport code.
// Timestamps are validated UTC strings at the application boundary.
type Project struct {
	ID            int64
	Name          string
	WorkspacePath string
	Description   string
	CreatedAt     string
	UpdatedAt     string
}

type Create struct {
	Name          string
	WorkspacePath string
	Description   string
}

type Update struct {
	Name          *string
	WorkspacePath *string
	Description   *string
}

func NormalizeCreate(input Create) Create {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = titleFromWorkspace(input.WorkspacePath)
	}
	return input
}

func ValidateCreate(input Create) error {
	normalized := NormalizeCreate(input)
	if strings.TrimSpace(normalized.Name) == "" || len(normalized.Name) > 200 ||
		len(normalized.WorkspacePath) > 4096 || strings.ContainsRune(normalized.WorkspacePath, 0) ||
		len(normalized.Description) > 10000 {
		return ErrInvalidRecord
	}
	return nil
}

func ValidateRecord(value Project) error {
	if value.ID <= 0 || strings.TrimSpace(value.Name) == "" || ValidateCreate(Create{
		Name: value.Name, WorkspacePath: value.WorkspacePath, Description: value.Description,
	}) != nil || !ValidUTCTimestamp(value.CreatedAt) || !ValidUTCTimestamp(value.UpdatedAt) {
		return ErrInvalidRecord
	}
	created, _ := time.Parse(time.RFC3339Nano, value.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, value.UpdatedAt)
	if created.After(updated) {
		return ErrInvalidRecord
	}
	return nil
}

func ValidUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

// ApplyUpdate preserves Node compatibility: an empty/falsy name retains the
// current name, while explicit empty workspace/description values clear them.
func ApplyUpdate(current Project, update Update) (Project, error) {
	if current.ID <= 0 || strings.TrimSpace(current.Name) == "" {
		return Project{}, ErrInvalidRecord
	}
	result := current
	if update.Name != nil {
		if name := strings.TrimSpace(*update.Name); name != "" {
			result.Name = name
		}
	}
	if update.WorkspacePath != nil {
		result.WorkspacePath = *update.WorkspacePath
	}
	if update.Description != nil {
		result.Description = *update.Description
	}
	if ValidateCreate(Create{Name: result.Name, WorkspacePath: result.WorkspacePath, Description: result.Description}) != nil {
		return Project{}, ErrInvalidRecord
	}
	return result, nil
}

func titleFromWorkspace(workspace string) string {
	for _, line := range strings.Split(strings.ReplaceAll(workspace, "\r\n", "\n"), "\n") {
		if title := strings.TrimSpace(line); title != "" {
			characters := []rune(title)
			if len(characters) > 80 {
				characters = characters[:80]
			}
			return string(characters)
		}
	}
	return DefaultName
}

// Visibility is supplied by an authenticated inbound adapter. Workspace paths
// are local capabilities and remain empty unless the adapter grants access.
type Visibility struct {
	WorkspacePath bool
}

const (
	RedactedEnvironment = "<redacted-env-vars>"
	DefaultMCPHost      = "127.0.0.1"
	DefaultMCPPort      = int64(43847)
	DefaultMCPTransport = "http"
)
