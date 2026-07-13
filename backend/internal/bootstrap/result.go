package bootstrap

import (
	"encoding/json"
	"io"
)

const (
	exitOK      = 0
	exitUsage   = 2
	exitBlocked = 3
	exitFailure = 1
)

type commandResult struct {
	SchemaVersion        int      `json:"schema_version"`
	Command              string   `json:"command"`
	Status               string   `json:"status"`
	Code                 string   `json:"code"`
	Reasons              []string `json:"reasons,omitempty"`
	Mode                 string   `json:"mode,omitempty"`
	TargetKind           string   `json:"target_kind,omitempty"`
	RegisteredMigrations int      `json:"registered_migrations"`
	WritePerformed       bool     `json:"write_performed"`
}

func writeResult(output io.Writer, result commandResult) int {
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return exitFailure
	}
	return exitOK
}
