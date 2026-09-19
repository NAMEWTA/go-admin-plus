package files

import "strings"

// NewDiskCapacityProbe binds capacity checks to an absolute storage path.
// Platform-specific implementations live in capacity_probe_*.go.
func NewDiskCapacityProbe(rootPath string) CapacityProbe {
	return diskCapacityProbe{rootPath: strings.TrimSpace(rootPath)}
}

type diskCapacityProbe struct{ rootPath string }
