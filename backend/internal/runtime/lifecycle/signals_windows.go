//go:build windows

package lifecycle

import (
	"os"
	"syscall"
)

// TerminationSignals includes the signals exposed by the Go Windows runtime.
func TerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
