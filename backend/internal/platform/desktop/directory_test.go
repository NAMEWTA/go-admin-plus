package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsurePrivateDirectoryRejectsSymlinkedParentBeforeCreation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation needs elevated privileges")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsurePrivateDirectory(filepath.Join(linked, "must-not-exist")); err == nil {
		t.Fatal("symlinked ancestor was accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("unsafe creation escaped through symlink: %v", err)
	}
}
