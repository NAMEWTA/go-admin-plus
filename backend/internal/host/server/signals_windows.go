//go:build windows

package server

import "os"

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
