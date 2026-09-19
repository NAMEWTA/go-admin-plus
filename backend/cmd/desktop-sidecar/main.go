package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	desktopplatform "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

const (
	shutdownTimeout = 5 * time.Second
	version         = "0.0.3"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "desktop sidecar failed")
		os.Exit(1)
	}
}

func run() error {
	parentPipe := bufio.NewReader(os.Stdin)
	material, err := desktopplatform.ReadLaunchMaterial(parentPipe)
	if err != nil {
		return err
	}
	parent, cancelSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelSignals()
	runCtx, stop := context.WithCancel(parent)
	go cancelWhenParentPipeCloses(parentPipe, stop)
	host, err := desktophost.New(desktophost.Config{
		Launch: material, Version: version, Stop: stop, Build: product.BuildDesktop,
	})
	if err != nil {
		return err
	}
	if err := host.Start(runCtx); err != nil {
		return err
	}
	status := struct {
		State string `json:"state"`
		Port  uint16 `json:"port"`
	}{State: "listening", Port: host.Port()}
	if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return errors.Join(err, host.Drain(shutdownCtx))
	}
	select {
	case <-runCtx.Done():
	case serveErr := <-host.ServeErrors():
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			err = errors.New("desktop sidecar server stopped")
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(err, host.Drain(shutdownCtx))
}

func cancelWhenParentPipeCloses(reader io.Reader, stop context.CancelFunc) {
	if reader == nil || stop == nil {
		return
	}
	var unexpected [1]byte
	_, _ = reader.Read(unexpected[:])
	stop()
}
