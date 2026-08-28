package ui

import (
	"bytes"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"

	"errors"

	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"

	"context"

	"testing"

	"time"

	"github.com/creack/pty"
)

type testOutputWriteCloser struct {
	io.Writer
	close func() error
}

func (writer testOutputWriteCloser) Close() error {
	if writer.close != nil {
		return writer.close()
	}
	return nil
}

type failingTestWriter struct{}

func (failingTestWriter) Write([]byte) (int, error) { return 0, errors.New("persist failed") }

func TestTUILifecycleFlow(t *testing.T) {
	t.Run("tui-exit-keys-cancel-workers-before-leaving", func(t *testing.T) {
		for _, test := range []struct {
			key         string
			quitPending bool
		}{
			{key: "esc", quitPending: false},
			{key: "q", quitPending: true},
			{key: "ctrl+c", quitPending: false},
		} {
			t.Run(test.key, func(t *testing.T) {
				view := sampleTUIView()
				view.running = true
				cancelled := false
				view.cancel = func() { cancelled = true }
				if quit := handleTUIKey(context.Background(), &view, test.key, TUIActions{}, make(chan tuiEvent, 1)); quit {
					t.Fatal("TUI exited before active workers reported completion")
				}
				if cancelled != (test.key != "ctrl+c") || view.quitPending != test.quitPending {
					t.Fatalf("cancelled=%v quitPending=%v", cancelled, view.quitPending)
				}
			})
		}
	})
	t.Run("tui-input-failure-cancels-workers-before-leaving", func(t *testing.T) {
		view := sampleTUIView()
		view.running = true
		cancelled := false
		view.cancel = func() { cancelled = true }
		quit := handleTUIEvent(context.Background(), &view, tuiEvent{eventType: "input_error", err: errors.New("input failed")}, TUIActions{}, make(chan tuiEvent, 1))
		if quit || !cancelled || !view.quitPending {
			t.Fatalf("quit=%v cancelled=%v quitPending=%v", quit, cancelled, view.quitPending)
		}
	})
}

func TestTUIOutputLifecycle(t *testing.T) {
	t.Run("writer-bounds-progress-and-preserves-display-on-persistence-failure", func(t *testing.T) {
		events := make(chan tuiEvent, 4)
		writer := &tuiWriter{events: events}
		if _, err := writer.Write([]byte("10%\r20%\n")); err != nil {
			t.Fatal(err)
		}
		first, second := <-events, <-events
		if first.text != "10%" || second.text != "20%" {
			t.Fatalf("progress events = %#v, %#v", first, second)
		}
		if _, err := writer.Write([]byte(strings.Repeat("x", tuiMaxLogLineBytes+10))); err != nil {
			t.Fatal(err)
		}
		if event := <-events; len(event.text) > tuiMaxLogLineBytes+len("…") || len(writer.buffer) > tuiMaxLogLineBytes {
			t.Fatalf("unbounded line: event=%d buffer=%d", len(event.text), len(writer.buffer))
		}

		output := make(chan tuiEvent, 2)
		open := func(model.Config, string, string, string, string) (io.WriteCloser, error) {
			return testOutputWriteCloser{Writer: failingTestWriter{}, close: func() error { return errors.New("close failed") }}, nil
		}
		stdout, stderr := newTUIDownloadOutput(model.Config{}, func(_ model.Config, _ string, _ string, _ string, message string) ([]string, error) {
			return []string{message}, nil
		}, open, output, model.Application{ID: "app", Name: "App"})
		if count, err := stdout.Write([]byte("download output\n")); err != nil || count != len("download output\n") {
			t.Fatalf("write = %d, %v", count, err)
		}
		if err := stdout.Close(); err != nil {
			t.Fatal(err)
		}
		if err := stderr.Close(); err != nil {
			t.Fatal(err)
		}
		if event := <-output; event.text != "download output" {
			t.Fatalf("display event = %#v", event)
		}
	})
	t.Run("command-output-keeps-one-complete-record-per-application", func(t *testing.T) {
		type persisted struct{ operation, appID, appName, message string }
		var saved []persisted
		events := make(chan tuiEvent, 4)
		router := &tuiCommandOutputRouter{
			commands: map[tuiCommandOutputKey]*tuiCommandOutputState{}, events: events,
			format: func(_ model.Config, _ string, operation, subject, message string) ([]string, error) {
				return []string{operation + "|" + subject + "|" + strings.TrimSpace(message)}, nil
			},
			open: func(_ model.Config, _ string, operation, appID, appName string) (io.WriteCloser, error) {
				var buffer bytes.Buffer
				return testOutputWriteCloser{Writer: &buffer, close: func() error { saved = append(saved, persisted{operation, appID, appName, buffer.String()}); return nil }}, nil
			},
		}
		router.Write(model.CommandOutput{CommandID: 7, AppID: "osv", AppName: "osv-scanner", Operation: model.OperationUpdate, Data: []byte("go: downloading\n")})
		router.Write(model.CommandOutput{CommandID: 7, AppID: "codex", AppName: "OpenAI Codex CLI", Operation: model.OperationCheck, Data: []byte("codex-cli 0.149.1\n")})
		router.Write(model.CommandOutput{CommandID: 7, AppID: "codex", AppName: "OpenAI Codex CLI", Operation: model.OperationCheck, Done: true})
		router.Write(model.CommandOutput{CommandID: 7, AppID: "osv", AppName: "osv-scanner", Operation: model.OperationUpdate, Done: true})
		first, second := <-events, <-events
		if first.text != "update|osv-scanner|go: downloading" || second.text != "check|OpenAI Codex CLI|codex-cli 0.149.1" {
			t.Fatalf("display identity crossed: %q, %q", first.text, second.text)
		}
		if len(saved) != 2 || saved[0] != (persisted{model.OperationCheck, "codex", "OpenAI Codex CLI", "codex-cli 0.149.1\n"}) || saved[1] != (persisted{model.OperationUpdate, "osv", "osv-scanner", "go: downloading\n"}) {
			t.Fatalf("persisted identity crossed: %#v", saved)
		}
	})
	t.Run("format-log-lines-retains-context", func(t *testing.T) {
		lines := FormatLogLines(time.Date(2026, 8, 14, 10, 20, 30, 456000000, time.Local), LogError, "update", "pypdf", "first\nsecond")
		if len(lines) != 2 || !strings.HasSuffix(lines[1], "second") {
			t.Fatalf("lines = %#v", lines)
		}
		for _, line := range lines {
			for _, part := range []string{"[ERROR]", "[UPDATE  ]", "[pypdf]"} {
				if !strings.Contains(line, part) {
					t.Fatalf("line missing %q: %q", part, line)
				}
			}
		}
	})
}

