package files

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultMaximumAccountBytes      int64 = 1024 * 1024 * 1024
	DefaultMaximumAccountObjects    int64 = 1_000
	DefaultMaximumTotalBytes        int64 = 10 * 1024 * 1024 * 1024
	DefaultMaximumTotalObjects      int64 = 10_000
	DefaultMinimumAvailableBytes    int64 = 256 * 1024 * 1024
	DefaultMinimumAvailableFraction       = 0.05
)

var (
	ErrQuotaExceeded = errors.New("files quota exceeded")
	ErrDiskCapacity  = errors.New("files disk capacity unavailable")
	ErrSizeMismatch  = errors.New("files declared size mismatch")
)

type Capacity struct {
	AvailableBytes int64
	TotalBytes     int64
}

type CapacityProbe interface {
	Capacity(context.Context) (Capacity, error)
}

type CapacityPolicy struct {
	MaximumObjectBytes       int64
	MaximumAccountBytes      int64
	MaximumAccountObjects    int64
	MaximumTotalBytes        int64
	MaximumTotalObjects      int64
	MinimumAvailableBytes    int64
	MinimumAvailableFraction float64
	ReservationTTL           time.Duration
	ReconcileBatchSize       int
}

func WithCapacityPolicy(policy CapacityPolicy) Option {
	return func(service *Service) { service.capacityPolicy = policy }
}

func WithCapacityProbe(probe CapacityProbe) Option {
	return func(service *Service) { service.capacityProbe = probe }
}

func DefaultCapacityPolicy() CapacityPolicy {
	return CapacityPolicy{
		MaximumObjectBytes:       DefaultMaximumContentBytes,
		MaximumAccountBytes:      DefaultMaximumAccountBytes,
		MaximumAccountObjects:    DefaultMaximumAccountObjects,
		MaximumTotalBytes:        DefaultMaximumTotalBytes,
		MaximumTotalObjects:      DefaultMaximumTotalObjects,
		MinimumAvailableBytes:    DefaultMinimumAvailableBytes,
		MinimumAvailableFraction: DefaultMinimumAvailableFraction,
		ReservationTTL:           15 * time.Minute,
		ReconcileBatchSize:       recoveryBatchSize,
	}
}

func (policy CapacityPolicy) valid() bool {
	return policy.MaximumObjectBytes > 0 &&
		policy.MaximumAccountBytes >= policy.MaximumObjectBytes &&
		policy.MaximumTotalBytes >= policy.MaximumAccountBytes &&
		policy.MaximumAccountObjects > 0 &&
		policy.MaximumTotalObjects >= policy.MaximumAccountObjects &&
		policy.MinimumAvailableBytes >= 0 &&
		policy.MinimumAvailableFraction > 0 && policy.MinimumAvailableFraction < 1 &&
		policy.ReservationTTL > 0 && policy.ReconcileBatchSize > 0 && policy.ReconcileBatchSize <= 1_000
}

func (policy CapacityPolicy) accepts(capacity Capacity, reservationBytes int64) bool {
	if capacity.TotalBytes <= 0 || capacity.AvailableBytes < 0 || capacity.AvailableBytes > capacity.TotalBytes || reservationBytes < 0 {
		return false
	}
	remaining := capacity.AvailableBytes - reservationBytes
	return remaining >= policy.MinimumAvailableBytes && float64(remaining)/float64(capacity.TotalBytes) >= policy.MinimumAvailableFraction
}

type unavailableCapacityProbe struct{}

func (unavailableCapacityProbe) Capacity(context.Context) (Capacity, error) {
	return Capacity{}, ErrDiskCapacity
}
