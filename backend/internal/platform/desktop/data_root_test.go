package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDataRootForPlatform(t *testing.T) {
	tests := []struct {
		name         string
		goos         string
		userConfig   string
		localAppData string
		want         string
		wantErr      bool
	}{
		{
			name:       "macOS Application Support",
			goos:       "darwin",
			userConfig: filepath.Join("Users", "alice", "Library", "Application Support"),
			want:       filepath.Join("Users", "alice", "Library", "Application Support", "go-admin-plus"),
		},
		{
			name:         "Windows LocalAppData",
			goos:         "windows",
			userConfig:   filepath.Join("Users", "alice", "AppData", "Roaming"),
			localAppData: filepath.Join("Users", "alice", "AppData", "Local"),
			want:         filepath.Join("Users", "alice", "AppData", "Local", "go-admin-plus"),
		},
		{
			name:       "Windows does not fall back to roaming",
			goos:       "windows",
			userConfig: filepath.Join("Users", "alice", "AppData", "Roaming"),
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveDataRoot(test.goos, "", test.userConfig, test.localAppData)
			if test.wantErr {
				if err == nil {
					t.Fatalf("resolveDataRoot = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDataRoot: %v", err)
			}
			if got != test.want {
				t.Fatalf("root = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveDataRootMakesOverrideAbsolute(t *testing.T) {
	got, err := resolveDataRoot("darwin", filepath.Join("relative", "data"), "", "")
	if err != nil {
		t.Fatalf("resolveDataRoot: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("override = %q, want absolute path", got)
	}
}

func TestResolveDataRootUsesNativePlatformDirectory(t *testing.T) {
	got, err := ResolveDataRoot("")
	if err != nil {
		t.Fatalf("ResolveDataRoot: %v", err)
	}

	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			t.Fatal("LOCALAPPDATA is empty on Windows")
		}
		if roaming := os.Getenv("APPDATA"); roaming != "" && filepath.Clean(got) == filepath.Clean(filepath.Join(roaming, applicationDataDirectory)) {
			t.Fatalf("data root = %q, must not use roaming APPDATA", got)
		}
	} else {
		base, err = os.UserConfigDir()
		if err != nil {
			t.Fatalf("UserConfigDir: %v", err)
		}
	}

	want := filepath.Join(base, applicationDataDirectory)
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("data root = %q, want native platform directory %q", got, want)
	}
}
