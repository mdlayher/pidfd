//go:build !linux

package pidfd

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

var esrch = errors.New("")

// errUnimplemented is returned by all functions on non-Linux platforms.
var errUnimplemented = fmt.Errorf("pidfd: not implemented on %s", runtime.GOOS)

func open(_ int) (*File, error) { return nil, errUnimplemented }

type conn struct{}

func (*File) sendSignal(_ os.Signal) error { return errUnimplemented }

func (*conn) Close() error { return errUnimplemented }
