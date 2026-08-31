package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eoctet/tendkit/internal/service"
	"github.com/eoctet/tendkit/internal/ui"
)

func captureCommandOutput(t *testing.T, execute func()) (string, string) {
	t.Helper()
	directory := t.TempDir()
	stdoutPath := filepath.Join(directory, "stdout")
	stderrPath := filepath.Join(directory, "stderr")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		_ = stdout.Close()
		t.Fatal(err)
	}

	previousStdout, previousStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	active := true
	defer func() {
		if active {
			os.Stdout, os.Stderr = previousStdout, previousStderr
			_ = stdout.Close()
			_ = stderr.Close()
		}
	}()
	execute()
	os.Stdout, os.Stderr = previousStdout, previousStderr
	if err := stdout.Close(); err != nil {
		_ = stderr.Close()
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	active = false

	stdoutText, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	stderrText, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(stdoutText), string(stderrText)
}

func runForTest(t *testing.T, arguments []string) (int, string, string) {
	t.Helper()
	code := 0
	stdout, stderr := captureCommandOutput(t, func() { code = run(arguments) })
	return code, stdout, stderr
}

func runWithTUIForTest(t *testing.T, arguments []string, execute func(context.Context, *service.Service, ui.Mode) error) (int, string, string) {
	t.Helper()
	code := 0
	stdout, stderr := captureCommandOutput(t, func() { code = runWithTUI(arguments, execute) })
	return code, stdout, stderr
}
