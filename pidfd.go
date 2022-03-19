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
	// To observe context cancelation, we will set a past deadline in a
	// goroutine to force blocked Reads to unblock.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	go func() {
		defer wg.Done()

		// Immediately unblock pending reads.q
		<-ctx.Done()
		_ = f.c.SetReadDeadline(time.Unix(0, 1))
	}()

	// Wait for the poller to indicate readiness, unless the context is canceled
	// first via a deadline adjustment. For values of n:
	//  - 0: init
	//  - 1: callback invoked, wait for process exit
	//  - 2: callback invoked due to process exit, stop waiting
	var n uint32
	_ = f.rc.Read(func(_ uintptr) bool {
		if ctx.Err() != nil {
			return true
		}

		return atomic.AddUint32(&n, 1) == 2
	})

	// Tidy up and make sure we observe context cancelation.
	err := ctx.Err()
	cancel()
	wg.Wait()

	// Now that the goroutine has stopped, disarm the deadline we used to
	// interrupt the blocking Read above.
	if serr := f.c.SetReadDeadline(time.Time{}); serr != nil {
		return serr
	}

	return err
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
