//go:build !windows

package files

import (
	"context"
	"math"
	"syscall"
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
	var stat syscall.Statfs_t
	if err := syscall.Statfs(probe.rootPath, &stat); err != nil {
		return Capacity{}, ErrDiskCapacity
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := uint64(stat.Bavail)
	totalBlocks := uint64(stat.Blocks)
	if blockSize == 0 || availableBlocks > math.MaxInt64/blockSize || totalBlocks > math.MaxInt64/blockSize {
		return Capacity{}, ErrDiskCapacity
	}
	return Capacity{AvailableBytes: int64(availableBlocks * blockSize), TotalBytes: int64(totalBlocks * blockSize)}, nil
}
