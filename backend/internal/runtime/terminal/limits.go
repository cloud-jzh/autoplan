// Package terminal owns the platform-neutral P14 PTY runtime boundary. It has
// no repository, HTTP, WebSocket, snapshot, SSE, or audit dependency.
package terminal

import (
	"time"

	"github.com/lyming99/autoplan/backend/internal/config"
)

// Limits is copied from configuration at construction time so a live session
// cannot be widened by a mutable caller. Every quantity affecting memory,
// process lifetime, writes or resize activity is finite.
type Limits struct {
	MaxSessionsGlobal     int
	MaxSessionsPerProject int
	MaxSessionRuntime     time.Duration
	GracePeriod           time.Duration
	MaxInputBytes         int
	MaxInputRateBytes     int
	InputRateWindow       time.Duration
	MaxResizeRate         int
	ResizeRateWindow      time.Duration
	ReadChunkBytes        int
	DefaultCols           int
	DefaultRows           int
}

func limitsFromConfig(value config.TerminalRuntime) (Limits, error) {
	if !value.Valid() {
		return Limits{}, ErrConfiguration
	}
	return Limits{
		MaxSessionsGlobal: value.MaxSessionsGlobal, MaxSessionsPerProject: value.MaxSessionsPerProject,
		MaxSessionRuntime: value.MaxSessionRuntime, GracePeriod: value.GracePeriod,
		MaxInputBytes: value.MaxInputBytes, MaxInputRateBytes: value.MaxInputRateBytes,
		InputRateWindow: value.InputRateWindow, MaxResizeRate: value.MaxResizeRate,
		ResizeRateWindow: value.ResizeRateWindow, ReadChunkBytes: value.ReadChunkBytes,
		DefaultCols: value.DefaultCols, DefaultRows: value.DefaultRows,
	}, nil
}

func (limits Limits) validSize(cols, rows int) bool {
	return cols >= config.TerminalMinimumColumns && cols <= config.TerminalMaximumColumns &&
		rows >= config.TerminalMinimumRows && rows <= config.TerminalMaximumRows
}
