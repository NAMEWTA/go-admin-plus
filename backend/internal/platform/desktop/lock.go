package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const instanceLockName = ".go-admin-instance.lock"

var ErrInstanceLocked = errors.New("desktop application is already running")

// InstanceLock owns the OS-level lock that prevents a second SQLite writer.
type InstanceLock struct {
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

// AcquireInstanceLock acquires the per-data-root writer lock. The lock file is
// persistent, while the OS lock is released automatically if the process dies.
func AcquireInstanceLock(dataRoot string) (*InstanceLock, error) {
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return nil, errors.New("desktop data root is required for instance lock")
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve desktop lock root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create desktop lock root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve desktop lock root links: %w", err)
	}
	if err := os.Chmod(realRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure desktop lock root: %w", err)
	}
	return acquireInstanceLock(realRoot, false)
}

// AcquireSecureInstanceLock is the Tauri sidecar lock boundary. The caller
// must provide a canonical host-owned root with no symbolic-link ancestors.
func AcquireSecureInstanceLock(dataRoot string) (*InstanceLock, error) {
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return nil, errors.New("desktop data root is required for instance lock")
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, errors.New("desktop lock root is invalid")
	}
	realRoot, err := EnsurePrivateDirectory(absolute)
	if err != nil {
		return nil, errors.New("desktop lock root is unsafe")
	}
	return acquireInstanceLock(realRoot, true)
}

func acquireInstanceLock(realRoot string, secure bool) (*InstanceLock, error) {
	lockPath := filepath.Join(realRoot, instanceLockName)
	linkedInfo, statErr := os.Lstat(lockPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect desktop instance lock: %w", statErr)
	}
	if secure && statErr == nil && (!linkedInfo.Mode().IsRegular() || linkedInfo.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("desktop instance lock is unsafe")
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open desktop instance lock: %w", err)
	}
	if secure {
		openedInfo, openStatErr := file.Stat()
		pathInfo, pathStatErr := os.Lstat(lockPath)
		if openStatErr != nil || pathStatErr != nil || !openedInfo.Mode().IsRegular() ||
			!pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(openedInfo, pathInfo) || (statErr == nil && !os.SameFile(linkedInfo, openedInfo)) ||
			!privateSingleLink(file, openedInfo) {
			_ = file.Close()
			return nil, errors.New("desktop instance lock changed during open")
		}
	}
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire desktop instance lock: %w", err)
	}
	if !locked {
		_ = file.Close()
		return nil, ErrInstanceLocked
	}
	if err := writeLockOwner(file); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	return &InstanceLock{file: file}, nil
}

func writeLockOwner(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate desktop instance lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek desktop instance lock: %w", err)
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write desktop instance lock owner: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync desktop instance lock owner: %w", err)
	}
	return nil
}

// Close releases the OS lock once. The lock file remains for race-free reuse.
func (lock *InstanceLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.closeOnce.Do(func() {
		lock.closeErr = errors.Join(unlockFile(lock.file), lock.file.Close())
	})
	return lock.closeErr
}
