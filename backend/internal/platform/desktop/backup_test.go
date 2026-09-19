package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDatabaseBackupRestoresOriginalAfterFailedMigration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "data", "desktop.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := BackupDatabase(databasePath, filepath.Join(root, "data", "backups"), time.Unix(1, 0))
	if err != nil || !backup.Exists() {
		t.Fatalf("BackupDatabase() exists=%v err=%v", backup.Exists(), err)
	}
	if err := os.Remove(databasePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("partial migration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(databasePath)
	if err != nil || string(content) != "before" {
		t.Fatalf("restored content = %q, err=%v", content, err)
	}
}

func TestDatabaseBackupRemovesFailedFirstDatabaseAndCompanions(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(data, "desktop.db")
	backup, err := BackupDatabase(databasePath, filepath.Join(data, "backups"), time.Unix(2, 0))
	if err != nil || backup.Exists() {
		t.Fatalf("empty BackupDatabase() exists=%v err=%v", backup.Exists(), err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(databasePath+suffix, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := backup.Restore(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(databasePath + suffix); !os.IsNotExist(err) {
			t.Fatalf("partial database companion %q remains: %v", suffix, err)
		}
	}
}

func TestDatabaseBackupRejectsSymlinkDatabaseWithoutReadingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation needs elevated privileges")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(data, "desktop.db")); err != nil {
		t.Fatal(err)
	}
	if backup, err := BackupDatabase(filepath.Join(data, "desktop.db"), filepath.Join(data, "backups"), time.Unix(3, 0)); err == nil || backup.Exists() {
		t.Fatal("symlink database was accepted")
	}
}
