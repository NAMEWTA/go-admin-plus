package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstanceLockAllowsOnlyOneWriterAndCanBeReacquired(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "app-data")
	first, err := AcquireInstanceLock(root)
	if err != nil {
		t.Fatalf("Acquire first lock: %v", err)
	}
	second, err := AcquireInstanceLock(root)
	if !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("Acquire second lock error = %v, want ErrInstanceLocked", err)
	}
	if second != nil {
		t.Fatal("second lock is non-nil")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first lock: %v", err)
	}

	reacquired, err := AcquireInstanceLock(root)
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("Close reacquired lock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, instanceLockName))
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("lock permissions = %o, want private", info.Mode().Perm())
	}
}

func TestInstanceLockRejectsSymlinkAndHardlinkFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows link creation needs elevated privileges")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "app-data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "outside")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, instanceLockName)
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if lock, err := AcquireSecureInstanceLock(root); err == nil || lock != nil {
		t.Fatal("symlink lock was accepted")
	}
	if bytes, _ := os.ReadFile(target); string(bytes) != "keep" {
		t.Fatalf("symlink target changed: %q", bytes)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if lock, err := AcquireSecureInstanceLock(root); err == nil || lock != nil {
		t.Fatal("hardlinked lock was accepted")
	}
}
