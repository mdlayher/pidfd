//go:build linux

package pidfd_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/pidfd"
	"golang.org/x/sys/unix"
)

func TestFileSendSignalChild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sig  os.Signal
	}{
		{
			name: "INT",
			sig:  os.Interrupt,
		},
		{
			name: "HUP",
			sig:  unix.SIGHUP,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Start a child process which waits 1 hour. It will either receive
			// SIGKILL on context cancel or will receive the intended signal via
			// pidfd.
			ctx, f, cmd := testSleepFile(t, 1*time.Hour)

			if err := f.SendSignal(tt.sig); err != nil {
				t.Fatalf("failed to signal child process: %v", err)
			}

			if err := f.Wait(ctx); err != nil {
				t.Fatalf("failed to wait for child process exit: %v", err)
			}

			// Now verify that we received the test signal rather than KILL via
			// context cancel due to the process hanging.
			var eerr *exec.ExitError
			if err := cmd.Wait(); !errors.As(err, &eerr) {
				t.Fatalf("child process terminated but did not return an exit error: %v", err)
			}

			ws, ok := eerr.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatalf("expected syscall.WaitStatus value, but got: %T", eerr.Sys())
			}

			if diff := cmp.Diff(tt.sig, ws.Signal()); diff != "" {
				t.Fatalf("unexpected child exit signal (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFileSendSignalInitErrors(t *testing.T) {
	t.Parallel()

	const pid = 1
	f, err := pidfd.Open(pid)
	if err != nil {
		t.Fatalf("failed to open init pidfd: %v", err)
	}
	defer f.Close()

	// We attempt to signal init, but should lack permission to do so. In the
	// event that the signal were to succeed, we pick a harmless one which won't
	// interrupt systemd:
	//
	// From systemd(1): "SIGUSR2: When this signal is received the systemd
	// manager will log its complete state in human-readable form. The data
	// logged is the same as printed by systemd-analyze dump."
	var perr *pidfd.Error
	err = f.SendSignal(unix.SIGUSR2)
	if !errors.As(err, &perr) {
		t.Fatalf("expected *pidfd.Error, but got: %v", err)
	}

	// Verify errors.Is wrapping.
	if !errors.Is(err, os.ErrPermission) || !errors.Is(perr, os.ErrPermission) {
		t.Fatalf("expected permission denied, but got: %v", err)
	}

	// Verify the underlying error.
	want := &pidfd.Error{
		PID: pid,
		// Copy FD; we don't care about the actual number.
		FD:  perr.FD,
		Err: os.NewSyscallError("pidfd_send_signal", unix.EPERM),
	}

	if diff := cmp.Diff(want, perr); diff != "" {
		t.Fatalf("unexpected Error (-want +got):\n%s", diff)
	}
}

func TestFileWaitProcessExitOK(t *testing.T) {
	t.Parallel()

	// Cleanly exit after 1 second. File.Wait blocks until exit.
	ctx, f, cmd := testSleepFile(t, 1*time.Second)

	if err := f.Wait(ctx); err != nil {
		t.Fatalf("failed to wait for child to exit: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("failed to wait for child process: %v", err)
	}
}

func TestFileWaitProcessExitContextCanceled(t *testing.T) {
	t.Parallel()

	// Cleanly exit after 1 second. File.Wait should not block because the child
	// context is immediately canceled.
	ctx, f, cmd := testSleepFile(t, 1*time.Second)

	ctx, cancel := context.WithCancel(ctx)
	cancel()

	if err := f.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled for wait, but got: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("failed to wait for child process: %v", err)
	}
}

func TestFileWaitRepeatedContextCanceled(t *testing.T) {
	t.Parallel()

	// Start a long-running process so we can wait for it under various
	// scenarios.
	ctx, f, _ := testSleepFile(t, 1*time.Hour)

	wait := func() error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()

		return f.Wait(ctx)
	}

	// Wait for less time than it would take for the process to exit. The
	// context will be canceled and Wait will unblock. Subsequent Wait calls
	// should also work.
	for i := 0; i < 3; i++ {
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled for wait[%d], but got: %v", i, err)
		}
	}

	// Now terminate the child process, but don't bother to check its exit code.
	if err := f.SendSignal(os.Interrupt); err != nil {
		t.Fatalf("failed to signal child process: %v", err)
	}

	if err := f.Wait(ctx); err != nil {
		t.Fatalf("failed to wait for child process exit: %v", err)
	}
}

func TestOpenNotExist(t *testing.T) {
	t.Parallel()

	// Chances are Pretty Good(tm) that this PID won't be in use when this test
	// runs on any given Linux machine.
	if _, err := pidfd.Open(12345678); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not exist, but got: %v", err)
	}
}

func testSleepFile(t *testing.T, d time.Duration) (context.Context, *pidfd.File, *exec.Cmd) {
	t.Helper()

	// Don't block forever.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	// Start a child process which waits 1 hour. It will either receive
	// SIGKILL on context cancel or will receive the intended signal via
	// pidfd.
	cmd := exec.CommandContext(ctx, "sleep", strconv.Itoa(int(d.Seconds())))
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to exec sleep: %v", err)
	}

	t.Cleanup(func() {
		// Forcibly terminate at the end of the test.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	f, err := pidfd.Open(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("failed to open child pidfd: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	return ctx, f, cmd
}
