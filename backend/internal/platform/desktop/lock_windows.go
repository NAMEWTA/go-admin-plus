//go:build windows

package desktop

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32.NewProc("LockFileEx")
	unlockFileExProc = kernel32.NewProc("UnlockFileEx")
)

func tryLockFile(file *os.File) (bool, error) {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := lockFileExProc.Call(
		file.Fd(), lockFileExclusiveLock|lockFileFailImmediately, 0, 1, 0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if callErr == errorLockViolation {
		return false, nil
	}
	return false, callErr
}

func unlockFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := unlockFileExProc.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	if result != 0 {
		return nil
	}
	return callErr
}

func privateSingleLink(file *os.File, info os.FileInfo) bool {
	var opened syscall.ByHandleFileInformation
	err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &opened)
	return err == nil && info.Mode().IsRegular() && opened.NumberOfLinks == 1
}
