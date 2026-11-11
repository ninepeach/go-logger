//go:build linux || darwin

package logger

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

var localSyslogPaths = [...]string{"/dev/log", "/var/run/syslog", "/var/run/log"}

func dialLocal(address string, timeout time.Duration) (syslogConn, error) {
	paths := localSyslogPaths[:]
	if address != "" {
		paths = []string{address}
	}
	var failures []error
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Deadline: deadline}
	for _, path := range paths {
		for _, network := range [...]string{"unixgram", "unix"} {
			c, err := dialer.Dial(network, path)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if err := c.SetWriteDeadline(time.Time{}); err != nil {
				failures = append(failures, fmt.Errorf("logger: local write deadline: %w", errors.Join(err, c.Close())))
				continue
			}
			return syslogConn{c, network}, nil
		}
	}
	return syslogConn{}, errors.Join(failures...)
}

func classifyDatagram(err error) datagramAction {
	if errors.Is(err, syscall.EMSGSIZE) || errors.Is(err, syscall.ENOBUFS) {
		return datagramKeep
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EBADF) {
		return datagramDisconnect
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return datagramSuppress
	}
	return datagramKeep
}
