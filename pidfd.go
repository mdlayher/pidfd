package pidfd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// A File is a handle to a Linux pidfd. If the process referred to by the pidfd
// no longer exists, File's methods will return an *Error value which is
// compatible with errors.Is(err, os.ErrNotExist).
type File struct {
	pid int
	c   *conn
	rc  syscall.RawConn
}

// Open opens a pidfd File referring to the process identified by pid. If the
// process does not exist, an *Error value is returned which is compatible with
// errors.Is(err, os.ErrNotExist).
func Open(pid int) (*File, error) { return open(pid) }

// Close releases the File's resources.
func (f *File) Close() error { return f.c.Close() }

// SendSignal sends a signal the process referred to by File. Note that
// unix.Signal values also implement os.Signal.
func (f *File) SendSignal(signal os.Signal) error {
	// TODO(mdlayher): expose info parameter?
	return f.sendSignal(signal)
}

// Wait waits for the process referred to by File to exit. If the context is
// canceled, Wait will unblock and return an error.
func (f *File) Wait(ctx context.Context) error {
	// Wait for the poller to indicate readiness, unless the context is canceled
	// first. For values of n:
	//  - 0: init
	//  - 1: callback invoked, wait for process exit
	//  - 2: callback invoked due to process exit, stop waiting
	var n uint32
	return f.readContext(ctx, func(_ uintptr) bool {
		return atomic.AddUint32(&n, 1) == 2
	})
}

// TODO(mdlayher): move into socket.Conn?

// readContext invokes fn, a read function which indicates readiness, against
// the associated file descriptor. readContext will block until the read
// function returns true or the context is canceled. If a context error occurs,
// it supersedes all other errors returned by this function.
func (f *File) readContext(ctx context.Context, fn func(fd uintptr) bool) error {
	// To observe context cancelation, we will set a past deadline in a
	// goroutine to force blocked Reads to unblock.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	go func() {
		defer wg.Done()

		// Immediately unblock pending reads.
		<-ctx.Done()
		_ = f.c.SetReadDeadline(time.Unix(0, 1))
	}()

	rerr := f.rc.Read(func(fd uintptr) bool {
		if ctx.Err() != nil {
			// Context canceled, don't invoke user function.
			return true
		}

		return fn(fd)
	})

	// The operation has unblocked. Observe context cancelation, tidy up the
	// cancelation goroutine, and disarm the read deadline timer.
	cerr := ctx.Err()
	cancel()
	wg.Wait()
	serr := f.c.SetReadDeadline(time.Time{})

	// Context cancel takes priority over all other errors.
	for _, err := range []error{cerr, rerr, serr} {
		if err != nil {
			return err
		}
	}

	return nil
}

// Ensure compatibility with package errors.
var _ interface {
	error
	// https://pkg.go.dev/errors#Is
	Is(error) bool
	// https://pkg.go.dev/errors#Unwrap
	Unwrap() error
} = &Error{}

// An Error is an error value produced by the pidfd_* family of syscalls.
type Error struct {
	FD, PID int
	Err     error
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("pidfd %d: pid: %d: %v", e.FD, e.PID, e.Err)
}

// Is implements errors.Is comparison.
func (e *Error) Is(target error) bool {
	switch target {
	case os.ErrNotExist:
		// No such process.
		return errors.Is(e.Err, esrch)
	default:
		// Fall back to the next error in the chain.
		return false
	}
}

// Unwrap implements errors.Unwrap functionality.
func (e *Error) Unwrap() error { return e.Err }
