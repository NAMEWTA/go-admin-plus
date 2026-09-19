package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnsurePrivateDirectory validates every existing ancestor before creating a
// host-owned directory. It never chmods through a symbolic link.
func EnsurePrivateDirectory(directory string) (string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", errors.New("desktop directory path is invalid")
	}
	if err := validateExistingAncestors(directory); err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", errors.New("desktop directory cannot be created")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("desktop directory is unsafe")
	}
	real, err := filepath.EvalSymlinks(directory)
	if err != nil || filepath.Clean(real) != directory {
		return "", errors.New("desktop directory is not canonical")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", errors.New("desktop directory cannot be secured")
	}
	return directory, nil
}

func validateExistingAncestors(directory string) error {
	volume := filepath.VolumeName(directory)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return errors.New("desktop directory path is invalid")
	}
	current := filepath.Clean(root)
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("desktop directory path is invalid")
		}
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && current != directory) {
			return errors.New("desktop directory ancestor is unsafe")
		}
	}
	return nil
}
