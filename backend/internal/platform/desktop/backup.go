package desktop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type DatabaseBackup struct {
	databasePath string
	directory    string
	files        []string
}

func (backup DatabaseBackup) Exists() bool     { return backup.directory != "" }
func (DatabaseBackup) String() string          { return "desktop.DatabaseBackup{paths:redacted}" }
func (backup DatabaseBackup) GoString() string { return backup.String() }

// BackupDatabase copies the closed SQLite database and journal companions before migration.
// Backups are immutable recovery artifacts and are never deleted automatically.
func BackupDatabase(databasePath, backupRoot string, now time.Time) (DatabaseBackup, error) {
	databasePath = filepath.Clean(strings.TrimSpace(databasePath))
	backupRoot = filepath.Clean(strings.TrimSpace(backupRoot))
	if !filepath.IsAbs(databasePath) || !filepath.IsAbs(backupRoot) || databasePath == backupRoot ||
		filepath.Dir(backupRoot) != filepath.Dir(databasePath) {
		return DatabaseBackup{}, errors.New("desktop database backup paths are invalid")
	}
	dataRoot, err := canonicalPrivateDirectory(filepath.Dir(databasePath))
	if err != nil || dataRoot != filepath.Dir(databasePath) {
		return DatabaseBackup{}, errors.New("desktop database root is unsafe")
	}
	if info, err := os.Lstat(databasePath); errors.Is(err, os.ErrNotExist) {
		return DatabaseBackup{databasePath: databasePath}, nil
	} else if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return DatabaseBackup{}, errors.New("desktop database is unavailable for backup")
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return DatabaseBackup{}, errors.New("desktop backup directory is unavailable")
	}
	canonicalBackupRoot, err := canonicalPrivateDirectory(backupRoot)
	if err != nil || canonicalBackupRoot != backupRoot {
		return DatabaseBackup{}, errors.New("desktop backup directory is unsafe")
	}
	incomplete, err := os.MkdirTemp(backupRoot, ".incomplete-")
	if err != nil {
		return DatabaseBackup{}, errors.New("desktop backup cannot be created")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(incomplete)
		}
	}()
	backup := DatabaseBackup{databasePath: databasePath}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := databasePath + suffix
		info, statErr := os.Lstat(source)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return DatabaseBackup{}, errors.New("desktop database companion is unavailable")
		}
		name := filepath.Base(databasePath) + suffix
		if err := copyPrivateFile(source, filepath.Join(incomplete, name)); err != nil {
			return DatabaseBackup{}, err
		}
		backup.files = append(backup.files, name)
	}
	if len(backup.files) == 0 {
		return DatabaseBackup{}, errors.New("desktop database backup is empty")
	}
	if err := syncDirectory(incomplete); err != nil {
		return DatabaseBackup{}, errors.New("desktop backup cannot be synchronized")
	}
	finalDirectory := filepath.Join(backupRoot, now.UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(incomplete, finalDirectory); err != nil {
		return DatabaseBackup{}, errors.New("desktop backup cannot be published")
	}
	if err := syncDirectory(backupRoot); err != nil {
		return DatabaseBackup{}, errors.New("desktop backup publication cannot be synchronized")
	}
	complete = true
	backup.directory = finalDirectory
	return backup, nil
}

