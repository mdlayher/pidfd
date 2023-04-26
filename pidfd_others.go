//go:build !linux

package pidfd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
)

var esrch = errors.New("")

// errUnimplemented is returned by all functions on non-Linux platforms.
var errUnimplemented = fmt.Errorf("pidfd: not implemented on %s", runtime.GOOS)

func open(_ int) (*File, error) { return nil, errUnimplemented }

type conn struct{}

func (*File) sendSignal(_ os.Signal) error { return errUnimplemented }
func (*File) wait(_ context.Context) error { return errUnimplemented }

func (*conn) Close() error                      { return errUnimplemented }
func (*conn) SetReadDeadline(_ time.Time) error { return errUnimplemented }