const tuiPTYTimeout = 3 * time.Second

type tuiPTYHarness struct {
	t        *testing.T
	master   *os.File
	slave    *os.File
	original syscall.Termios
	cancel   context.CancelFunc
	done     chan error

	mu      sync.Mutex
	output  bytes.Buffer
	updates chan struct{}
	exited  bool
}

func newTUIPTYHarness(t *testing.T, actions TUIActions) *tuiPTYHarness {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &tuiPTYHarness{t: t, master: master, slave: slave, original: termiosForTUIPTY(t, slave), cancel: cancel, done: make(chan error, 1), updates: make(chan struct{}, 1)}
	go h.captureOutput()
	go func() { h.done <- RunTUI(ctx, slave, slave, actions, ModeNever) }()
	t.Cleanup(h.close)
	return h
}

func (h *tuiPTYHarness) captureOutput() {
	buffer := make([]byte, 4096)
	for {
		count, err := h.master.Read(buffer)
		if count > 0 {
			h.mu.Lock()
			_, _ = h.output.Write(buffer[:count])
			h.mu.Unlock()
			select {
			case h.updates <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *tuiPTYHarness) close() {
	h.cancel()
	_ = h.master.Close()
	_ = h.slave.Close()
	h.mu.Lock()
	exited := h.exited
	h.mu.Unlock()
	if exited {
		return
	}
	select {
	case <-h.done:
	case <-time.After(tuiPTYTimeout):
		h.t.Errorf("RunTUI did not stop during PTY cleanup")
	}
}

func (h *tuiPTYHarness) write(data string) {
	h.t.Helper()
	if _, err := io.WriteString(h.master, data); err != nil {
		h.t.Fatal(err)
	}
}

func (h *tuiPTYHarness) resize(rows, columns uint16) {
	h.t.Helper()
	if err := pty.Setsize(h.slave, &pty.Winsize{Rows: rows, Cols: columns}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *tuiPTYHarness) contains(marker string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Contains(h.output.Bytes(), []byte(marker))
}

func (h *tuiPTYHarness) containsSince(offset int, marker string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return offset <= h.output.Len() && bytes.Contains(h.output.Bytes()[offset:], []byte(marker))
}

func (h *tuiPTYHarness) outputLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.output.Len()
}

func (h *tuiPTYHarness) waitForNewOutput(previous int) {
	h.t.Helper()
	timer := time.NewTimer(tuiPTYTimeout)
	defer timer.Stop()
	for h.outputLen() <= previous {
		select {
		case <-h.updates:
		case err := <-h.done:
			h.t.Fatalf("RunTUI returned before redraw: %v", err)
		case <-timer.C:
			h.t.Fatal("timed out waiting for TUI redraw")
		}
	}
}

func (h *tuiPTYHarness) waitFor(marker string) {
	h.t.Helper()
	timer := time.NewTimer(tuiPTYTimeout)
	defer timer.Stop()
	for !h.contains(marker) {
		h.mu.Lock()
		exited := h.exited
		h.mu.Unlock()
		select {
		case <-h.updates:
		case err := <-h.done:
			if exited {
				continue
			}
			h.t.Fatalf("RunTUI returned before output %q: %v", marker, err)
		case <-timer.C:
			h.t.Fatalf("timed out waiting for output %q", marker)
		}
	}
}

func (h *tuiPTYHarness) waitForSince(offset int, marker string) {
	h.t.Helper()
	timer := time.NewTimer(tuiPTYTimeout)
	defer timer.Stop()
	for !h.containsSince(offset, marker) {
		select {
		case <-h.updates:
		case err := <-h.done:
			h.t.Fatalf("RunTUI returned before output %q: %v", marker, err)
		case <-timer.C:
			h.t.Fatalf("timed out waiting for new output %q", marker)
		}
	}
}

func (h *tuiPTYHarness) waitForExit() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.mu.Lock()
		h.exited = true
		h.mu.Unlock()
		return err
	case <-time.After(tuiPTYTimeout):
		h.t.Fatal("timed out waiting for RunTUI exit")
		return nil
	}
}

type tuiPTYActionSpy struct {
	mu           sync.Mutex
	runRequests  []TUIRunRequest
	scanRequests []TUIScanRequest
	started      chan struct{}
}

func (spy *tuiPTYActionSpy) recordRun(request TUIRunRequest) {
	spy.mu.Lock()
	spy.runRequests = append(spy.runRequests, request)
	spy.mu.Unlock()
	select {
	case spy.started <- struct{}{}:
	default:
	}
}

func (spy *tuiPTYActionSpy) recordScan(request TUIScanRequest) {
	spy.mu.Lock()
	spy.scanRequests = append(spy.scanRequests, request)
	spy.mu.Unlock()
	select {
	case spy.started <- struct{}{}:
	default:
	}
}

func (spy *tuiPTYActionSpy) waitForStart(t *testing.T) {
	t.Helper()
	select {
	case <-spy.started:
	case <-time.After(tuiPTYTimeout):
		t.Fatal("timed out waiting for TUI action request")
	}
}

func tuiPTYActions() (TUIActions, *tuiPTYActionSpy) {
	view := sampleTUIView()
	catalog := cloneConfig(view.catalog)
	catalog.Apps = append(catalog.Apps, model.Application{ID: "zed", Name: "Zed", Type: model.ApplicationTypeBundle, Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}})
	state := cloneTUIState(view.state)
	spy := &tuiPTYActionSpy{started: make(chan struct{}, 2)}
	return TUIActions{
		Load: func() (model.Config, model.RuntimeState, error) {
			return cloneConfig(catalog), cloneTUIState(state), nil
		},
		Reload: func() (model.Config, model.RuntimeState, error) {
			return cloneConfig(catalog), cloneTUIState(state), nil
		},
		SaveConfig:       func(_, proposed model.Config) (model.Config, error) { return cloneConfig(proposed), nil },
		SaveScan:         func(_, proposed model.Config) (model.Config, error) { return cloneConfig(proposed), nil },
		GenerateIdentity: func(application model.Application) (string, error) { return application.ID, nil },
		StartRun: func(ctx context.Context, request TUIRunRequest, observer TUIObserver) (*TUIRunBatch, error) {
			spy.recordRun(request)
			for _, name := range request.Names {
				observer.AppStart(model.Result{AppID: name, Name: name, Status: model.StatusStarted})
			}
			for line := 0; line < 24; line++ {
				observer.CommandOutput(model.CommandOutput{AppID: request.Names[0], AppName: request.Names[0], Operation: model.OperationUpdate, Stream: "stdout", Data: []byte(fmt.Sprintf("PTY update log %02d\n", line))})
			}
			return &TUIRunBatch{
				AddRequest: func(request TUIRunRequest) error {
					spy.recordRun(request)
					return nil
				},
				WaitResult: func() (model.Config, []model.Result, error) {
					<-ctx.Done()
					return cloneConfig(catalog), nil, ctx.Err()
				},
			}, nil
		},
		Scan: func(ctx context.Context, request TUIScanRequest, observer TUIScanObserver) (TUIScanSnapshot, error) {
			spy.recordScan(request)
			for line := 0; line < 24; line++ {
				observer.Progress("prepare", fmt.Sprintf("PTY scan log %02d", line))
			}
			<-ctx.Done()
			return TUIScanSnapshot{}, ctx.Err()
		},
	}, spy
}

