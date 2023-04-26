package pidfd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
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
	return f.wait(ctx)
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
