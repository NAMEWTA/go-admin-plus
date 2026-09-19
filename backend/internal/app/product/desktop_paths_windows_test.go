//go:build windows && !desktop_native_e2e

package product

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	desktopplatform "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

// 覆盖 Tauri 实际传入的扩展路径，验证首次建库和已有数据库重启均可就绪。
func TestDesktopHostWindowsExtendedPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = `\\?\` + strings.TrimPrefix(root, `\\?\`)
	for _, name := range []string{"first-launch", "restart-with-backup"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nonce := strings.Repeat("n", 43)
			host, err := desktophost.New(desktophost.Config{
				Launch: desktopplatform.LaunchMaterial{
					DataDirectory:  filepath.Join(root, "data with spaces"),
					LogDirectory:   filepath.Join(root, "logs"),
					ReadinessNonce: nonce,
					ControlToken:   strings.Repeat("t", 43),
				},
				Version: "test", Stop: cancel, Build: BuildDesktop,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := host.Drain(ctx); err != nil {
					t.Error(err)
				}
			}()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/__desktop/ready", host.Port()), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Go-Admin-Desktop-Nonce", nonce)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("desktop readiness status = %d", response.StatusCode)
			}
		})
	}
}
