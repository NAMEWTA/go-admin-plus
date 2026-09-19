package desktop_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	desktopplatform "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

func TestControlBoundaryRejectsOriginAndMissingSecret(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, err := newProductHost(root+"/data", root+"/logs", "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789", cancel)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatal(err)
	}
	port := host.Port()
	if port == 0 {
		t.Fatal("sidecar did not bind an operating-system-assigned port")
	}
	defer drainHost(t, host)

	client := &http.Client{Timeout: time.Second}
	request, _ := http.NewRequest(http.MethodGet, hostURL(port, "/api/iam/session/current"), nil)
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing control status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodGet, hostURL(port, "/api/iam/session/current"), nil)
	request.Header.Set(desktopplatform.ControlHeader, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	request.Header.Set("Origin", "tauri://localhost")
	response, err = client.Do(request)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	ready, _ := http.NewRequest(http.MethodGet, hostURL(port, "/__desktop/ready"), nil)
	ready.Header.Set(desktopplatform.ReadinessHeader, "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789")
	response, err = client.Do(ready)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("readiness status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	ready, _ = http.NewRequest(http.MethodGet, hostURL(port, "/__desktop/ready"), nil)
	ready.Header.Set(desktopplatform.ReadinessHeader, "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789")
	response, err = client.Do(ready)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("replayed readiness status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	metrics, _ := http.NewRequest(http.MethodGet, hostURL(port, "/metrics"), nil)
	response, err = client.Do(metrics)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("uncontrolled metrics status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	metrics, _ = http.NewRequest(http.MethodGet, hostURL(port, "/metrics"), nil)
	metrics.Header.Set(desktopplatform.ControlHeader, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	response, err = client.Do(metrics)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("controlled metrics status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
	status, _ := http.NewRequest(http.MethodGet, hostURL(port, "/api/v1/runtime/status"), nil)
	status.Header.Set(desktopplatform.ControlHeader, "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	response, err = client.Do(status)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("controlled runtime status=%v err=%v", responseStatus(response), err)
	}
	_ = response.Body.Close()
}

func TestHostHoldsInstanceLockUntilDrain(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := newProductHost(root+"/data", root+"/logs-one", "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789", cancel)
	if err != nil || first.Start(ctx) != nil {
		t.Fatalf("start first host: %v", err)
	}
	second, err := newProductHost(root+"/data", root+"/logs-two", "YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY", cancel)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(ctx); err == nil {
		t.Fatal("second host acquired the same data-root lock")
	}
	drainHost(t, first)
	restarted, err := newProductHost(root+"/data", root+"/logs-two", "YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY", cancel)
	if err != nil || restarted.Start(ctx) != nil {
		t.Fatalf("instance lock was not released after drain: %v", err)
	}
	drainHost(t, restarted)
}

func newProductHost(dataDirectory, logDirectory, readinessNonce string, stop context.CancelFunc) (*desktophost.Host, error) {
	return desktophost.New(desktophost.Config{
		Launch: desktopplatform.LaunchMaterial{
			DataDirectory: dataDirectory, LogDirectory: logDirectory, LoopbackPort: 0,
			ReadinessNonce: readinessNonce,
			ControlToken:   "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		},
		Version: "test", Stop: stop, Build: product.BuildDesktop,
	})
}

func drainHost(t *testing.T, host *desktophost.Host) {
	t.Helper()
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := host.Drain(shutdown); err != nil {
		t.Fatal(err)
	}
}

func hostURL(port uint16, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

func responseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
