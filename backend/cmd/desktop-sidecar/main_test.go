package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const parentPipeHelper = "GO_ADMIN_DESKTOP_PARENT_PIPE_HELPER"

func TestSidecarStopsWhenItsParentPipeCloses(t *testing.T) {
	if os.Getenv(parentPipeHelper) == "1" {
		if err := run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestSidecarStopsWhenItsParentPipeCloses$")
	command.Env = append(os.Environ(), parentPipeHelper+"=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = nil
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	payload := map[string]any{
		"dataDirectory":  filepath.Join(root, "data"),
		"logDirectory":   filepath.Join(root, "logs"),
		"loopbackPort":   0,
		"readinessNonce": "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789",
		"controlToken":   strings.Repeat("Z", 43),
	}
	if err := json.NewEncoder(stdin).Encode(payload); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr == nil && !strings.Contains(line, `"state":"listening"`) {
			readErr = fmt.Errorf("unexpected readiness")
		}
		ready <- readErr
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("sidecar readiness: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sidecar readiness timed out")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("sidecar parent EOF exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("sidecar ignored parent pipe EOF")
	}
}
