//go:build linux

package pidfd_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/pidfd"
	"golang.org/x/sys/unix"
)

func TestFileSendSignalChild(t *testing.T) {
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
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Start a child process which waits 1 hour. It will either receive
			// SIGKILL on context cancel or will receive the intended signal via
			// pidfd.
			cmd := exec.CommandContext(ctx, "sleep", "1h")
			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to exec sleep: %v", err)
			}

			f, err := pidfd.Open(cmd.Process.Pid)
			if err != nil {
				t.Fatalf("failed to open child pidfd: %v", err)
			}
			defer f.Close()

			if err := f.SendSignal(tt.sig); err != nil {
				t.Fatalf("failed to signal child process: %v", err)
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
	f, err := pidfd.Open(1)
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
		// Copy FD; we don't care about the actual number.
		FD:  perr.FD,
		Err: os.NewSyscallError("pidfd_send_signal", unix.EPERM),
	}

	if diff := cmp.Diff(want, perr); diff != "" {
		t.Fatalf("unexpected Error (-want +got):\n%s", diff)
	}
}

func TestOpenNotExist(t *testing.T) {
	// Chances are Pretty Good(tm) that this PID won't be in use when this test
	// runs on any given Linux machine.
	if _, err := pidfd.Open(12345678); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not exist, but got: %v", err)
	}
}
