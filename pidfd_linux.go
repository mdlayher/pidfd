//go:build linux

package pidfd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mdlayher/socket"
	"golang.org/x/sys/unix"
)

// esrch is the "no such process" errno.
var esrch = unix.ESRCH

// A conn backs File for Linux pidfds. We can use socket.Conn directly on Linux
// to implement most of the necessary methods.
type conn = socket.Conn

// open opens a pidfd File.
func open(pid int) (*File, error) {
	// Open nonblocking: we always use asynchronous I/O anyway with
	// *socket.Conn.
	//
	// TODO(mdlayher): plumb in more pidfd_open flags if it ever makes sense to
	// do so.
	fd, err := unix.PidfdOpen(pid, unix.PIDFD_NONBLOCK)
	if err != nil {
		// No FD to annotate the error yet.
		return nil, &Error{PID: pid, Err: err}
	}

	c, err := socket.New(fd, "pidfd")
	if err != nil {
		return nil, err
	}

	rc, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}

	return &File{
		pid: pid,
		c:   c,
		rc:  rc,
	}, nil
}

// sendSignal signals the process referred to by File.
func (f *File) sendSignal(signal os.Signal) error {
	ssig, ok := signal.(unix.Signal)
	if !ok {
		return fmt.Errorf("pidfd: invalid signal type for File.SendSignal: %T", signal)
	}

	// From pidfd_send_signal(2):
	//
	// "If the info argument is a NULL pointer, this is equivalent to specifying
	// a pointer to a siginfo_t buffer whose fields match the values that are
	// implicitly supplied when a signal is sent using kill(2)"
	//
	// "The flags argument is reserved for future use; currently, this argument
	// must be specified as 0."
	return f.wrap(f.c.PidfdSendSignal(ssig, nil, 0))
}

func (f *File) wait(ctx context.Context) error {
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

	var werr error

	rerr := f.rc.Read(func(fd uintptr) bool {
		var si unix.Siginfo
		err := unix.Waitid(unix.P_PIDFD, int(fd), &si, unix.WEXITED|unix.WNOWAIT, nil)
		switch err {
		case unix.EAGAIN:
			return false
		default:
			werr = err
			return true
		}
	})

	// The operation has unblocked. Observe context cancelation, tidy up the
	// cancelation goroutine, and disarm the read deadline timer.
	cerr := ctx.Err()
	cancel()
	wg.Wait()
	serr := f.c.SetReadDeadline(time.Time{})

	// Context cancel takes priority over all other errors.
	for _, err := range []error{cerr, rerr, werr, serr} {
		if err != nil {
			return err
		}
	}

	return nil
}

// wrap annotates and returns an *Error with File metadata. If err is nil, wrap
// is a no-op.
func (f *File) wrap(err error) error {
	if err == nil {
		return nil
	}

	// Best effort.
	var fd int
	_ = f.rc.Control(func(cfd uintptr) {
		fd = int(cfd)
	})

	return &Error{
		PID: f.pid,
		FD:  fd,
		Err: err,
	}
}