func TestTUITerminalLifecycleFlow(t *testing.T) {
	t.Run("tui-pty-input-waiter", func(t *testing.T) {
		master, slave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer master.Close()
		defer slave.Close()
		ready, err := waitTUIInput(slave, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			t.Fatal("idle PTY unexpectedly readable")
		}
	})
	t.Run("tui-pty-starts-resizes-quits-and-restores-terminal", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		actions, _ := tuiPTYActions()
		h := newTUIPTYHarness(t, actions)
		h.waitFor(tuiEnterScreen)
		if raw := termiosForTUIPTY(t, h.slave); raw.Lflag&syscall.ICANON != 0 {
			t.Fatal("TUI did not enter raw terminal mode")
		}
		previous := h.outputLen()
		h.resize(20, 70)
		h.waitForNewOutput(previous)
		h.waitFor(i18n.T("tui.too_small", 70, 20))
		previous = h.outputLen()
		h.resize(36, 120)
		h.waitForNewOutput(previous)
		h.waitFor("\x1b[36;1H")
		h.write("q")
		if err := h.waitForExit(); err != nil {
			t.Fatalf("RunTUI = %v, want nil", err)
		}
		h.waitFor(tuiExitScreen)
		if restored := termiosForTUIPTY(t, h.slave); restored != h.original {
			t.Fatal("TUI did not restore terminal attributes after exit")
		}
	})
	t.Run("tui-pty-cancels-running-update-and-scan-then-quits", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		for _, journey := range []struct {
			name string
		}{
			{name: "update"},
			{name: "scan"},
		} {
			t.Run(journey.name, func(t *testing.T) {
				actions, spy := tuiPTYActions()
				h := newTUIPTYHarness(t, actions)
				h.waitFor(i18n.T("tui.title"))
				h.write("\x1b[B") // Select Zed before starting the operation.
				if journey.name == "update" {
					h.write("c")
				} else {
					h.write("\x13\x1b[Bt")
				}
				spy.waitForStart(t)
				spy.mu.Lock()
				if journey.name == "update" {
					if len(spy.runRequests) != 1 || len(spy.runRequests[0].Names) != 1 || spy.runRequests[0].Names[0] != "zed" {
						t.Fatalf("update requests = %#v, want selected zed", spy.runRequests)
					}
				} else if len(spy.scanRequests) != 1 || spy.scanRequests[0].Application == nil || spy.scanRequests[0].Application.ID != "zed" {
					t.Fatalf("scan requests = %#v, want selected zed", spy.scanRequests)
				}
				spy.mu.Unlock()
				if journey.name == "update" {
					h.write("\x1b[A") // Move from Zed to Obsidian while the batch is active.
					h.write("c")
					spy.waitForStart(t)
					spy.mu.Lock()
					if len(spy.runRequests) != 2 || len(spy.runRequests[1].Names) != 1 || spy.runRequests[1].Names[0] != "obsidian" {
						t.Fatalf("running update requests = %#v, want second selected obsidian request", spy.runRequests)
					}
					spy.mu.Unlock()
				} else {
					h.write("\x1b[A") // Exercise selection while the scan is active before opening logs.
				}
				log := "PTY update log 23"
				if journey.name == "scan" {
					log = "PTY scan log 23"
				}
				h.waitFor(log)
				previous := h.outputLen()
				h.write("l")
				h.waitForSince(previous, i18n.T("tui.key.back_logs_only"))
				previous = h.outputLen()
				h.write("\x1b[5~")
				h.waitForNewOutput(previous)
				previous = h.outputLen()
				h.write("\x1b[F")
				h.waitForNewOutput(previous)
				h.write("l") // Leave the scan log focus before applying ESC cancellation.

				h.write("\x1b")
				h.write("q")
				if err := h.waitForExit(); err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("RunTUI = %v", err)
				}
			})
		}
	})
}

func TestWaitForTUIScanHasFiniteGracePeriod(t *testing.T) {
	view := tuiModel{scanPageState: scanPageState{scanRunning: true}, viewportState: viewportState{width: 80, height: 24}}
	started := time.Now()
	if waitForTUIScan(&view, TUIActions{}, make(chan tuiEvent), &bytes.Buffer{}, 20*time.Millisecond) {
		t.Fatal("stuck scanner was reported as stopped")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("scanner shutdown wait was not bounded: %s", elapsed)
	}
}
