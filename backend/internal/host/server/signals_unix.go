//go:build !windows

package server

import (
	"os"
	"syscall"
)

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
