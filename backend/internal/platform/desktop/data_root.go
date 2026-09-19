package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const applicationDataDirectory = "go-admin-plus"

// ResolveDataRoot returns the private platform application-data directory.
// Windows deliberately uses LocalAppData instead of the roaming config root.
func ResolveDataRoot(override string) (string, error) {
	userConfig, configErr := os.UserConfigDir()
	if configErr != nil && strings.TrimSpace(override) == "" && runtime.GOOS != "windows" {
		return "", fmt.Errorf("resolve platform config directory: %w", configErr)
	}
	return resolveDataRoot(runtime.GOOS, override, userConfig, os.Getenv("LOCALAPPDATA"))
}

func resolveDataRoot(goos, override, userConfig, localAppData string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve desktop data override: %w", err)
		}
		return absolute, nil
	}

	root := strings.TrimSpace(userConfig)
	if goos == "windows" {
		root = strings.TrimSpace(localAppData)
		if root == "" {
			return "", errors.New("resolve Windows LocalAppData: LOCALAPPDATA is empty")
		}
	}
	if root == "" {
		return "", errors.New("platform application-data directory is empty")
	}
	return filepath.Join(root, applicationDataDirectory), nil
}
