//go:build darwin

package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

const tuiPTYTimeout = 3 * time.Second

func TestTUIPTYInputWaiter(t *testing.T) {
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
}

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

func termiosForTUIPTY(t *testing.T, file *os.File) syscall.Termios {
	t.Helper()
	var state syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&state)), 0, 0, 0)
	if errno != 0 {
		t.Fatalf("TIOCGETA: %v", errno)
	}
	return state
}

func TestTUIPTYStartsResizesQuitsAndRestoresTerminal(t *testing.T) {
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
}

func TestTUIPTYCancelsRunningUpdateAndScanThenQuits(t *testing.T) {
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
}