// Restore replaces migration-touched files with the recovery snapshot after the database closes.
func (backup DatabaseBackup) Restore() error {
	if !backup.Exists() {
		if backup.databasePath == "" {
			return nil
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(backup.databasePath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("desktop new database recovery cleanup failed")
			}
		}
		return nil
	}
	dataRoot, err := canonicalPrivateDirectory(filepath.Dir(backup.databasePath))
	if err != nil || dataRoot != filepath.Dir(backup.databasePath) {
		return errors.New("desktop database recovery root is unsafe")
	}
	backupInfo, err := os.Lstat(backup.directory)
	if err != nil || !backupInfo.IsDir() || backupInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("desktop database recovery snapshot is unsafe")
	}
	staging, err := os.MkdirTemp(dataRoot, ".restore-staging-")
	if err != nil {
		return errors.New("desktop database recovery staging failed")
	}
	defer os.RemoveAll(staging)
	rollback, err := os.MkdirTemp(dataRoot, ".restore-rollback-")
	if err != nil {
		return errors.New("desktop database recovery rollback failed")
	}
	rollbackOwned := true
	defer func() {
		if rollbackOwned {
			_ = os.RemoveAll(rollback)
		}
	}()
	for _, name := range backup.files {
		source := filepath.Join(backup.directory, name)
		info, statErr := os.Lstat(source)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("desktop database recovery snapshot is invalid")
		}
		if err := copyPrivateFile(source, filepath.Join(staging, name)); err != nil {
			return errors.New("desktop database recovery failed")
		}
	}
	if err := syncDirectory(staging); err != nil {
		return errors.New("desktop database recovery staging sync failed")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		target := backup.databasePath + suffix
		if info, statErr := os.Lstat(target); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("desktop database recovery target is unavailable")
		}
		if err := os.Rename(target, filepath.Join(rollback, filepath.Base(target))); err != nil {
			_ = restoreRollback(rollback, dataRoot)
			return errors.New("desktop database recovery rollback preparation failed")
		}
		// From this point the rollback directory is the durable recovery source.
		// Preserve it if any subsequent restoration step fails.
		rollbackOwned = false
	}
	if err := syncDirectory(rollback); err != nil {
		_ = restoreRollback(rollback, dataRoot)
		return errors.New("desktop database recovery rollback sync failed")
	}
	if err := syncDirectory(dataRoot); err != nil {
		_ = restoreRollback(rollback, dataRoot)
		return errors.New("desktop database recovery rollback publication failed")
	}
	installed := make([]string, 0, len(backup.files))
	for _, name := range backup.files {
		target := filepath.Join(dataRoot, name)
		if err := os.Rename(filepath.Join(staging, name), target); err != nil {
			for _, path := range installed {
				_ = os.Remove(path)
			}
			_ = restoreRollback(rollback, dataRoot)
			return errors.New("desktop database recovery replacement failed")
		}
		installed = append(installed, target)
	}
	if err := syncDirectory(dataRoot); err != nil {
		for _, path := range installed {
			_ = os.Remove(path)
		}
		_ = restoreRollback(rollback, dataRoot)
		return errors.New("desktop database recovery sync failed")
	}
	if err := os.RemoveAll(rollback); err != nil {
		return errors.New("desktop database recovery cleanup failed")
	}
	if err := syncDirectory(dataRoot); err != nil {
		return errors.New("desktop database recovery cleanup sync failed")
	}
	rollbackOwned = false
	return nil
}

func restoreRollback(rollback, dataRoot string) error {
	entries, err := os.ReadDir(rollback)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return errors.New("invalid rollback entry")
		}
		if err := os.Rename(filepath.Join(rollback, entry.Name()), filepath.Join(dataRoot, entry.Name())); err != nil {
			return err
		}
	}
	return syncDirectory(dataRoot)
}

func canonicalPrivateDirectory(directory string) (string, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("directory is unsafe")
	}
	real, err := filepath.EvalSymlinks(directory)
	if err != nil || filepath.Clean(real) != filepath.Clean(directory) {
		return "", errors.New("directory is not canonical")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("directory is not private")
	}
	return filepath.Clean(real), nil
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		// Windows does not expose a portable directory fsync through os.File.
		return nil
	}
	owner, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer owner.Close()
	return owner.Sync()
}

func copyPrivateFile(source, destination string) (resultErr error) {
	linkedInfo, err := os.Lstat(source)
	if err != nil || !linkedInfo.Mode().IsRegular() || linkedInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("desktop backup source is unsafe")
	}
	input, err := os.Open(source)
	if err != nil {
		return errors.New("desktop backup source cannot be opened")
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	openedInfo, err := input.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(linkedInfo, openedInfo) {
		return errors.New("desktop backup source changed during open")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("desktop backup destination cannot be created")
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	if _, err := io.Copy(output, input); err != nil {
		return errors.New("desktop backup copy failed")
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("desktop backup sync failed: %w", err)
	}
	return nil
}
