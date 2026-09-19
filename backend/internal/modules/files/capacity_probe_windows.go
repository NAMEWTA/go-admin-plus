//go:build windows

package files

import (
	"context"
	"golang.org/x/sys/windows"
	"math"
)

func (probe diskCapacityProbe) Capacity(ctx context.Context) (Capacity, error) {
	if ctx == nil {
		return Capacity{}, ErrDiskCapacity
	}
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	if probe.rootPath == "" {
		return Capacity{}, ErrDiskCapacity
	}
	path, err := windows.UTF16PtrFromString(probe.rootPath)
	if err != nil {
		return Capacity{}, ErrDiskCapacity
	}
	var available, total, _ uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, &total, nil); err != nil || available > math.MaxInt64 || total > math.MaxInt64 {
		return Capacity{}, ErrDiskCapacity
	}
	return Capacity{AvailableBytes: int64(available), TotalBytes: int64(total)}, nil
}
