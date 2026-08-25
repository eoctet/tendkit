package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
)

const (
	DefaultCaptureLimit  = 1 << 20
	DefaultIdleTimeout   = 5 * time.Minute
	StreamStdout         = "stdout"
	StreamStderr         = "stderr"
	outputChunkQueueSize = 16
	pipeReadBufferSize   = 8 * 1024
	truncationMarker     = "\n...[output truncated]"
)

var runnerShell = func() string { return HostPlatform().Shell() }

// Result captures bounded output and timing for a shell command.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// Combined joins stdout and stderr for parsers that accept either stream.
func (r Result) Combined() string {
	if r.Stdout == "" {
		return r.Stderr
	}
	if r.Stderr == "" {
		return r.Stdout
	}
	return r.Stdout + "\n" + r.Stderr
}

// Runner executes trusted scripts with bounded output and an idle timeout.
type Runner struct {
	IdleTimeout  time.Duration
	CaptureLimit int
	OnOutput     func(context.Context, OutputEvent)
}

// OutputEvent identifies chunks and completion for one command execution.
type OutputEvent struct {
	CommandID uint64
	Stream    string
	Data      []byte
	Done      bool
}

var commandSequence atomic.Uint64

type limitedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) {
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		b.data = append(b.data, data[:remaining]...)
	}
	if remaining < len(data) {
		b.truncated = true
	}
}

func (b *limitedBuffer) String() string {
	if !b.truncated {
		return string(b.data)
	}
	return string(b.data) + truncationMarker
}

type chunk struct {
	stream string
	data   []byte
}

// Run executes script as one shell argument, so multiline scripts, functions,
// pipelines and heredocs retain their original shell semantics. IdleTimeout is
// reset whenever stdout or stderr produces bytes; it is not a wall-clock limit.
func (r Runner) Run(ctx context.Context, script string, environment map[string]string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	started := time.Now()
	// #nosec G204 -- Executing reviewed provider actions through the platform shell is this runner's documented trust boundary.
	cmd := exec.Command(runnerShell(), "-lc", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = commandEnvironment(environment)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, &apperrors.StartError{Err: err}
	}
	commandID := commandSequence.Add(1)
	if r.OnOutput != nil {
		defer r.OnOutput(ctx, OutputEvent{CommandID: commandID, Done: true})
	}

	chunks := make(chan chunk, outputChunkQueueSize)
	var readers sync.WaitGroup
	readers.Add(2)
	go readPipe(&readers, StreamStdout, stdout, chunks)
	go readPipe(&readers, StreamStderr, stderr, chunks)
	go func() {
		readers.Wait()
		close(chunks)
	}()

	captureLimit := r.CaptureLimit
	if captureLimit <= 0 {
		captureLimit = DefaultCaptureLimit
	}
	out := limitedBuffer{limit: captureLimit}
	errOut := limitedBuffer{limit: captureLimit}
	idle := r.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	timedOut := false
	cancelled := false
	for chunks != nil {
		select {
		case item, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if len(item.data) > 0 {
				r.recordOutput(ctx, commandID, item, &out, &errOut)
				resetTimer(timer, idle)
			}
		case <-timer.C:
			timedOut = true
			TerminateProcessGroup(cmd, ProcessTerminationGracePeriod)
		case <-ctx.Done():
			cancelled = true
			TerminateProcessGroup(cmd, ProcessTerminationGracePeriod)
		}
		if timedOut || cancelled {
			for item := range chunks {
				r.recordOutput(ctx, commandID, item, &out, &errOut)
			}
			chunks = nil
		}
	}
	waitErr := cmd.Wait()
	result := Result{ExitCode: cmd.ProcessState.ExitCode(), Stdout: out.String(), Stderr: errOut.String(), Duration: time.Since(started)}
	if timedOut {
		return result, &apperrors.IdleTimeoutError{Duration: idle}
	}
	if cancelled {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		return result, context.Canceled
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return result, waitErr
		}
	}
	return result, nil
}

func (r Runner) recordOutput(ctx context.Context, commandID uint64, item chunk, out, errOut *limitedBuffer) {
	if item.stream == StreamStdout {
		out.Write(item.data)
	} else {
		errOut.Write(item.data)
	}
	if r.OnOutput != nil {
		r.OnOutput(ctx, OutputEvent{CommandID: commandID, Stream: item.stream, Data: item.data})
	}
}

// IsSensitiveEnvironmentKey reports whether a variable name is likely to hold
// credentials. Sensitive process variables are not inherited by catalog
// commands unless the application explicitly opts in through its environment.
func IsSensitiveEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "SECRET") || strings.Contains(upper, "API_KEY") ||
		strings.Contains(upper, "APIKEY") || strings.Contains(upper, "ACCESS_KEY") ||
		strings.Contains(upper, "PRIVATE_KEY") || strings.Contains(upper, "CREDENTIAL")
}

func commandEnvironment(configured map[string]string) []string {
	values := make(map[string]string, len(os.Environ())+len(configured))
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok && !IsSensitiveEnvironmentKey(key) {
			values[key] = value
		}
	}
	for key, value := range configured {
		if value == "" && IsSensitiveEnvironmentKey(key) {
			if inherited, exists := os.LookupEnv(key); exists {
				value = inherited
			}
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func readPipe(wg *sync.WaitGroup, stream string, source io.Reader, target chan<- chunk) {
	defer wg.Done()
	buffer := make([]byte, pipeReadBufferSize)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			data := append([]byte(nil), buffer[:n]...)
			target <- chunk{stream: stream, data: data}
		}
		if err != nil {
			return
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

// ProcessTerminationGracePeriod is the default delay between terminating and
// forcibly killing an isolated process group.
const ProcessTerminationGracePeriod = 3 * time.Second

// TerminateProcessGroup stops the command and every descendant that remains in
// its isolated process group. Processes that ignore SIGTERM are killed after
// the grace period.
func TerminateProcessGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if grace < 0 {
		grace = 0
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return
		}
	}
}
