package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
)

func TestTerminateProcessGroupKillsDescendantsIgnoringTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	script := fmt.Sprintf("trap '' TERM\n( trap '' TERM; while :; do sleep 1; done ) &\nprintf ready > %s\nwait", QuoteShell(ready))
	cmd := exec.Command(HostPlatform().Shell(), "-lc", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatal("stubborn process group did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pgid := cmd.Process.Pid
	started := time.Now()
	TerminateProcessGroup(cmd, 100*time.Millisecond)
	_ = cmd.Wait()
	if time.Since(started) > time.Second {
		t.Fatal("process-group escalation exceeded its grace period")
	}
	for syscall.Kill(-pgid, 0) == nil && time.Now().Before(deadline.Add(time.Second)) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(-pgid, 0) == nil {
		t.Fatal("descendant process survived process-group termination")
	}
}

func TestRunMultilineScript(t *testing.T) {
	r := Runner{IdleTimeout: time.Second}
	result, err := r.Run(context.Background(), "value=ok\nprintf '%s\\n' \"$value\"", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("unexpected output %q", result.Stdout)
	}
}

func TestRunnerUsesSelectedHostShell(t *testing.T) {
	previous := runnerShell
	runnerShell = func() string { return "/bin/sh" }
	t.Cleanup(func() { runnerShell = previous })
	result, err := (Runner{}).Run(context.Background(), "printf selected-shell", nil)
	if err != nil || result.Stdout != "selected-shell" {
		t.Fatalf("Runner selected shell result = %#v, %v", result, err)
	}
}

func TestRunDoesNotInheritSensitiveProcessEnvironment(t *testing.T) {
	t.Setenv("RUNNER_TEST_TOKEN", "process-secret")
	t.Setenv("RUNNER_TEST_SAFE", "inherited")
	result, err := (Runner{IdleTimeout: time.Second}).Run(
		context.Background(),
		`printf '%s|%s' "${RUNNER_TEST_TOKEN-unset}" "$RUNNER_TEST_SAFE"`,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "unset|inherited" {
		t.Fatalf("unexpected filtered environment %q", result.Stdout)
	}
}

func TestRunPassesExplicitSensitiveApplicationEnvironment(t *testing.T) {
	t.Setenv("RUNNER_TEST_TOKEN", "process-secret")
	result, err := (Runner{IdleTimeout: time.Second}).Run(
		context.Background(),
		`printf '%s' "$RUNNER_TEST_TOKEN"`,
		map[string]string{"RUNNER_TEST_TOKEN": "application-secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "application-secret" {
		t.Fatalf("explicit sensitive variable was not passed: %q", result.Stdout)
	}
}

func TestRunExplicitlyInheritsSensitiveApplicationEnvironment(t *testing.T) {
	t.Setenv("RUNNER_TEST_TOKEN", "process-secret")
	result, err := (Runner{IdleTimeout: time.Second}).Run(
		context.Background(),
		`printf '%s' "$RUNNER_TEST_TOKEN"`,
		map[string]string{"RUNNER_TEST_TOKEN": ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "process-secret" {
		t.Fatalf("explicitly inherited sensitive variable was not passed: %q", result.Stdout)
	}
}

func TestIdleTimeoutResetsOnOutput(t *testing.T) {
	// Leave enough startup margin for the login shell while keeping the total
	// runtime longer than one idle interval, so the test still proves resets.
	r := Runner{IdleTimeout: 500 * time.Millisecond}
	result, err := r.Run(context.Background(), "printf a; sleep 0.3; printf b; sleep 0.3; printf c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "abc" {
		t.Fatalf("unexpected output %q", result.Stdout)
	}
}

func TestIdleTimeoutTerminatesSilentCommand(t *testing.T) {
	r := Runner{IdleTimeout: 80 * time.Millisecond}
	_, err := r.Run(context.Background(), "sleep 5", nil)
	var timeout *apperrors.IdleTimeoutError
	if err == nil || !errors.As(err, &timeout) {
		t.Fatalf("expected idle timeout, got %v", err)
	}
}

func TestCancellationTerminatesCommandGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	r := Runner{IdleTimeout: 5 * time.Second}
	started := time.Now()
	_, err := r.Run(ctx, "sleep 5", nil)
	if err != context.Canceled {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("cancellation did not stop the command promptly")
	}
}

func TestDeadlinePreservesContextError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := Runner{IdleTimeout: 5 * time.Second}
	_, err := r.Run(ctx, "sleep 5", nil)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline error, got %v", err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := r.Run(cancelled, "printf should-not-run", nil); err != context.Canceled {
		t.Fatalf("pre-cancelled context error = %v", err)
	}
}

func TestRunTruncatesCapturedOutput(t *testing.T) {
	r := Runner{IdleTimeout: time.Second, CaptureLimit: 32}
	result, err := r.Run(context.Background(), "printf 'abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) > 32+len(truncationMarker) || !strings.HasSuffix(result.Stdout, truncationMarker) {
		t.Fatalf("captured output was not clearly bounded: %q", result.Stdout)
	}
}

func TestRunDeliversOutputDrainedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen strings.Builder
	r := Runner{IdleTimeout: time.Second, OnOutput: func(_ context.Context, output OutputEvent) {
		seen.Write(output.Data)
		if strings.Contains(seen.String(), "first") {
			cancel()
		}
	}}
	result, err := r.Run(ctx, "trap 'printf tail; exit' TERM; printf first; while :; do sleep 1; done", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	if seen.String() != result.Stdout {
		t.Fatalf("callback output = %q, result stdout = %q", seen.String(), result.Stdout)
	}
}
